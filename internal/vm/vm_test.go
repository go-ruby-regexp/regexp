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

func TestLeftmostSearch(t *testing.T) {
	// Match must find the leftmost start, even when a later start would also
	// match.
	b, e, ok := matchSpan(t, "ab", "xxabxxab")
	if !ok || b != 2 || e != 4 {
		t.Fatalf("leftmost = (%d,%d,%v)", b, e, ok)
	}
}
