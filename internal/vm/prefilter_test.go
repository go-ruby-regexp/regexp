package vm

import (
	"strings"
	"testing"

	"github.com/go-onigmo/regexp/internal/ast"
	"github.com/go-onigmo/regexp/internal/compile"
	"github.com/go-onigmo/regexp/internal/syntax"
)

// TestAnalyzeLiteralPrefix verifies a leading run of fixed bytes is extracted as
// a required literal prefix and pins the first-byte set.
func TestAnalyzeLiteralPrefix(t *testing.T) {
	pf := analyze(build(t, "abc"))
	if pf.prefix != "abc" {
		t.Fatalf("prefix = %q, want abc", pf.prefix)
	}
	if !pf.usable() {
		t.Fatal("literal-prefix prefilter must be usable")
	}
	if pf.anchored {
		t.Fatal("plain literal must not be anchored")
	}
	if !pf.hasFirst || !pf.first.has('a') || pf.first.count() != 1 {
		t.Fatalf("first byte set must be exactly {a}, got count %d", pf.first.count())
	}
}

// TestAnalyzeAnchored verifies \A collapses the scan to offset 0.
func TestAnalyzeAnchored(t *testing.T) {
	pf := analyze(build(t, `\Aabc`))
	if !pf.anchored {
		t.Fatal(`\A pattern must be anchored`)
	}
	if !pf.usable() {
		t.Fatal("anchored prefilter must be usable")
	}
	// The literal after \A is still recorded (harmless: nextStart short-circuits
	// on anchored first).
	if pf.prefix != "abc" {
		t.Fatalf("prefix = %q, want abc", pf.prefix)
	}
}

// TestAnalyzeClassFirst verifies a byte-oriented leading class yields a
// first-byte set and no literal prefix.
func TestAnalyzeClassFirst(t *testing.T) {
	pf := analyze(build(t, "[ax]bc"))
	if pf.prefix != "" {
		t.Fatalf("class-led pattern must have empty literal prefix, got %q", pf.prefix)
	}
	if !pf.hasFirst || !pf.first.has('a') || !pf.first.has('x') || pf.first.count() != 2 {
		t.Fatalf("first set must be {a,x}, count %d", pf.first.count())
	}
	if !pf.usable() {
		t.Fatal("class-led prefilter must be usable")
	}
}

// TestAnalyzeCharThenClass verifies a literal byte followed by a class extends
// the prefix only with the fixed byte and then stops, the class ending it.
func TestAnalyzeCharThenClass(t *testing.T) {
	pf := analyze(build(t, "a[bc]"))
	if pf.prefix != "a" {
		t.Fatalf("prefix = %q, want a", pf.prefix)
	}
	if !pf.first.has('a') || pf.first.count() != 1 {
		t.Fatalf("first set must be {a}, count %d", pf.first.count())
	}
}

// TestAnalyzeNegatedClass verifies a negated byte class produces the complement
// first-byte set (still a proper subset, so usable).
func TestAnalyzeNegatedClass(t *testing.T) {
	pf := analyze(build(t, "[^a]b"))
	if pf.first.has('a') {
		t.Fatal("[^a] first set must exclude 'a'")
	}
	if !pf.first.has('b') || !pf.first.has(0) {
		t.Fatal("[^a] first set must include other bytes")
	}
	if pf.first.count() != 255 {
		t.Fatalf("[^a] first set count = %d, want 255", pf.first.count())
	}
	if !pf.usable() {
		t.Fatal("negated single-byte class is a proper subset, must be usable")
	}
}

// TestAnalyzeUnusable verifies patterns whose leading atom is unconstrained give
// no exploitable prefilter, so the scan stays on its plain path.
func TestAnalyzeUnusable(t *testing.T) {
	// A bare dot must be unusable.
	if analyze(build(t, ".")).usable() {
		t.Fatal("bare dot must yield an unusable prefilter")
	}
	// A fold-led pattern must be unusable (rune-aware first atom).
	if analyze(build(t, "(?i)abc")).usable() {
		t.Fatal("(?i)-led pattern must be unusable")
	}
	// A property-led pattern must be unusable.
	if analyze(build(t, `\p{L}x`)).usable() {
		t.Fatal("property-led pattern must be unusable")
	}
	// A leading dot before a single-byte literal must be unusable: the dot ends the
	// prefix/first analysis and the lone trailing byte is too short (< 2) for the
	// required-interior-literal filter. (A leading dot before a 2+ byte literal,
	// e.g. ".abc", is now usable via that interior filter — see
	// TestAnalyzeRequiredLiteral.)
	if analyze(build(t, ".a")).usable() {
		t.Fatal("leading-dot then single byte must be unusable")
	}
	// A leading lookahead must be unusable.
	if analyze(build(t, "(?=a)b")).usable() {
		t.Fatal("lookahead-led pattern must be unusable")
	}
	// A leading alternation with a non-reducible branch (dot) must be unusable:
	// one branch's first byte is unconstrained, so the union cannot be trusted.
	if analyze(build(t, "a|.b")).usable() {
		t.Fatal("alternation with a dot branch must be unusable")
	}
	// A bare optional whose continuation can match empty (a*) is unconstrained.
	if analyze(build(t, "a*")).usable() {
		t.Fatal("a* must be unusable (matches empty, any first byte)")
	}
	// An alternation with an empty branch followed by an unconstrained atom must
	// be unusable: the empty branch lets the dot lead.
	if analyze(build(t, "(?:a|).")).usable() {
		t.Fatal("empty-branch alternation before a dot must be unusable")
	}
}

// TestAnalyzeAlternationFirstBytes verifies a leading alternation of byte-
// determinable branches yields the union first-byte set (the alternation-aware
// optimizer pass), with no literal prefix.
func TestAnalyzeAlternationFirstBytes(t *testing.T) {
	pf := analyze(build(t, "foo|bar"))
	if pf.prefix != "" {
		t.Fatalf("alternation must have no single literal prefix, got %q", pf.prefix)
	}
	if !pf.usable() || !pf.hasFirst {
		t.Fatal("foo|bar must yield a usable first-byte set")
	}
	if !pf.first.has('f') || !pf.first.has('b') || pf.first.count() != 2 {
		t.Fatalf("first set must be {f,b}, count %d", pf.first.count())
	}
	// Three-way nested split.
	pf3 := analyze(build(t, "cat|dog|emu"))
	if !pf3.first.has('c') || !pf3.first.has('d') || !pf3.first.has('e') || pf3.first.count() != 3 {
		t.Fatalf("cat|dog|emu first set must be {c,d,e}, count %d", pf3.first.count())
	}
	// Alternation of byte classes unions the classes.
	pfc := analyze(build(t, "[ax]|[by]"))
	for _, b := range []byte{'a', 'x', 'b', 'y'} {
		if !pfc.first.has(b) {
			t.Fatalf("[ax]|[by] first set missing %q", b)
		}
	}
	if pfc.first.count() != 4 {
		t.Fatalf("[ax]|[by] first set count = %d, want 4", pfc.first.count())
	}
	// A leading optional a*b: first byte is 'a' (one+ a's) or 'b' (zero a's).
	pfo := analyze(build(t, "a*b"))
	if !pfo.usable() || !pfo.first.has('a') || !pfo.first.has('b') || pfo.first.count() != 2 {
		t.Fatalf("a*b first set must be {a,b}, count %d", pfo.first.count())
	}
}

// TestFirstByteSetDepthLimit verifies a deeply nested leading alternation beyond
// the recursion bound is declared non-reducible (given up), never mis-analyzed.
func TestFirstByteSetDepthLimit(t *testing.T) {
	// Build a synthetic chain of splits deeper than maxFirstByteDepth, each
	// branching to an OpChar, terminated by a non-reducible OpAny so the deepest
	// recursion is what trips the bound rather than a clean leaf. A chain of
	// right-nested splits models a|b|c|… with many alternatives.
	var insts []compile.Inst
	n := maxFirstByteDepth + 5
	for i := 0; i < n; i++ {
		// split at 2i: X -> a char at (2i+1), Y -> next split at (2i+2)
		insts = append(insts, compile.Inst{Op: compile.OpSplit, X: 2*i + 1, Y: 2*i + 2})
		insts = append(insts, compile.Inst{Op: compile.OpChar, B: 'a'})
	}
	// Final fallthrough atom is non-reducible so the deepest branch fails; but the
	// depth bound should already have fired first.
	insts = append(insts, compile.Inst{Op: compile.OpAny})
	var set byteSet
	if firstByteSet(insts, 0, &set, 0) {
		t.Fatal("a split nest deeper than the bound must be non-reducible")
	}
}

// TestFirstByteSetOutOfRange covers the bounds guards for a pc past the program
// and a negative pc.
func TestFirstByteSetOutOfRange(t *testing.T) {
	var set byteSet
	if firstByteSet(nil, 0, &set, 0) {
		t.Fatal("empty program must be non-reducible")
	}
	// A negative pc (a malformed branch target) must fail rather than panic.
	if firstByteSet([]compile.Inst{{Op: compile.OpChar, B: 'a'}}, -1, &set, 0) {
		t.Fatal("negative pc must be non-reducible")
	}
	// A save at the very end (pc++ runs off the end) must fail rather than panic.
	insts := []compile.Inst{{Op: compile.OpSave, Slot: 0}}
	if firstByteSet(insts, 0, &set, 0) {
		t.Fatal("trailing save with no atom must be non-reducible")
	}
}

// TestFirstByteSetAtomicBegin covers the OpAtomicBegin zero-width pass-through:
// an atomic group leading the pattern is stepped over to its first atom. (?>ab)c
// begins with OpAtomicBegin then OpChar 'a'.
func TestFirstByteSetAtomicBegin(t *testing.T) {
	pf := analyze(build(t, `(?>a|b)c`))
	if !pf.usable() || !pf.first.has('a') || !pf.first.has('b') || pf.first.count() != 2 {
		t.Fatalf("(?>a|b)c first set must be {a,b}, count %d", pf.first.count())
	}
}

// TestFirstByteSetJmpBackEdge covers the loop back-edge guard: a synthetic split
// whose Y branch jumps backwards (a *-loop body) is declared non-reducible so a
// possibly-empty loop never produces an unsound set.
func TestFirstByteSetJmpBackEdge(t *testing.T) {
	// pc0: split X->1, Y->2 ; pc1: char 'a' ; pc2: jmp back to 0
	insts := []compile.Inst{
		{Op: compile.OpSplit, X: 1, Y: 2},
		{Op: compile.OpChar, B: 'a'},
		{Op: compile.OpJmp, X: 0},
	}
	var set byteSet
	if firstByteSet(insts, 0, &set, 0) {
		t.Fatal("a backward jmp (loop back-edge) must be non-reducible")
	}
}

// TestFirstByteSetJmpForward covers the forward-jmp follow path: a branch that
// reaches its leading atom through an unconditional forward jump.
func TestFirstByteSetJmpForward(t *testing.T) {
	// pc0: split X->1, Y->2 ; pc1: char 'a' ; pc2: jmp forward to 3 ; pc3: char 'b'
	insts := []compile.Inst{
		{Op: compile.OpSplit, X: 1, Y: 2},
		{Op: compile.OpChar, B: 'a'},
		{Op: compile.OpJmp, X: 3},
		{Op: compile.OpChar, B: 'b'},
	}
	var set byteSet
	if !firstByteSet(insts, 0, &set, 0) || !set.has('a') || !set.has('b') || set.count() != 2 {
		t.Fatalf("forward-jmp branch first set must be {a,b}, count %d", set.count())
	}
}

// TestFirstByteSetRuneClassBranch covers the non-reducible-class branch inside an
// alternation: a rune-aware (\p{…}-bearing) class in one branch makes the whole
// union non-reducible.
func TestFirstByteSetRuneClassBranch(t *testing.T) {
	pf := analyze(build(t, `a|[\p{L}]`))
	if pf.usable() {
		t.Fatal("alternation with a rune-aware class branch must be unusable")
	}
}

// TestAnalyzeFoldClassUnusable verifies a folded (/i) class is rune-aware and
// therefore not byte-reducible: classFirstBytes refuses it and the prefix ends.
func TestAnalyzeFoldClassUnusable(t *testing.T) {
	pf := analyze(build(t, `(?i)[a-z]x`))
	if pf.usable() {
		t.Fatal("(?i)[a-z]-led pattern must be unusable (rune-aware class)")
	}
}

// TestAnalyzeBeginLine verifies ^ and \G are stepped over without claiming a
// byte prefix, but a following literal is still captured.
func TestAnalyzeBeginLine(t *testing.T) {
	pf := analyze(build(t, `^abc`))
	if pf.anchored {
		t.Fatal("^ is not \\A: must not set anchored")
	}
	if pf.prefix != "abc" {
		t.Fatalf("prefix after ^ = %q, want abc", pf.prefix)
	}
	pfG := analyze(build(t, `\Gabc`))
	if pfG.prefix != "abc" {
		t.Fatalf("prefix after \\G = %q, want abc", pfG.prefix)
	}
}

// TestClassFirstBytesRuneAware verifies classFirstBytes rejects rune-aware
// classes (with code-point ranges or properties).
func TestClassFirstBytesRuneAware(t *testing.T) {
	// A class with a RuneRange (e.g. \R-style multibyte member) is not reducible.
	in := compile.Inst{Op: compile.OpClass, RuneRanges: []ast.RuneClassRange{{Lo: 0x100, Hi: 0x200}}}
	if _, ok := classFirstBytes(in); ok {
		t.Fatal("class with code-point ranges must not be byte-reducible")
	}
	// A class with a property member is not reducible.
	in2 := compile.Inst{Op: compile.OpClass, Props: []ast.PropRef{{Name: "L"}}}
	if _, ok := classFirstBytes(in2); ok {
		t.Fatal("class with property member must not be byte-reducible")
	}
}

// TestAnalyzeRequiredLiteral verifies the required-interior-literal extraction:
// the longest fixed byte run on the mandatory spine, across quantifiers, captured
// groups, and lookarounds, but never across an alternation or through optional
// content.
func TestAnalyzeRequiredLiteral(t *testing.T) {
	cases := []struct{ pat, want string }{
		// Classic interior literals with no anchor or leading literal.
		{`\d+foo\d+`, "foo"},
		{`[ab]*xyz[cd]*`, "xyz"},
		{`x\d+world`, "world"}, // leading 'x' (len 1) loses to "world"
		// A literal that spans a required captured group stays one run (OpSave and
		// OpReturn are zero-width pass-throughs).
		{`foo(bar)baz`, "foobarbaz"},
		// A quantified group is optional: only the forced bytes around it count.
		{`pre\w*post`, "post"}, // "pre" (3) loses to "post" (4)
		// Lookarounds are zero-width: the walk skips the asserted body and keeps the
		// forced continuation.
		{`(?=foo)bar`, "bar"},
		{`(?!foo)barbaz`, "barbaz"},
		// An alternation forks the spine: nothing past it is forced, and a 1-byte
		// lead is too short, so no required literal.
		{`a(b|c)d`, ""},
		{`foo|bar`, ""},
		// All-optional content has no required literal.
		{`a?b?c?`, ""},
		// A single interior byte is below the 2-byte threshold.
		{`\d-\d`, ""},
		// Under /i the literals are rune-aware (OpFoldChar), not fixed bytes.
		{`(?i)x?required`, ""},
	}
	for _, c := range cases {
		if got := requiredLiteral(build(t, c.pat).Insts); got != c.want {
			t.Errorf("requiredLiteral(%q) = %q, want %q", c.pat, got, c.want)
		}
	}
}

// TestRequiredLiteralUsable verifies a pattern whose only exploitable structure
// is a required interior literal is reported usable (so the whole-haystack gate
// runs), while one whose interior is too short is not.
func TestRequiredLiteralUsable(t *testing.T) {
	pf := analyze(build(t, `\d+foo\d+`))
	if pf.prefix != "" || pf.anchored {
		t.Fatalf("\\d+foo\\d+ must have no prefix/anchor, got prefix %q anchored %v", pf.prefix, pf.anchored)
	}
	if pf.required != "foo" {
		t.Fatalf("required = %q, want foo", pf.required)
	}
	if !pf.usable() {
		t.Fatal("a required-interior-literal prefilter must be usable")
	}
	// A leading-dot then single byte: no prefix, no first set, interior too short.
	if analyze(build(t, `.a`)).usable() {
		t.Fatal(".a must be unusable (interior literal < 2 bytes)")
	}
}

// TestRequiredLiteralDegenerateGuards covers the defensive guards in
// requiredLiteral that protect against malformed programs: a quantifier split
// whose GuardTo does not advance, a lookaround whose continuation does not
// advance, and the step bound. None can occur from the compiler, so they are
// exercised with synthetic instruction lists.
func TestRequiredLiteralDegenerateGuards(t *testing.T) {
	// A quantifier split whose GuardTo points back at itself: the walk must stop
	// with whatever run it had, not loop. Two leading chars give a run "ab" the
	// flush keeps before the bad split.
	bad := []compile.Inst{
		{Op: compile.OpChar, B: 'a'},
		{Op: compile.OpChar, B: 'b'},
		{Op: compile.OpSplit, Quant: true, GuardTo: 2}, // GuardTo == pc (no advance)
	}
	if got := requiredLiteral(bad); got != "ab" {
		t.Fatalf("non-advancing quant GuardTo: got %q, want ab", got)
	}
	// A lookaround whose continuation X does not advance must likewise stop.
	badLook := []compile.Inst{
		{Op: compile.OpChar, B: 'a'},
		{Op: compile.OpChar, B: 'b'},
		{Op: compile.OpLook, X: 2}, // X == pc (no advance)
	}
	if got := requiredLiteral(badLook); got != "ab" {
		t.Fatalf("non-advancing look X: got %q, want ab", got)
	}
	// A pc-out-of-range start (empty program) returns the empty run immediately.
	if got := requiredLiteral(nil); got != "" {
		t.Fatalf("empty program: got %q, want empty", got)
	}
	// A stray OpLookEnd (one not jumped over by its OpLook) hits the default stop
	// arm, flushing the run accumulated before it.
	stray := []compile.Inst{
		{Op: compile.OpChar, B: 'a'},
		{Op: compile.OpChar, B: 'b'},
		{Op: compile.OpLookEnd},
	}
	if got := requiredLiteral(stray); got != "ab" {
		t.Fatalf("stray OpLookEnd: got %q, want ab", got)
	}
	// A program of only zero-width pass-throughs accumulates no literal and runs off
	// the end without flushing a 2+ run.
	var passes []compile.Inst
	for i := 0; i < 5; i++ {
		passes = append(passes, compile.Inst{Op: compile.OpReturn})
	}
	if got := requiredLiteral(passes); got != "" {
		t.Fatalf("all-pass-through program: got %q, want empty", got)
	}
}

// TestRequiredGateRejectsMissingLiteral verifies the whole-haystack gate in Match
// rejects an input lacking the required interior literal without running the VM at
// any position, and admits one containing it (the VM then decides the match).
func TestRequiredGateRejectsMissingLiteral(t *testing.T) {
	prog := build(t, `\d+foo\d+`)
	// Missing "foo": gated out.
	if _, ok, _ := Match(prog, "12bar34", DefaultBudget); ok {
		t.Fatal("haystack without the required literal must not match")
	}
	// Present "foo" with surrounding digits: matches.
	if _, ok, _ := Match(prog, "12foo34", DefaultBudget); !ok {
		t.Fatal("haystack with literal and digits must match")
	}
	// Present "foo" but no surrounding digits: gate passes, VM rejects.
	if _, ok, _ := Match(prog, "foo", DefaultBudget); ok {
		t.Fatal("literal present but pattern unsatisfied must not match")
	}
}

// TestNextStartAnchored covers both anchored outcomes.
func TestNextStartAnchored(t *testing.T) {
	pf := prefilter{anchored: true}
	if got := pf.nextStart("hello", 0); got != 0 {
		t.Fatalf("anchored nextStart(_,0) = %d, want 0", got)
	}
	if got := pf.nextStart("hello", 1); got != -1 {
		t.Fatalf("anchored nextStart(_,1) = %d, want -1", got)
	}
}

// TestNextStartPrefix covers the literal-prefix found and not-found branches.
func TestNextStartPrefix(t *testing.T) {
	pf := prefilter{prefix: "cat"}
	if got := pf.nextStart("a cat sat", 0); got != 2 {
		t.Fatalf("prefix nextStart = %d, want 2", got)
	}
	if got := pf.nextStart("a dog ran", 0); got != -1 {
		t.Fatalf("prefix not-found nextStart = %d, want -1", got)
	}
	// Searching from past the only occurrence finds nothing.
	if got := pf.nextStart("a cat sat", 3); got != -1 {
		t.Fatalf("prefix nextStart from past occurrence = %d, want -1", got)
	}
}

// TestNextStartFirstByte covers the first-byte-set found and exhausted branches.
func TestNextStartFirstByte(t *testing.T) {
	var pf prefilter
	pf.hasFirst = true
	pf.first.add('z')
	if got := pf.nextStart("aaazbbb", 0); got != 3 {
		t.Fatalf("first-byte nextStart = %d, want 3", got)
	}
	if got := pf.nextStart("aaabbb", 0); got != -1 {
		t.Fatalf("first-byte exhausted nextStart = %d, want -1", got)
	}
}

// TestNextStartFallthrough covers the no-constraint path: a prefilter with no
// anchored/prefix/first returns the position unchanged. (Reachable only by a
// direct call; Match guards this with usable().)
func TestNextStartFallthrough(t *testing.T) {
	var pf prefilter
	if got := pf.nextStart("hello", 2); got != 2 {
		t.Fatalf("empty prefilter nextStart = %d, want 2 (unchanged)", got)
	}
}

// TestUsableFullFirstSet verifies a first-byte set covering all 256 bytes is
// reported unusable (it would never skip a position).
func TestUsableFullFirstSet(t *testing.T) {
	var pf prefilter
	pf.hasFirst = true
	for i := 0; i < 256; i++ {
		pf.first.add(byte(i))
	}
	if pf.usable() {
		t.Fatal("a full 256-byte first set must be reported unusable")
	}
	// And an empty/unset prefilter is unusable.
	var empty prefilter
	if empty.usable() {
		t.Fatal("empty prefilter must be unusable")
	}
}

// TestPrefilterTransparency is the core proof: for a spread of patterns the
// optimized search returns byte-identical spans to a brute-force scan that tries
// every start position with no prefilter. This asserts the prefilter never
// changes a result. It is oracle-independent (no external Ruby).
func TestPrefilterTransparency(t *testing.T) {
	cases := []struct{ pat, input string }{
		{"abc", "xxabcxx"},
		{"abc", "no match here"},
		{"abc", "abc"},
		{"abc", ""},
		{`\Aabc`, "abcdef"},
		{`\Aabc`, "xabcdef"},
		{"[ax]y", "qqaybb"},
		{"[ax]y", "qqxybb"},
		{"[^a]b", "aab cb"},
		{"a[bc]d", "zzabdzz"},
		{"cat", "the cat sat on the cat mat"},
		{"cat", strings.Repeat("dog ", 100) + "cat"},
		{"x", strings.Repeat("y", 500)},
		{"end$", "the end"},
		{"^go", "go\nstop"},
		// Alternation first-byte set (alternation-aware pass).
		{"foo|bar", "xx bar yy"},
		{"foo|bar", "nothing here"},
		{"cat|dog|emu", "see the emu run"},
		{"cat|dog|emu", strings.Repeat("zzz ", 50) + "dog"},
		{"[ax]|[by]", "qqqybbb"},
		{"a*b", "cccab"},
		{"a*b", "cccb"},
		{"a*b", "ccc"},
		{`(?>a|b)c`, "zzbczz"},
		{`(?>foo)bar`, "xfoobary"},
		// Required interior literal (no anchor, no leading literal): the gate must
		// reject haystacks lacking the literal and pass those containing it, with the
		// VM still deciding the actual match.
		{`\d+foo\d+`, "12foo34"},
		{`\d+foo\d+`, "12bar34"},     // literal absent: gated out, no match
		{`\d+foo\d+`, "foo"},         // literal present but no surrounding digits: no match
		{`[ab]*xyz[cd]*`, "aaxyzcc"},
		{`[ab]*xyz[cd]*`, "nothing here at all"},
		{`.abc`, "Zabc"},
		{`.abc`, "no a-b-c run"},
		{`.abc`, "abc"},             // dot needs a leading byte: no match at 0
		{`(?=foo)bar`, "barfoo no"}, // lookahead-led; required "bar" present but assertion fails
		{`x\d+world`, "x9world"},
		{`x\d+world`, "x9planet"},   // "world" absent: gated out
		{`foo(bar)baz`, "zfoobarbazz"},
		{`foo(bar)baz`, "foobarbaz"},
	}
	for _, c := range cases {
		fast := build(t, c.pat)
		fb, fe, fok := matchSpan(t, c.pat, c.input)
		bb, be, bok := bruteForce(t, fast, c.input)
		if fok != bok || fb != bb || fe != be {
			t.Errorf("pat %q input %q: prefilter (%d,%d,%v) != brute (%d,%d,%v)",
				c.pat, c.input, fb, fe, fok, bb, be, bok)
		}
	}
}

// bruteForce runs every start position through the VM with no prefilter, the
// reference behaviour the prefilter must reproduce exactly.
func bruteForce(t *testing.T, prog *compile.Program, input string) (int, int, bool) {
	t.Helper()
	for start := 0; start <= len(input); start++ {
		caps := make([]int, prog.NumSlots())
		for i := range caps {
			caps[i] = -1
		}
		m := &machine{prog: prog, input: input, budget: DefaultBudget, visited: map[int64]bool{}, memoize: !prog.HasBackref && !prog.HasCall}
		res, ok, err := m.run(start, caps)
		if err != nil {
			t.Fatalf("brute run: %v", err)
		}
		if ok {
			return res[0], res[1], true
		}
	}
	return -1, -1, false
}

// --- Benchmarks: literal-prefix prefilter skipping a long non-matching scan. ---

// benchHaystack is a long string with no occurrence of the literal "needle",
// forcing the worst case: the scan must reject every start position. With the
// prefilter, strings.Index rejects the whole haystack in one pass; without it,
// the VM is invoked at every offset.
var benchHaystack = strings.Repeat("the quick brown fox jumps over the lazy dog. ", 2000)

func BenchmarkLiteralPrefixMiss(b *testing.B) {
	prog := mustBuild(b, "needle")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, _ := Match(prog, benchHaystack, DefaultBudget); ok {
			b.Fatal("unexpected match")
		}
	}
}

func BenchmarkLiteralPrefixHit(b *testing.B) {
	hay := benchHaystack + "needle"
	prog := mustBuild(b, "needle")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, _ := Match(prog, hay, DefaultBudget); !ok {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkFirstByteSetMiss(b *testing.B) {
	// A class-led pattern with no literal prefix: the byte-set scan skips every
	// position whose byte is not in {z}.
	prog := mustBuild(b, "[z]oo")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, _ := Match(prog, benchHaystack, DefaultBudget); ok {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkAlternationFirstByteMiss exercises the alternation-aware pass: a
// leading alternation with no common literal prefix still yields a first-byte
// set ({z,q,k}) the scan uses to skip non-matching positions.
func BenchmarkAlternationFirstByteMiss(b *testing.B) {
	prog := mustBuild(b, "zoo|quux|kite")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, _ := Match(prog, benchHaystack, DefaultBudget); ok {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkRequiredInteriorMiss exercises the required-interior-literal prefilter
// on a long haystack that never contains the literal: a pattern with no anchor and
// no leading literal (\d+needle\d+) whose mandatory interior "needle" lets a single
// strings.Contains reject the whole haystack, instead of invoking the VM at every
// one of the haystack's offsets. Compare against BenchmarkNoPrefilterMiss (same
// haystack, a leading-dot pattern the filter cannot exploit) for the speedup.
func BenchmarkRequiredInteriorMiss(b *testing.B) {
	prog := mustBuild(b, `\d+needle\d+`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, _ := Match(prog, benchHaystack, DefaultBudget); ok {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkNoPrefilterMiss is the baseline: a leading dot followed by a single
// byte defeats every prefilter (no anchor, no leading literal, no usable first
// byte, and the lone trailing byte is below the interior-literal threshold), so
// the VM runs at every start position. It bounds the speedup the prefilters buy on
// the same haystack. (A trailing literal such as ".needle" is now covered by the
// required-interior-literal filter — see BenchmarkRequiredInteriorMiss.) The
// digit-led tail matches nothing in the all-letters haystack, so the VM still
// rejects every offset, isolating the unfiltered cost.
func BenchmarkNoPrefilterMiss(b *testing.B) {
	prog := mustBuild(b, `.\d`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, _ := Match(prog, benchHaystack, DefaultBudget); ok {
			b.Fatal("unexpected match")
		}
	}
}

// mustBuild compiles a pattern for a benchmark, failing the benchmark on error.
func mustBuild(b *testing.B, pat string) *compile.Program {
	b.Helper()
	r, err := syntax.Parse(pat)
	if err != nil {
		b.Fatalf("parse %q: %v", pat, err)
	}
	return compile.Compile(r)
}
