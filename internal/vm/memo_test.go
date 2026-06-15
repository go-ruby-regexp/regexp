package vm

import (
	"strings"
	"testing"
)

// repeat builds a string of n copies of s (test helper; the engine is the unit
// under test, not this).
func repeat(s string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(s)
	}
	return b.String()
}

// TestMemoDefusesCatastrophicBacktracking checks that the canonical
// exponential-backtracking patterns terminate within a modest step budget once
// (pc, sp) memoization is on. Without it, /(a+)+$/ on a non-matching tail would
// need on the order of 2^n steps; with it the work is polynomial. Expectations
// are hardcoded (oracle-independent) and match MRI Onigmo.
func TestMemoDefusesCatastrophicBacktracking(t *testing.T) {
	const n = 40
	long := repeat("a", n) + "!" // never reaches end-of-line after the '!'
	allA := repeat("a", n)       // the empty-body and dot cases do match here

	// A budget far below 2^40 but ample for polynomial memoized work.
	const budget = 2_000_000
	for _, tc := range []struct {
		pat, in    string
		begin, end int
		ok         bool
	}{
		{`(a+)+$`, long, -1, -1, false},   // classic ReDoS, no match
		{`(a|aa)+$`, long, -1, -1, false}, // overlapping alternatives, no match
		{`(a*)*$`, long, n + 1, n + 1, true}, // empty body matches at the boundary
		{`(.*)*$`, long, 0, n + 1, true},     // dot-star nest spans the whole input
		{`(a+)+b`, allA, -1, -1, false},      // greedy nest then a required 'b'
	} {
		caps, ok, err := Match(build(t, tc.pat), tc.in, budget)
		if err != nil {
			t.Fatalf("/%s/ on %q: budget exhausted, memoization not effective: %v",
				tc.pat, tc.in, err)
		}
		if ok != tc.ok {
			t.Fatalf("/%s/ on %q: ok=%v want %v", tc.pat, tc.in, ok, tc.ok)
		}
		if ok && (caps[0] != tc.begin || caps[1] != tc.end) {
			t.Fatalf("/%s/ on %q: span=[%d,%d] want [%d,%d]",
				tc.pat, tc.in, caps[0], caps[1], tc.begin, tc.end)
		}
	}
}

// TestMemoPreservesGreedySemantics verifies the memo never changes the match a
// pattern produces — it only prunes redundant re-exploration. These small cases
// have a single correct leftmost-first answer regardless of memoization.
func TestMemoPreservesGreedySemantics(t *testing.T) {
	for _, tc := range []struct {
		pat, in    string
		begin, end int
	}{
		{`(a*)*b`, "aaab", 0, 4},
		{`(a|aa)+b`, "aaaab", 0, 5},
		{`(a+)(a+)`, "aaaa", 0, 4},
		{`a.*b.*c`, "axbxbxcxc", 0, 9},
	} {
		b, e, ok := matchSpan(t, tc.pat, tc.in)
		if !ok || b != tc.begin || e != tc.end {
			t.Errorf("/%s/ on %q = (%d,%d,%v) want (%d,%d,true)",
				tc.pat, tc.in, b, e, ok, tc.begin, tc.end)
		}
	}
}

// TestBackrefDisablesMemo exercises the memoize=false path: a program with a
// backreference keeps the per-advance reset of the (pc, sp) set (so the memo's
// soundness condition holds), while still matching correctly. The empty-loop
// guard must still keep an empty-body star from spinning even with memoization
// off.
func TestBackrefDisablesMemo(t *testing.T) {
	for _, tc := range []struct {
		pat, in string
		want    bool
	}{
		{`(a*)*\1`, "aaa", true}, // empty-loop guard with a backref, memo off
		{`(ab)+\1`, "abab", true},
		{`(ab)+\1`, "ababab", true},
		{`(a+)\1`, "aaaa", true},
		{`(a+)\1b`, "aaaa", false}, // no trailing b: must fail, not spin
	} {
		if got := matchString(t, tc.pat, tc.in); got != tc.want {
			t.Errorf("/%s/ on %q: got %v want %v", tc.pat, tc.in, got, tc.want)
		}
	}
}
