package vm

import (
	"errors"
	"testing"

	"github.com/go-onigmo/regexp/internal/ast"
	"github.com/go-onigmo/regexp/internal/compile"
	"github.com/go-onigmo/regexp/internal/syntax"
)

// build parses, compiles, and returns a program for testing.
func build(t *testing.T, pat string) *compile.Program {
	t.Helper()
	r, err := syntax.Parse(pat)
	if err != nil {
		t.Fatalf("Parse(%q): %v", pat, err)
	}
	return compile.Compile(r)
}

// matchSpan runs a default-budget match and returns the whole-match span and
// whether it matched.
func matchSpan(t *testing.T, pat, input string) (int, int, bool) {
	t.Helper()
	caps, ok, err := Match(build(t, pat), input, DefaultBudget)
	if err != nil {
		t.Fatalf("Match(%q,%q): %v", pat, input, err)
	}
	if !ok {
		return -1, -1, false
	}
	return caps[0], caps[1], true
}

// matchString reports whether pat matches anywhere in input.
func matchString(t *testing.T, pat, input string) bool {
	t.Helper()
	_, ok, err := Match(build(t, pat), input, DefaultBudget)
	if err != nil {
		t.Fatalf("Match(%q,%q): %v", pat, input, err)
	}
	return ok
}

func TestMatchBasics(t *testing.T) {
	for _, tc := range []struct {
		pat, in    string
		begin, end int
		ok         bool
	}{
		{"abc", "xxabcxx", 2, 5, true},
		{"a*", "aaa", 0, 3, true},
		{"a+", "baa", 1, 3, true},
		{"a?", "b", 0, 0, true},
		{".", "x", 0, 1, true},
		{".", "\n", -1, -1, false},
		{"[a-c]+", "xbcay", 1, 4, true},
		{"[^a-c]+", "abXYc", 2, 4, true},
		{"a|ab", "ab", 0, 1, true},
		{"xyz", "abc", -1, -1, false},
	} {
		b, e, ok := matchSpan(t, tc.pat, tc.in)
		if ok != tc.ok || b != tc.begin || e != tc.end {
			t.Errorf("/%s/ on %q = (%d,%d,%v) want (%d,%d,%v)",
				tc.pat, tc.in, b, e, ok, tc.begin, tc.end, tc.ok)
		}
	}
}

func TestMatchAnchors(t *testing.T) {
	for _, tc := range []struct {
		pat, in string
		ok      bool
	}{
		{`\Aabc`, "abcd", true},
		{`\Aabc`, "xabc", false},
		{`xyz\z`, "wxyz", true},
		{`xyz\z`, "wxyzz", false},
		{`xyz\Z`, "wxyz\n", true},
		{`xyz\Z`, "wxyz", true},
		{`xyz\Z`, "wxyz\n\n", false},
		{`^abc`, "x\nabc", true},
		{`^abc`, "xabc", false},
		{`abc$`, "abc\nx", true},
		{`abc$`, "abc", true},
		{`abc$`, "abcd", false},
	} {
		_, _, ok := matchSpan(t, tc.pat, tc.in)
		if ok != tc.ok {
			t.Errorf("/%s/ on %q matched=%v want %v", tc.pat, tc.in, ok, tc.ok)
		}
	}
}

func TestMatchCaptures(t *testing.T) {
	caps, ok, err := Match(build(t, `(\d+)-(\d+)`), "v2026-06", DefaultBudget)
	if err != nil || !ok {
		t.Fatalf("match failed: ok=%v err=%v", ok, err)
	}
	// group 0 = "2026-06" at [1,8), group 1 = "2026" [1,5), group 2 = "06" [6,8)
	want := []int{1, 8, 1, 5, 6, 8}
	for i, w := range want {
		if caps[i] != w {
			t.Errorf("caps[%d] = %d want %d (all %v)", i, caps[i], w, caps)
		}
	}
}

func TestMatchEmptyStar(t *testing.T) {
	// (a*)* must not loop forever on an empty body; the visited guard handles
	// it. The match should be the empty string at position 0.
	b, e, ok := matchSpan(t, "(a*)*", "")
	if !ok || b != 0 || e != 0 {
		t.Fatalf("(a*)* on \"\" = (%d,%d,%v)", b, e, ok)
	}
}

func TestMatchEmptyStarNonEmpty(t *testing.T) {
	// (a*)* on "aaa" should consume everything without spinning.
	b, e, ok := matchSpan(t, "(a*)*", "aaa")
	if !ok || b != 0 || e != 3 {
		t.Fatalf("(a*)* on \"aaa\" = (%d,%d,%v)", b, e, ok)
	}
}

func TestMatchEmptyPattern(t *testing.T) {
	b, e, ok := matchSpan(t, "", "hi")
	if !ok || b != 0 || e != 0 {
		t.Fatalf("empty pattern = (%d,%d,%v)", b, e, ok)
	}
}

func TestBudgetExceeded(t *testing.T) {
	// A tiny budget forces ErrBudget on any non-trivial search.
	_, ok, err := Match(build(t, "a+b"), "aaaaaaaaaa", 3)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("expected ErrBudget, got ok=%v err=%v", ok, err)
	}
}

func TestBudgetExceededOnLaterStart(t *testing.T) {
	// Budget is shared across start positions; this forces exhaustion after the
	// first start position fails.
	prog := build(t, "z")
	_, ok, err := Match(prog, "aaaa", 2)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("expected ErrBudget across starts, got ok=%v err=%v", ok, err)
	}
}

func TestClassMatchNegate(t *testing.T) {
	in := compile.Inst{
		Op:     compile.OpClass,
		Ranges: []ast.ClassRange{{Lo: 'a', Hi: 'z'}},
		Negate: true,
	}
	if classMatch(in, 'a') {
		t.Error("negated [a-z] must reject 'a'")
	}
	if !classMatch(in, '0') {
		t.Error("negated [a-z] must accept '0'")
	}
}

func TestMatchLookaround(t *testing.T) {
	for _, tc := range []struct {
		pat, in    string
		begin, end int
		ok         bool
	}{
		// Positive / negative lookahead.
		{`foo(?=bar)`, "foobar", 0, 3, true},
		{`foo(?=bar)`, "foobaz", -1, -1, false},
		{`foo(?!bar)`, "foobaz", 0, 3, true},
		{`foo(?!bar)`, "foobar", -1, -1, false},
		// Positive / negative lookbehind.
		{`(?<=foo)bar`, "foobar", 3, 6, true},
		{`(?<=foo)bar`, "xxxbar", -1, -1, false},
		{`(?<!foo)bar`, "xxxbar", 3, 6, true},
		{`(?<!foo)bar`, "foobar", -1, -1, false},
		// Lookbehind with alternation of differing widths (widest first).
		{`(?<=ab|c)d`, "cd", 1, 2, true},
		{`(?<=ab|c)d`, "abd", 2, 3, true},
		{`(?<=ab|c)d`, "zd", -1, -1, false},
		// Fixed-width lookbehind with a class/dot body.
		{`(?<=a.c)d`, "abcd", 3, 4, true},
		// Captures inside positive lookahead are visible afterwards.
		{`(?=(b))b`, "ab", 1, 2, true},
		// Nested lookaround.
		{`a(?=b(?=c))`, "abc", 0, 1, true},
		{`a(?=b(?=c))`, "abd", -1, -1, false},
		// Lookbehind at the very start cannot match (would need bytes < 0).
		{`(?<=ab)c`, "c", -1, -1, false},
	} {
		b, e, ok := matchSpan(t, tc.pat, tc.in)
		if ok != tc.ok || b != tc.begin || e != tc.end {
			t.Errorf("/%s/ on %q = (%d,%d,%v) want (%d,%d,%v)",
				tc.pat, tc.in, b, e, ok, tc.begin, tc.end, tc.ok)
		}
	}
}

func TestMatchLookaheadCapture(t *testing.T) {
	// The capture made inside a positive lookahead must survive to the result.
	caps, ok, err := Match(build(t, `(?=(\d+))\d`), "x42y", DefaultBudget)
	if err != nil || !ok {
		t.Fatalf("match failed: ok=%v err=%v", ok, err)
	}
	// group 0 = "4" at [1,2); group 1 (inside lookahead) = "42" at [1,3).
	if caps[2] != 1 || caps[3] != 3 {
		t.Fatalf("lookahead capture = [%d,%d] want [1,3] (all %v)", caps[2], caps[3], caps)
	}
}

func TestMatchPrevMatchAnchor(t *testing.T) {
	// \G anchors to the scan origin (position 0) for a single Match.
	for _, tc := range []struct {
		pat, in string
		ok      bool
	}{
		{`\Gabc`, "abc", true},
		{`\Gabc`, "xabc", false},
		{`\G\d+`, "123", true},
	} {
		_, _, ok := matchSpan(t, tc.pat, tc.in)
		if ok != tc.ok {
			t.Errorf("/%s/ on %q matched=%v want %v", tc.pat, tc.in, ok, tc.ok)
		}
	}
}

func TestMatchPrevMatchInsideLook(t *testing.T) {
	// \G evaluated inside a lookaround sub-program (exercises OpAssertPrevMatch
	// in execLook).
	if !matchString(t, `(?=\Gabc)abc`, "abc") {
		t.Fatal(`(?=\Gabc)abc should match "abc"`)
	}
	if matchString(t, `(?=\Gabc)abc`, "xabc") {
		t.Fatal(`(?=\Gabc)abc should not match "xabc"`)
	}
}

func TestMatchBackrefInsideLook(t *testing.T) {
	// A backreference inside a lookahead body, including the unset-group path.
	if !matchString(t, `(a)(?=\1)a`, "aa") {
		t.Fatal(`(a)(?=\1)a should match "aa"`)
	}
	if matchString(t, `(a)(?=\1)a`, "ab") {
		t.Fatal(`(a)(?=\1)a should not match "ab"`)
	}
}

func TestMatchEmptyLoopInsideLook(t *testing.T) {
	// An empty-matching star inside a lookahead must not spin (exercises the
	// visited guard's "already explored" branch in execLook).
	if !matchString(t, `(?=(a*)*b)`, "aaab") {
		t.Fatal(`(?=(a*)*b) should match "aaab"`)
	}
}

func TestBudgetExceededInsideLook(t *testing.T) {
	// A tiny budget is exhausted while running a lookaround sub-program.
	_, _, err := Match(build(t, `(?=a+b)`), "aaaaaaaa", 5)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("expected ErrBudget inside look, got %v", err)
	}
}

func TestBudgetExceededInsideNestedLook(t *testing.T) {
	// The budget is exhausted inside a lookaround nested within another, so the
	// error must propagate out of the inner execLook (OpLook handler) too.
	_, _, err := Match(build(t, `(?=a(?=a+b))`), "aaaaaaaa", 6)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("expected ErrBudget inside nested look, got %v", err)
	}
}

func TestBackrefUnsetGroupInsideLook(t *testing.T) {
	// (z)? does not participate, so \1 inside the lookahead is the empty string,
	// which always succeeds (exercises the unset-group branch in execLook).
	if !matchString(t, `(z)?(?=\1)a`, "a") {
		t.Fatal(`(z)?(?=\1)a should match "a"`)
	}
}

func TestLookBodyExercisesEveryOpcode(t *testing.T) {
	// Each pattern routes a different opcode through the lookaround executor.
	for _, tc := range []struct {
		pat, in string
		ok      bool
	}{
		{`a(?=.)`, "ab", true},               // OpAny inside look
		{`a(?=.)`, "a\n", false},             // OpAny rejects newline
		{`a(?=[0-9])`, "a5", true},           // OpClass inside look
		{`a(?=b|cc)`, "acc", true},           // OpSplit + OpJmp inside look
		{`a(?=b|cc)`, "axx", false},          // both alternatives fail
		{`a(?=\Ab)`, "ab", false},            // OpAssertBeginText (sp != 0) fails
		{`(?=\Aab)ab`, "ab", true},           // OpAssertBeginText succeeds
		{`a(?=b\z)`, "ab", true},             // OpAssertEndText inside look
		{`a(?=b\z)`, "abc", false},           // not end of text
		{`a(?=b\Z)`, "ab\n", true},           // OpAssertEndTextOptNL inside look
		{`a(?=b$)`, "ab\nc", true},           // OpAssertEndLine inside look
		{`\n(?=^b)`, "\nb", true},            // OpAssertBeginLine inside look
		{`a(?=(b)*c)`, "abbbc", true},        // OpSave + greedy loop inside look
		{`a(?=(b*)*c)`, "abbc", true},        // empty-loop guard inside look
		{`(?<=ab)(?<=.b)c`, "abc", true},     // stacked lookbehind, dot width
		{`(?<=abc)d`, "ab", false},           // lookbehind start clamped (sp-w<0)
		{`(?<=ab)x`, "abc", false},           // lookbehind ok but no following x
	} {
		if got := matchString(t, tc.pat, tc.in); got != tc.ok {
			t.Errorf("/%s/ on %q matched=%v want %v", tc.pat, tc.in, got, tc.ok)
		}
	}
}

func TestLookbehindEndMustAlign(t *testing.T) {
	// A fixed-width lookbehind whose body could match a shorter span must still
	// be anchored to end exactly at the current position. (?<=a.) requires two
	// bytes ending right before 'c'.
	if !matchString(t, `(?<=a.)c`, "abc") {
		t.Fatal(`(?<=a.)c should match "abc"`)
	}
	if matchString(t, `(?<=a.)c`, "ac") {
		// only one byte precedes 'c', so the two-wide lookbehind cannot align.
		t.Fatal(`(?<=a.)c should not match "ac"`)
	}
}

func TestBudgetExceededInsideLookbehind(t *testing.T) {
	// Exhaust the budget while a lookbehind sub-program runs. Several budgets
	// straddle the point where the failure occurs inside the candidate-position
	// loop of look, so its error-propagation path is exercised.
	for _, b := range []int{6, 10, 12, 14, 16} {
		_, _, err := Match(build(t, `(?<=a{4})b`), "aaaab", b)
		if !errors.Is(err, ErrBudget) {
			t.Fatalf("budget=%d: expected ErrBudget inside lookbehind, got %v", b, err)
		}
	}
}

func TestLeftmostSearch(t *testing.T) {
	// Match must find the leftmost start, even when a later start would also
	// match.
	b, e, ok := matchSpan(t, "ab", "xxabxxab")
	if !ok || b != 2 || e != 4 {
		t.Fatalf("leftmost = (%d,%d,%v)", b, e, ok)
	}
}

func TestMatchFoldChar(t *testing.T) {
	// (?i) folds ASCII letters in both directions; non-letters are unaffected.
	for _, tc := range []struct {
		pat, in string
		want    bool
	}{
		{`(?i)abc`, "ABC", true},
		{`(?i)ABC`, "abc", true},
		{`(?i)aBc`, "AbC", true},
		{`(?i)a5b`, "A5B", true},  // digit between folded letters
		{`(?i)a5b`, "A6B", false}, // non-letter must still match exactly
		{`(?i)abc`, "abd", false},
		{`abc`, "ABC", false}, // no /i: case-sensitive
	} {
		if got := matchString(t, tc.pat, tc.in); got != tc.want {
			t.Errorf("/%s/ on %q: got %v want %v", tc.pat, tc.in, got, tc.want)
		}
	}
}

func TestMatchFoldClass(t *testing.T) {
	for _, tc := range []struct {
		pat, in string
		want    bool
	}{
		{`(?i)[a-z]`, "A", true},       // swap finds it
		{`(?i)[A-Z]`, "a", true},       // swap finds it (other direction)
		{`(?i)[a-z]`, "a", true},       // already in set (no swap needed)
		{`(?i)[a-z]`, "5", false},      // digit folds to itself, not in set
		{`(?i)[^a-z]`, "A", false},     // negated: A folds into [a-z] => excluded
		{`(?i)[^a-z]`, "5", true},      // negated: digit stays out => included
		{`(?i)[m-p]`, "M", true},
		{`(?i)[m-p]`, "Q", false},
	} {
		if got := matchString(t, tc.pat, tc.in); got != tc.want {
			t.Errorf("/%s/ on %q: got %v want %v", tc.pat, tc.in, got, tc.want)
		}
	}
}

func TestMatchFoldBackref(t *testing.T) {
	// A backreference under /i compares case-insensitively.
	for _, tc := range []struct {
		pat, in string
		want    bool
	}{
		{`(?i)(ab)\1`, "AbAB", true},
		{`(?i)(ab)\1`, "Abac", false}, // second copy differs in a real letter
		{`(?i)(ab)\1`, "Aba", false},  // too short to hold the backref
		{`(ab)(?i)\1`, "abAB", true},  // capture unfolded, backref folded
		{`(ab)\1`, "abAB", false},     // no /i anywhere: case-sensitive
	} {
		if got := matchString(t, tc.pat, tc.in); got != tc.want {
			t.Errorf("/%s/ on %q: got %v want %v", tc.pat, tc.in, got, tc.want)
		}
	}
}

func TestFoldInsideLookaround(t *testing.T) {
	// Exercise the rune-aware fold paths inside execLook (the lookaround sub-VM).
	// Only lookahead is tested: a folded atom is rune-aware and so of variable byte
	// width, which a fixed-width lookbehind rejects at parse time (see
	// TestFoldLookbehindRejected) — the same boundary \p{…} draws.
	for _, tc := range []struct {
		pat, in string
		want    bool
	}{
		{`x(?=(?i)abc)`, "xABC", true},   // OpFoldChar in lookahead
		{`x(?=(?i)[a-z])`, "xA", true},   // OpClass fold in lookahead
		{`(?i)(ab)(?=\1)`, "abAB", true}, // OpBackref fold in lookahead
	} {
		if got := matchString(t, tc.pat, tc.in); got != tc.want {
			t.Errorf("/%s/ on %q: got %v want %v", tc.pat, tc.in, got, tc.want)
		}
	}
}

// TestFoldLookbehindRejected confirms that a case-insensitive atom — being
// rune-aware and thus of variable byte width — is refused inside a fixed-width
// lookbehind, the same boundary the engine draws for \p{…}.
func TestFoldLookbehindRejected(t *testing.T) {
	for _, pat := range []string{`(?<=(?i)abc)x`, `(?<=(?i)[a-z])x`, `(?<=(?i)é)x`} {
		if _, err := syntax.Parse(pat); err == nil {
			t.Errorf("Parse(%q): expected a variable-width-lookbehind error", pat)
		}
	}
}
