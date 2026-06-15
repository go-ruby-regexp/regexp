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
	// Alternation (leading split) must be unusable.
	if analyze(build(t, "a|b")).usable() {
		t.Fatal("alternation-led pattern must be unusable")
	}
	// A leading optional (split) must be unusable.
	if analyze(build(t, "a*b")).usable() {
		t.Fatal("leading-optional pattern must be unusable")
	}
	// A leading dot before a literal must be unusable (the dot ends analysis).
	if analyze(build(t, ".abc")).usable() {
		t.Fatal("leading-dot pattern must be unusable")
	}
	// A leading lookahead must be unusable.
	if analyze(build(t, "(?=a)b")).usable() {
		t.Fatal("lookahead-led pattern must be unusable")
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

// BenchmarkNoPrefilterMiss is the baseline: a leading dot defeats the prefilter,
// so the VM runs at every start position. It bounds the speedup the prefilter
// buys on the same haystack.
func BenchmarkNoPrefilterMiss(b *testing.B) {
	prog := mustBuild(b, ".needle")
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
