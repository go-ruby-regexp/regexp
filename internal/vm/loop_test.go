package vm

import (
	"strings"
	"testing"

	"github.com/go-ruby-regexp/regexp/internal/compile"
)

// TestFusedLoopAtomKinds exercises every atom kind a fused OpLoop can repeat
// (OpChar, OpClass, OpAny, OpUniProp, OpFoldChar), so loopAtomStep's full
// dispatch is covered and each accepts exactly what its unfused atom would.
func TestFusedLoopAtomKinds(t *testing.T) {
	for _, tc := range []struct {
		pat, in    string
		begin, end int
		ok         bool
	}{
		{`a+`, "aaab", 0, 3, true},       // OpChar
		{`[a-c]+`, "abcd", 0, 3, true},   // OpClass
		{`.+`, "xyz", 0, 3, true},        // OpAny (greedy dot)
		{`\p{L}+`, "héllo!", 0, 6, true}, // OpUniProp (é is two bytes)
		{`(?i)a+`, "AaAaB", 0, 4, true},  // OpFoldChar (case-insensitive)
		{`\p{N}+`, "12ab", 0, 2, true},   // OpUniProp digits
		{`[^x]+`, "abxc", 0, 2, true},    // negated class loop
	} {
		b, e, ok := matchSpan(t, tc.pat, tc.in)
		if ok != tc.ok || (ok && (b != tc.begin || e != tc.end)) {
			t.Errorf("/%s/ on %q = (%d,%d,%v) want (%d,%d,%v)",
				tc.pat, tc.in, b, e, ok, tc.begin, tc.end, tc.ok)
		}
	}
}

// TestFusedLoopLazyGiveBack drives a lazy fused loop through every take-more
// step, including the exhaustion at the longest run (loopResume's lazy branch).
// a+? is lazy: it matches the fewest a's that let the rest succeed; b after a
// run of a's forces it to take all of them, and a non-matching tail forces it to
// exhaust the loop.
func TestFusedLoopLazyGiveBack(t *testing.T) {
	for _, tc := range []struct {
		pat, in    string
		begin, end int
		ok         bool
	}{
		{`a+?b`, "aaab", 0, 4, true},        // lazy must grow to consume all a's then b
		{`a+?c`, "aaab", -1, -1, false},     // no c: lazy take-more exhausts, no match
		{`a*?b`, "b", 0, 1, true},           // lazy star takes zero
		{`a{2,5}?b`, "aaaab", 0, 5, true},   // bounded lazy grows from 2 to 4
		{`a{2,5}?x`, "aaaa", -1, -1, false}, // bounded lazy exhausts at max
	} {
		b, e, ok := matchSpan(t, tc.pat, tc.in)
		if ok != tc.ok || (ok && (b != tc.begin || e != tc.end)) {
			t.Errorf("/%s/ on %q = (%d,%d,%v) want (%d,%d,%v)",
				tc.pat, tc.in, b, e, ok, tc.begin, tc.end, tc.ok)
		}
	}
}

// TestFusedLoopGreedyGiveBack drives a greedy fused loop through every give-back
// step down to its floor (loopResume's greedy branch and its Min cutoff).
func TestFusedLoopGreedyGiveBack(t *testing.T) {
	for _, tc := range []struct {
		pat, in    string
		begin, end int
		ok         bool
	}{
		{`a+a`, "aaaa", 0, 4, true},      // greedy gives back one a for the trailing a
		{`a{2,4}a`, "aaaaa", 0, 5, true}, // bounded greedy gives back to fit
		{`a{2,4}a`, "aa", -1, -1, false}, // cannot satisfy floor+1, exhausts to Min
	} {
		b, e, ok := matchSpan(t, tc.pat, tc.in)
		if ok != tc.ok || (ok && (b != tc.begin || e != tc.end)) {
			t.Errorf("/%s/ on %q = (%d,%d,%v) want (%d,%d,%v)",
				tc.pat, tc.in, b, e, ok, tc.begin, tc.end, tc.ok)
		}
	}
}

// TestFusedLoopBudget verifies a fused loop charges the deterministic step budget
// per atom scanned, so a long run cannot evade the ReDoS/time bound: a tiny
// budget against a long run trips ErrBudget rather than silently completing.
func TestFusedLoopBudget(t *testing.T) {
	long := strings.Repeat("a", 1000)
	if _, _, err := Match(build(t, `a+`), long, 8); err == nil {
		t.Fatal("a fused loop over a long run must exhaust a tiny budget")
	}
	// An ample budget completes.
	if _, ok, err := Match(build(t, `a+`), long, DefaultBudget); err != nil || !ok {
		t.Fatalf("a+ on a long run with ample budget: ok=%v err=%v", ok, err)
	}
}

// TestMemoGenSparseBacking exercises the sparse-map backing of the (pc, sp) memo,
// chosen when the (instruction count × input length) table would exceed the dense
// cap. It drives test/set/clearAll on the sparse path directly.
func TestMemoGenSparseBacking(t *testing.T) {
	var b memoGen
	// nPC * (inputLen+1) > maxDenseMemoCells forces the sparse map.
	b.init(1024, maxDenseMemoCells, true)
	if b.gen != nil || b.sparse == nil {
		t.Fatal("oversized table must use the sparse map backing")
	}
	if b.test(3, 7) {
		t.Fatal("unset (3,7) must not be marked")
	}
	b.set(3, 7)
	if !b.test(3, 7) {
		t.Fatal("set (3,7) must be marked")
	}
	b.clearAll()
	if b.test(3, 7) {
		t.Fatal("clearAll must forget the mark")
	}
	// Re-init reuses the existing sparse map rather than allocating a new one.
	b.set(5, 5)
	b.init(1024, maxDenseMemoCells, true)
	if b.gen != nil || b.sparse == nil {
		t.Fatal("re-init must keep the sparse backing")
	}
}

// TestMemoGenDenseReuse exercises the dense-backing reuse path: a second init
// whose table fits the already-allocated slice reuses it rather than reallocating.
func TestMemoGenDenseReuse(t *testing.T) {
	var b memoGen
	b.init(16, 16, true) // small dense table
	if b.gen == nil {
		t.Fatal("small table must use the dense backing")
	}
	first := &b.gen[0]
	b.init(8, 8, true) // smaller: cap(b.gen) >= n, reuse
	if &b.gen[0] != first {
		t.Fatal("a fitting re-init must reuse the dense slice")
	}
	// An inactive (no-split) program leaves the memo untouched and allocates nothing.
	var c memoGen
	c.init(10, 10, false)
	if c.active {
		t.Fatal("a split-free program must leave the memo inactive")
	}
	c.clearAll() // no-op on an inactive memo
}

// TestMemoGenBumpWraparound forces the generation counter to wrap to zero, on both
// the dense and sparse backings, so the reset-and-restart-at-1 path is covered. A
// stale stamp must never collide with the live generation after a wrap.
func TestMemoGenBumpWraparound(t *testing.T) {
	// Dense: mark a cell at a generation, then wrap. After wrap the old stamp must
	// no longer read as marked.
	var d memoGen
	d.init(4, 4, true)
	d.set(1, 1)
	// Drive the live stamp up to the wrap boundary, then bump: cur++ overflows to 0,
	// which triggers the reset (clear the slice) and restart at generation 1. The
	// stale mark written above (at the old high generation) must not survive the
	// reset, so it must read as unmarked under the fresh generation.
	d.cur = ^uint32(0)
	d.bump()
	if d.cur != 1 {
		t.Fatalf("dense wrap must restart at generation 1, got %d", d.cur)
	}
	if d.test(1, 1) {
		t.Fatal("dense wrap must clear the stale mark")
	}

	// Sparse: same, on the map backing.
	var s memoGen
	s.init(1024, maxDenseMemoCells, true)
	s.set(2, 2)
	s.cur = ^uint32(0)
	s.bump()
	if s.cur != 1 {
		t.Fatalf("sparse wrap must restart at generation 1, got %d", s.cur)
	}
	if s.test(2, 2) {
		t.Fatal("sparse wrap must clear the stale mark")
	}
}

// TestLoopAtomStepUnknownSub covers loopAtomStep's defensive default arm: a
// loop whose Sub is not one of the five consuming-atom opcodes the compiler ever
// emits (only reachable via a malformed instruction) matches nothing. This can
// never arise from the compiler, so it is exercised directly.
func TestLoopAtomStepUnknownSub(t *testing.T) {
	m := &machine{input: "aaa"}
	// OpMatch is not a consuming atom, so the default arm returns (false, 0).
	if ok, w := m.loopAtomStep(compile.Inst{Sub: compile.OpMatch}, 0); ok || w != 0 {
		t.Fatalf("unknown Sub must match nothing, got (%v,%d)", ok, w)
	}
}

// TestFusedLoopInLookbehind covers a fused loop inside a lookbehind body (the
// execLook OpLoop arm), including the success and failure outcomes.
func TestFusedLoopInLookbehind(t *testing.T) {
	if !matchString(t, `(?<=a{3})b`, "aaab") {
		t.Fatal(`(?<=a{3})b should match "aaab"`)
	}
	if matchString(t, `(?<=a{3})b`, "aab") {
		t.Fatal(`(?<=a{3})b should not match "aab" (only two a's)`)
	}
}
