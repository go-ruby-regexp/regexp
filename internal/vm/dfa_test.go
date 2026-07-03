package vm

import (
	"fmt"
	"testing"

	"github.com/go-ruby-regexp/regexp/internal/compile"
	"github.com/go-ruby-regexp/regexp/internal/syntax"
)

// compileFor parses and compiles a pattern for the DFA-vs-VM differential tests.
func compileForDFA(t *testing.T, pat string) *compile.Program {
	t.Helper()
	res, err := syntax.ParseEnc(pat, syntax.UTF8)
	if err != nil {
		t.Fatalf("parse %q: %v", pat, err)
	}
	return compile.CompileEnc(res, compile.UTF8)
}

// vmSpan runs the backtracking VM and returns the whole-match [begin,end) span and
// whether it matched, the reference the DFA must agree with.
func vmSpan(t *testing.T, prog *compile.Program, input string) (int, int, bool) {
	t.Helper()
	caps, ok, err := Match(prog, input, DefaultBudget)
	if err != nil {
		t.Fatalf("vm error: %v", err)
	}
	if !ok {
		return -1, -1, false
	}
	return caps[0], caps[1], true
}

// dfaInputs is a varied set of haystacks exercised against every DFA-eligible
// pattern: empty, ASCII, multibyte UTF-8, newlines, and mixed content.
var dfaInputs = []string{
	"", "a", "ab", "abc", "aaa", "aaab", "b", "xby",
	"hello world", "HELLO", "Hello123World", "  spaces  ",
	"123", "12345", "x12345y", "no digits here",
	"café", "naïve", "αβγ", "Ωμέγα", "é", "aébc",
	"δδδγγ", "δγ", "γγ", "δδγγ", "中中中文", "文x", "αββββγ",
	"foo@bar.com", "user.name+tag@example.co.uk", "not an email",
	"https://example.com/path", "http://a.b/c", "ftp://x",
	"line1\nline2\nline3", "\n\n", "trailing\n",
	"a.b.c.d", "1.2.3.4", "255.255.255.255", "999.999",
	"the quick brown fox", "AAAAAAAAAAAAAAAAAAAA!", "aaaaaaaaaaaaaaaab",
}

// dfaPatterns is a broad set of patterns within the DFA subset (no backref, call,
// lookaround, atomic group): literals, classes, the dot, anchors, alternation,
// greedy / lazy / bounded quantifiers, Unicode, and combinations.
var dfaPatterns = []string{
	`abc`, `a.c`, `a|ab`, `ab|a`, `foo|bar|baz`,
	`a*`, `a+`, `a?`, `a*b`, `a+b`, `a?b`, `a*?b`, `a+?b`,
	`a{2}`, `a{2,}`, `a{2,4}`, `a{0,3}`, `[0-9]{2,4}`,
	`[a-z]+`, `[a-zA-Z]+`, `[^a-z]+`, `\w+`, `\d+`, `\s+`, `\W+`,
	`.`, `.+`, `.*`, `.*?`, `.x`, `a.*b`, `a.*?b`,
	`\Aabc`, `abc\z`, `abc\Z`, `^foo`, `foo$`, `^$`,
	`\p{L}+`, `\p{N}+`, `(?i)abc`, `(?i)[a-z]+`, `(?i)é`,
	`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`,
	`https?://[A-Za-z0-9./_-]+`,
	`(a|b)*c`, `(ab)+`, `(foo|bar)+`, `x(y|z)?w`,
	`\Aa*b`, `\A(a*)*b`, `\A(a|aa)+b`, `(ab|a)*`, `a*a*a*b`,
	`[abc]+|[def]+`, `\d+\.\d+`,
	// A quantifier directly over a MULTI-BYTE literal: the + / * / {n,} must repeat
	// the whole code point (δ is 2 bytes, 中 is 3), so the leftmost match begins at
	// the first repetition, not mid-character. Regression for the δ+γ. wrong-span bug.
	`δ+γ.`, `δ*γ.`, `δ{2,}γ.`, `δ{2}γ.`, `δ+?γ.`, `(δ+)γ.`, `中+文`, `中*文`, `αβ+γ`,
}

// forceDFA builds the DFA engine for a program regardless of the performance gate
// that routes literal-prefix patterns to the VM, so the differential test
// validates the NFA executor's correctness on every subset pattern (including the
// literal ones the production path serves from the VM). It returns nil only when
// the program is genuinely outside the DFA subset.
func forceDFA(prog *compile.Program) *DFA {
	nfa, ok := buildNFA(prog)
	if !ok {
		return nil
	}
	pf := analyze(prog)
	d := &DFA{nfa: nfa, anchored: leadingAnchored(prog), pf: pf, usePF: pf.usable(), cache: newDFACache(nfa, prog.Enc), classRun: detectClassRun(prog)}
	n := len(nfa.insts)
	d.pool.New = func() any {
		r := &dfaRun{}
		r.th = *newDFAThreads(n)
		r.cs.bufA = make([]int32, n)
		r.cs.bufB = make([]int32, n)
		return r
	}
	return d
}

// TestDFAMatchesVM checks that for every DFA-eligible pattern, the lazy NFA's
// leftmost-first span agrees exactly with the backtracking VM on every input.
func TestDFAMatchesVM(t *testing.T) {
	for _, pat := range dfaPatterns {
		prog := compileForDFA(t, pat)
		dfa := forceDFA(prog)
		if dfa == nil {
			// Outside the subset (e.g. a construct the NFA builder rejects); skip — it
			// is exercised by the VM path.
			continue
		}
		for _, in := range dfaInputs {
			wb, we, wok := vmSpan(t, prog, in)
			gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
			if gok != wok || (gok && (gb != wb || ge != we)) {
				t.Errorf("pattern %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
					pat, in, gb, ge, gok, wb, we, wok)
			}
		}
	}
}

// TestMultibyteQuantifierSpan pins the leftmost byte span for a quantifier applied
// directly to a multi-byte literal and asserts all three engines agree. A
// multi-byte literal was formerly lowered to one byte literal per UTF-8 byte, so a
// trailing quantifier bound only the literal's last byte (e.g. /δ+/ became
// 0xCE(0xB4)+); δ+γ. on "δδδγγ" then started at byte 4 (the last δ) rather than 0.
// The literal is now one atom, so the quantifier repeats the whole code point and
// the search begins at the leftmost position. Spans are byte offsets (δ=2 bytes,
// 中=3); these match Ruby 4.0.5 / C Onigmo (which report the same spans in bytes).
func TestMultibyteQuantifierSpan(t *testing.T) {
	cases := []struct {
		pat, input   string
		wantB, wantE int
		wantOK       bool
	}{
		{`δ+γ.`, "δδδγγ", 0, 10, true}, // the reported bug: was (4,10)
		{`δ*γ.`, "δδδγγ", 0, 10, true},
		{`δ*γ.`, "γγ", 0, 4, true}, // zero δ, leftmost start 0
		{`δ{2,}γ.`, "δδδγγ", 0, 10, true},
		{`δ{2}γ.`, "δδγγ", 0, 8, true},  // δδ (0..4) γ (4..6) . consumes γ (6..8)
		{`δ+?γ.`, "δδδγγ", 0, 10, true}, // lazy still leftmost-first
		{`δ+γ.`, "δγ", -1, -1, false},   // one δ then only one rune: γ. needs two
		{`中+文`, "中中中文", 0, 12, true},    // 3-byte literal, greedy +
		{`中*文`, "文x", 0, 3, true},       // zero 中
		{`αβ+γ`, "αββββγ", 0, 12, true}, // + binds only to β, not αβ
	}
	for _, c := range cases {
		prog := compileForDFA(t, c.pat)
		// Backtracking VM.
		vb, ve, vok := vmSpan(t, prog, c.input)
		if vok != c.wantOK || (vok && (vb != c.wantB || ve != c.wantE)) {
			t.Errorf("VM /%s/ on %q: got (%d,%d,%v), want (%d,%d,%v)",
				c.pat, c.input, vb, ve, vok, c.wantB, c.wantE, c.wantOK)
		}
		// Cached / per-step NFA engine (DFA.Search internally uses the cached
		// transition table and falls back to the per-step simulation at mixed-width
		// positions, so this exercises both NFA executors).
		dfa := forceDFA(prog)
		if dfa == nil {
			t.Errorf("/%s/: expected DFA-eligible program", c.pat)
			continue
		}
		db, de, dok := dfa.Search(c.input, compile.UTF8, 0)
		if dok != c.wantOK || (dok && (db != c.wantB || de != c.wantE)) {
			t.Errorf("DFA /%s/ on %q: got (%d,%d,%v), want (%d,%d,%v)",
				c.pat, c.input, db, de, dok, c.wantB, c.wantE, c.wantOK)
		}
	}
}

// TestDFASubsetRejection checks that programs outside the subset yield a nil DFA
// so the caller falls back to the VM.
func TestDFASubsetRejection(t *testing.T) {
	for _, pat := range []string{
		`(a+)\1`,       // backreference
		`a(?=b)`,       // lookahead
		`(?<=a)b`,      // lookbehind
		`(?>a+)b`,      // atomic group
		`a++b`,         // possessive (lowers to atomic)
		`(?<n>a)\g<n>`, // subexpression call
		`a{0,1000}`,    // over-large bounded loop
	} {
		prog := compileForDFA(t, pat)
		if BuildDFA(prog) != nil {
			t.Errorf("pattern %q: expected nil DFA (outside subset)", pat)
		}
	}
}

// TestDFAAnchoredSubset confirms a \A-anchored program is detected and only
// matches at offset 0.
func TestDFAAnchoredSubset(t *testing.T) {
	prog := compileForDFA(t, `\A\w+`)
	dfa := BuildDFA(prog)
	if dfa == nil {
		t.Fatal("expected DFA for \\A\\w+")
	}
	if !dfa.anchored {
		t.Error("expected anchored=true for \\A\\w+")
	}
	if _, _, ok := dfa.Search("  abc", compile.UTF8, 0); ok {
		t.Error("\\A\\w+ should not match leading spaces")
	}
}

// TestDFARedosLinear confirms the lazy NFA stays linear (does not blow up) on the
// catastrophic-backtracking patterns, returning the same no-match the VM does.
func TestDFARedosLinear(t *testing.T) {
	long := ""
	for i := 0; i < 40; i++ {
		long += "a"
	}
	long += "!"
	for _, pat := range []string{`\A(a*)*b`, `\A(a|aa)+b`} {
		prog := compileForDFA(t, pat)
		dfa := BuildDFA(prog)
		if dfa == nil {
			t.Fatalf("expected DFA for %q", pat)
		}
		if _, _, ok := dfa.Search(long, compile.UTF8, 0); ok {
			t.Errorf("%q on %q: DFA matched, want no match", pat, long)
		}
	}
}

// fuzzInputs and a small generated set widen the differential coverage with
// randomized a/b/c strings against a/b/c patterns where backtracking corner cases
// (a|ab, leftmost-first) are most likely to surface.
func TestDFAMatchesVMGenerated(t *testing.T) {
	pats := []string{`a|ab`, `ab|a`, `a*ab`, `(a|ab)(c|bcd)`, `a?a?a?aaa`, `(a+)+`, `a*b*c*`}
	var inputs []string
	for _, base := range []string{"", "a", "b", "c", "ab", "ba", "abc", "aabb", "abab", "aaa", "bbb"} {
		inputs = append(inputs, base, base+base, "x"+base+"y")
	}
	for _, pat := range pats {
		prog := compileForDFA(t, pat)
		dfa := forceDFA(prog)
		if dfa == nil {
			continue
		}
		for _, in := range inputs {
			wb, we, wok := vmSpan(t, prog, in)
			gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
			if gok != wok || (gok && (gb != wb || ge != we)) {
				t.Errorf("pattern %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
					pat, in, gb, ge, gok, wb, we, wok)
			}
		}
	}
}

// TestDFAInteriorAssertions exercises the position-aware (context-dependent)
// closure path of the executor: patterns whose epsilon-closure crosses a
// position-dependent assertion (^ $ \A \z \Z \G) reached during stepping, so the
// add() slow path and each assertion arm run. Every case is cross-checked against
// the backtracking VM on inputs that hit both the assertion-holds and
// assertion-fails sides.
func TestDFAInteriorAssertions(t *testing.T) {
	cases := []string{
		`a$|b`,       // end-of-line assertion in an alternation frontier
		`a\z|b`,      // end-of-text
		`a\Z|b`,      // end-of-text-optional-newline
		`(?m:a$)|b`,  // end-of-line under /m
		`a|\Ab`,      // begin-of-text mid-alternation
		`a|^b`,       // begin-of-line mid-alternation
		`x*$`,        // assertion after a loop (closure crosses it from the loop body)
		`x*\z`,       // end-of-text after a loop
		`[ab]*$|c`,   // class-loop then end, alternated
		`\G[0-9]+|z`, // \G in an alternation frontier
	}
	inputs := []string{"", "a", "b", "ab", "a\n", "a\nb", "xxx", "xxx\n", "123z", "z", "c", "ac"}
	for _, pat := range cases {
		prog := compileForDFA(t, pat)
		dfa := forceDFA(prog)
		if dfa == nil {
			t.Fatalf("expected DFA for %q", pat)
		}
		for _, in := range inputs {
			wb, we, wok := vmSpan(t, prog, in)
			gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
			if gok != wok || (gok && (gb != wb || ge != we)) {
				t.Errorf("pattern %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
					pat, in, gb, ge, gok, wb, we, wok)
			}
		}
	}
}

// TestDFABinaryEncoding runs the DFA in ASCII8BIT (binary /n) mode, exercising the
// per-byte atom acceptance branches (fold / dot / class / property) on multi-byte
// input where binary mode advances one byte at a time, cross-checked against the
// VM under the same encoding.
func TestDFABinaryEncoding(t *testing.T) {
	for _, pat := range []string{`.`, `.+`, `[^a]+`, `\w+`, `(?i)a+`, `\p{L}+`, `[a-z]+`,
		`(?i)[a-z]+`, `\P{L}+`, `[^\d]+`} {
		res, err := syntax.ParseEnc(pat, syntax.ASCII8BIT)
		if err != nil {
			t.Fatalf("parse %q: %v", pat, err)
		}
		prog := compile.CompileEnc(res, compile.ASCII8BIT)
		dfa := forceDFA(prog)
		if dfa == nil {
			continue
		}
		for _, in := range []string{"", "abc", "café", "\xff\x80", "A\nB", "naïve"} {
			caps, ok, err := Match(prog, in, DefaultBudget)
			if err != nil {
				t.Fatalf("vm: %v", err)
			}
			wb, we, wok := -1, -1, false
			if ok {
				wb, we, wok = caps[0], caps[1], true
			}
			gb, ge, gok := dfa.Search(in, compile.ASCII8BIT, 0)
			if gok != wok || (gok && (gb != wb || ge != we)) {
				t.Errorf("binary %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
					pat, in, gb, ge, gok, wb, we, wok)
			}
		}
	}
}

// TestDFAGenWraparound forces the visited-stamp generation to wrap past its
// uint32 maximum so the O(1)-clear reset branch of bump runs.
func TestDFAGenWraparound(t *testing.T) {
	th := newDFAThreads(4)
	th.visited[0] = ^uint32(0) // a stale stamp at the max generation
	th.gen = ^uint32(0)
	th.bump() // gen++ overflows to 0, triggering the full reset to gen 1
	if th.gen != 1 {
		t.Errorf("after wraparound gen = %d, want 1", th.gen)
	}
	if th.visited[0] != 0 {
		t.Errorf("wraparound did not clear stale stamp: visited[0] = %d", th.visited[0])
	}
	// After the reset the stale stamp (formerly == max gen) no longer equals the live
	// generation, so a node it represented reads as not-yet-visited this generation.
	if th.visited[0] == th.gen {
		t.Error("a stale node must not read as visited in the live generation after a wraparound reset")
	}
}

// TestDFAEmptyStartClosure covers the executor branch where a seeded start thread
// produces no waiting thread at a position (an unsatisfiable leading assertion
// there) so the scan advances and re-seeds. \G off the scan origin yields exactly
// that at every position past 0.
func TestDFAEmptyStartClosure(t *testing.T) {
	prog := compileForDFA(t, `\Gabc`)
	dfa := forceDFA(prog)
	if dfa == nil {
		t.Fatal("expected DFA for \\Gabc")
	}
	// gpos 0: \G holds only at offset 0, so "xabc" cannot match (start 0 is 'x').
	if _, _, ok := dfa.Search("xabc", compile.UTF8, 0); ok {
		t.Error("\\Gabc should not match when the text does not start at the \\G origin")
	}
	// A hit anchored at the origin still works.
	if b, e, ok := dfa.Search("abcd", compile.UTF8, 0); !ok || b != 0 || e != 3 {
		t.Errorf("\\Gabc on abcd = (%d,%d,%v), want (0,3,true)", b, e, ok)
	}
}

// TestDFAMatchThenThreadsDie covers the executor's "a match is fixed once the
// higher-priority threads that outranked it have all died" path: a leftmost-first
// match is recorded while a still-live, higher-priority alternative keeps running,
// then that alternative dies, leaving the thread list empty with a match already
// set so the search returns the recorded (leftmost-first) span.
func TestDFAMatchThenThreadsDie(t *testing.T) {
	// a+c is tried before a+ (leftmost-first); on "aaab" the a+c branch consumes the
	// run then fails at 'b', while the a+ branch has already produced a match. The
	// engine must return a+'s span, agreeing with the backtracker.
	for _, pat := range []string{`a+c|a+`, `a+c|a+b|a+`, `(?:abc|a)x?`} {
		prog := compileForDFA(t, pat)
		dfa := forceDFA(prog)
		if dfa == nil {
			t.Fatalf("expected DFA for %q", pat)
		}
		for _, in := range []string{"aaab", "aaac", "aaa", "abx", "ab", "a"} {
			wb, we, wok := vmSpan(t, prog, in)
			gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
			if gok != wok || (gok && (gb != wb || ge != we)) {
				t.Errorf("pattern %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
					pat, in, gb, ge, gok, wb, we, wok)
			}
		}
	}
}

// newSimForTest builds a bare dfaSim over a compiled pattern so the per-step NFA
// simulation (dfaSim.search) can be driven directly. The production DFA.Search only
// reaches dfaSim.search through the cached driver's adaptive fallback gate, which
// fires on multi-byte / assertion-churn input; that path never seeds at a position
// the prefilter has already proven unviable, so a handful of the simulation's
// general seed / re-seed arms are unreachable from there. They are correct, proven
// (this engine is the search the cached driver borrows wholesale), defensive code,
// so they are pinned here by invoking the simulation directly on the inputs that
// exercise each arm.
func newSimForTest(t *testing.T, pat, input string, gpos int) *dfaSim {
	t.Helper()
	prog := compileForDFA(t, pat)
	nfa, ok := buildNFA(prog)
	if !ok {
		t.Fatalf("expected NFA for %q", pat)
	}
	pf := analyze(prog)
	return &dfaSim{
		nfa:   nfa,
		th:    newDFAThreads(len(nfa.insts)),
		ctx:   dfaCtx{input: input, enc: compile.UTF8, gpos: gpos},
		pf:    pf,
		usePF: pf.usable(),
	}
}

// TestDFASimSeedArms drives the per-step simulation's seed / re-seed / drain arms
// directly (see newSimForTest), each cross-checked against the backtracking VM.
func TestDFASimSeedArms(t *testing.T) {
	// seed(0) < 0: a usable literal-prefix prefilter proves no start is viable, so the
	// simulation returns no-match before the scan loop.
	if _, _, ok := newSimForTest(t, `abc`, "no a-b-c sequence here", 0).search(false); ok {
		t.Error("seed(0)<0: expected no match when the required literal prefix is absent")
	}
	// Match fixed, then the higher-priority threads that outranked it die, leaving the
	// thread list empty with a match already recorded so the loop top returns the
	// leftmost-first span.
	if b, e, ok := newSimForTest(t, `a+c|a+`, "aaab", 0).search(false); !ok || b != 0 || e != 3 {
		t.Errorf("match-then-drain: got (%d,%d,%v), want (0,3,true)", b, e, ok)
	}
	// Re-seed whose start closure is empty (an unsatisfiable leading \G off the scan
	// origin), so the cursor advances code point by code point and re-seeds.
	if _, _, ok := newSimForTest(t, `\Gabc`, "xabc", 0).search(false); ok {
		t.Error("empty-start-closure re-seed: \\Gabc must not match off the \\G origin")
	}
	// The same empty-start-closure walk, but with a no-prefilter pattern (the dot, so
	// usePF is false) whose \G never holds, so the walk runs the cursor up to
	// end-of-input and the loop breaks there.
	if _, _, ok := newSimForTest(t, `\G.`, "ab", 5).search(false); ok {
		t.Error("empty-start-closure-to-EOI: \\G. must not match when \\G never holds")
	}
}

// TestDFAMixedWidthFallback pins the multi-byte mixed-width regression: when two
// consuming threads alive at the same offset accept different byte widths — the
// rune-aware dot consuming a whole 2-byte code point while a freshly re-seeded
// byte-literal start consumes one byte (e.g. `γ.` on "γγ") — the cached-DFA driver's
// single-cursor model cannot land both successors at their own offset. It formerly
// collapsed them to one width and truncated the dot's match by a byte (0,3 instead
// of 0,4); the fix routes such fallback positions to the per-step simulation. This
// asserts the cached DFA agrees byte-for-byte with the backtracking VM (the oracle)
// for these patterns on every arch — it does not depend on a target-arch ruby, so it
// runs under qemu where the differential test is skipped.
func TestDFAMixedWidthFallback(t *testing.T) {
	cases := []struct{ pat, in string }{
		{`γ.`, "γγ"},           // bare multibyte-literal-then-dot
		{`γ.`, "γδ"},           // distinct following rune
		{`γ.|x`, "γγ"},         // alternation, multibyte arm first
		{`a|γ.`, "γδ"},         // alternation, multibyte arm second
		{`(?m)^x|γ.`, "γγ"},    // the reported pattern: multiline anchor × multibyte arm
		{`(?m)^x|γ.`, "γδ"},    //
		{`(?m)^x|γ.`, "x\nγγ"}, // anchored arm reachable after a newline
		{`α.|β`, "αβγ"},        // dot spans the following multibyte rune
		{`xγ.|y`, "xγγz"},      // single-byte literal then multibyte arm
		{`γ.δ.`, "γαδβ"},       // two multibyte-then-dot pairs in sequence
	}
	for _, c := range cases {
		prog := compileForDFA(t, c.pat)
		dfa := forceDFA(prog)
		if dfa == nil {
			t.Fatalf("pattern %q unexpectedly outside the DFA subset", c.pat)
		}
		wb, we, wok := vmSpan(t, prog, c.in)
		gb, ge, gok := dfa.Search(c.in, compile.UTF8, 0)
		if gok != wok || (gok && (gb != wb || ge != we)) {
			t.Errorf("pattern %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
				c.pat, c.in, gb, ge, gok, wb, we, wok)
		}
	}
}

func ExampleDFA() {
	res, _ := syntax.ParseEnc(`[a-z]+`, syntax.UTF8)
	prog := compile.CompileEnc(res, compile.UTF8)
	dfa := BuildDFA(prog)
	b, e, ok := dfa.Search("  hello  ", compile.UTF8, 0)
	fmt.Println(b, e, ok)
	// Output: 2 7 true
}
