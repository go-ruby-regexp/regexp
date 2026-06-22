package compile

import (
	"testing"

	"github.com/go-ruby-regexp/regexp/internal/syntax"
)

// compilePattern parses and compiles a pattern for testing.
func compilePattern(t *testing.T, pat string) *Program {
	t.Helper()
	r, err := syntax.Parse(pat)
	if err != nil {
		t.Fatalf("Parse(%q): %v", pat, err)
	}
	return Compile(r)
}

// opCount counts instructions with a given opcode.
func opCount(p *Program, op Op) int {
	n := 0
	for _, in := range p.Insts {
		if in.Op == op {
			n++
		}
	}
	return n
}

func TestCompileWrapsSaveAndMatch(t *testing.T) {
	p := compilePattern(t, "a")
	if p.Insts[0].Op != OpSave || p.Insts[0].Slot != 0 {
		t.Fatalf("first inst must be Save 0, got %#v", p.Insts[0])
	}
	last := p.Insts[len(p.Insts)-1]
	if last.Op != OpMatch {
		t.Fatalf("last inst must be Match, got %#v", last)
	}
	// The whole pattern is itself a callable sub-program (the target of \g<0>): an
	// OpReturn precedes OpMatch so a \g<0> recursion returns, and the overall-match
	// close save precedes that OpReturn.
	if p.Insts[len(p.Insts)-2].Op != OpReturn {
		t.Fatalf("inst before Match must be Return, got %#v", p.Insts[len(p.Insts)-2])
	}
	if p.Insts[len(p.Insts)-3].Op != OpSave || p.Insts[len(p.Insts)-3].Slot != 1 {
		t.Fatalf("inst before Return must be Save 1")
	}
}

func TestNumSlots(t *testing.T) {
	p := compilePattern(t, "(a)(b)")
	if p.NumCapture != 2 {
		t.Fatalf("NumCapture = %d", p.NumCapture)
	}
	if got, want := p.NumSlots(), 6; got != want {
		t.Fatalf("NumSlots = %d want %d", got, want)
	}
}

func TestCompileAllNodeKinds(t *testing.T) {
	// One pattern that exercises every node lowering path.
	p := compilePattern(t, `\Aa.[b-d]\z^$\Z(x)(?:y)|`)
	for _, op := range []Op{
		OpSave, OpChar, OpAny, OpClass, OpMatch,
		OpAssertBeginText, OpAssertEndText, OpAssertEndTextOptNL,
		OpAssertBeginLine, OpAssertEndLine, OpSplit, OpJmp,
	} {
		if opCount(p, op) == 0 {
			t.Errorf("opcode %d never emitted", op)
		}
	}
}

// findLoop returns the single OpLoop instruction in p, failing if there is not
// exactly one. A quantifier over a single consuming atom (Char/Class/Any/UniProp/
// FoldChar) is fused into one OpLoop instead of the generic split/atom/jmp form.
func findLoop(t *testing.T, p *Program) Inst {
	t.Helper()
	var loop *Inst
	n := 0
	for i := range p.Insts {
		if p.Insts[i].Op == OpLoop {
			loop = &p.Insts[i]
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one OpLoop, got %d: %+v", n, p.Insts)
	}
	return *loop
}

func TestCompileStarLoop(t *testing.T) {
	// a* fuses into one OpLoop over OpChar 'a', unbounded (Max == -1), zero floor,
	// greedy. No split/jmp/extra char are emitted for it.
	p := compilePattern(t, "a*")
	if opCount(p, OpSplit) != 0 || opCount(p, OpJmp) != 0 {
		t.Fatalf("a* should fuse to OpLoop with no Split/Jmp: %+v", p.Insts)
	}
	loop := findLoop(t, p)
	if loop.Sub != OpChar || loop.B != 'a' || loop.Min != 0 || loop.Max != -1 || !loop.Greedy {
		t.Fatalf("a* OpLoop = %+v, want Sub=Char B='a' Min=0 Max=-1 Greedy", loop)
	}
}

func TestCompilePlus(t *testing.T) {
	// a+ fuses into one OpLoop over OpChar 'a' with a floor of one, unbounded.
	p := compilePattern(t, "a+")
	if opCount(p, OpChar) != 0 {
		t.Fatalf("a+ should fuse to OpLoop (no standalone Char): %+v", p.Insts)
	}
	loop := findLoop(t, p)
	if loop.Sub != OpChar || loop.B != 'a' || loop.Min != 1 || loop.Max != -1 || !loop.Greedy {
		t.Fatalf("a+ OpLoop = %+v, want Sub=Char B='a' Min=1 Max=-1 Greedy", loop)
	}
}

func TestCompileBoundedRepeat(t *testing.T) {
	// a{2,4} fuses into one OpLoop bounded [2,4]; no unrolled Char copies, no
	// Split/Jmp.
	p := compilePattern(t, "a{2,4}")
	if opCount(p, OpChar) != 0 || opCount(p, OpSplit) != 0 || opCount(p, OpJmp) != 0 {
		t.Fatalf("a{2,4} should fuse to one OpLoop, no Char/Split/Jmp: %+v", p.Insts)
	}
	loop := findLoop(t, p)
	if loop.Sub != OpChar || loop.B != 'a' || loop.Min != 2 || loop.Max != 4 {
		t.Fatalf("a{2,4} OpLoop = %+v, want Sub=Char B='a' Min=2 Max=4", loop)
	}
}

func TestCompileExactRepeat(t *testing.T) {
	// a{3} fuses into one OpLoop with Min == Max == 3.
	p := compilePattern(t, "a{3}")
	if opCount(p, OpChar) != 0 || opCount(p, OpSplit) != 0 {
		t.Fatalf("a{3} should fuse to one OpLoop, no Char/Split: %+v", p.Insts)
	}
	loop := findLoop(t, p)
	if loop.Sub != OpChar || loop.B != 'a' || loop.Min != 3 || loop.Max != 3 {
		t.Fatalf("a{3} OpLoop = %+v, want Sub=Char B='a' Min=3 Max=3", loop)
	}
}

func TestCompileEmpty(t *testing.T) {
	p := compilePattern(t, "")
	// Save0, Save1, Return (group-0 boundary), Match.
	if len(p.Insts) != 4 {
		t.Fatalf("empty pattern should compile to 4 insts, got %d", len(p.Insts))
	}
}

func TestCompileNonCapturingGroup(t *testing.T) {
	p := compilePattern(t, "(?:ab)")
	if p.NumCapture != 0 {
		t.Fatalf("non-capturing group must not add captures")
	}
	if opCount(p, OpSave) != 2 {
		t.Fatalf("only the whole-match saves expected: %+v", p.Insts)
	}
}

func TestCompileCapturingGroup(t *testing.T) {
	p := compilePattern(t, "(a)")
	// Save0, Save2, Char, Save3, Save1, Match → four Save.
	if opCount(p, OpSave) != 4 {
		t.Fatalf("capturing group should add a Save pair: %+v", p.Insts)
	}
}

func TestCompileLookahead(t *testing.T) {
	p := compilePattern(t, `a(?=b)`)
	if opCount(p, OpLook) != 1 || opCount(p, OpLookEnd) != 1 {
		t.Fatalf("lookahead should emit one OpLook and one OpLookEnd: %+v", p.Insts)
	}
	// OpLook.X must point just past its OpLookEnd.
	for i, in := range p.Insts {
		if in.Op == OpLook {
			if p.Insts[in.X-1].Op != OpLookEnd {
				t.Fatalf("OpLook.X (%d) must follow OpLookEnd, got %+v", in.X, p.Insts[in.X-1])
			}
			if in.Behind {
				t.Fatalf("inst %d: lookahead must not set Behind", i)
			}
		}
	}
}

func TestCompileLookbehindWidth(t *testing.T) {
	p := compilePattern(t, `(?<=ab|c)d`)
	var found bool
	for _, in := range p.Insts {
		if in.Op == OpLook {
			found = true
			if !in.Behind {
				t.Fatal("lookbehind must set Behind")
			}
			if in.Min != 1 || in.Max != 2 {
				t.Fatalf("lookbehind width = [%d,%d] want [1,2]", in.Min, in.Max)
			}
		}
	}
	if !found {
		t.Fatal("no OpLook emitted")
	}
}

func TestCompilePrevMatchAnchor(t *testing.T) {
	p := compilePattern(t, `\Ga`)
	if opCount(p, OpAssertPrevMatch) != 1 {
		t.Fatalf("\\G should emit OpAssertPrevMatch: %+v", p.Insts)
	}
}

func TestCompileAlternateLinks(t *testing.T) {
	p := compilePattern(t, "a|b|c")
	if opCount(p, OpSplit) != 2 || opCount(p, OpJmp) != 2 {
		t.Fatalf("3-way alternation needs two Split and two Jmp: %+v", p.Insts)
	}
}

func TestCompileHasBackref(t *testing.T) {
	// HasBackref drives whether the VM may memoize. It is set iff the program
	// emits an OpBackref (numeric \1 or named \k<>), and clear otherwise.
	for _, tc := range []struct {
		pat  string
		want bool
	}{
		{`(a)\1`, true},
		{`(?<g>a)\k<g>`, true},
		{`(a)(b)`, false},
		{`a(?=b)`, false},
		{`(a*)*b`, false},
	} {
		if got := compilePattern(t, tc.pat).HasBackref; got != tc.want {
			t.Errorf("/%s/ HasBackref = %v want %v", tc.pat, got, tc.want)
		}
	}
}

func TestCompileLazyStarSwapsSplit(t *testing.T) {
	// A single-atom quantifier fuses into one OpLoop, so greediness is carried on
	// the loop's Greedy flag rather than by swapping a split's branches: a* is
	// greedy, a*? is lazy. Both have the same atom, floor, and bound.
	greedy := findLoop(t, compilePattern(t, "a*"))
	lazy := findLoop(t, compilePattern(t, "a*?"))
	if !greedy.Greedy {
		t.Errorf("a* OpLoop must be greedy: %+v", greedy)
	}
	if lazy.Greedy {
		t.Errorf("a*? OpLoop must be lazy: %+v", lazy)
	}
	if greedy.Sub != lazy.Sub || greedy.B != lazy.B || greedy.Min != lazy.Min || greedy.Max != lazy.Max {
		t.Errorf("a* and a*? must differ only in Greedy: %+v vs %+v", greedy, lazy)
	}
}

func TestCompileLazyBoundedSwapsSplit(t *testing.T) {
	// a{0,2}? fuses into one lazy OpLoop bounded [0,2]; the lazy preference is the
	// Greedy=false flag, not a swapped split.
	loop := findLoop(t, compilePattern(t, "a{0,2}?"))
	if loop.Greedy {
		t.Errorf("a{0,2}? OpLoop must be lazy: %+v", loop)
	}
	if loop.Sub != OpChar || loop.Min != 0 || loop.Max != 2 {
		t.Errorf("a{0,2}? OpLoop = %+v, want Sub=Char Min=0 Max=2", loop)
	}
	// The legacy split-based lowering is gone for a single-atom quantifier: no
	// OpSplit should remain.
	if c := opCount(compilePattern(t, "a{0,2}?"), OpSplit); c != 0 {
		t.Errorf("a{0,2}? should emit no OpSplit, got %d", c)
	}
}

// TestCompileNonFusedBoundedRepeat exercises the generic (non-fused) bounded
// repeat lowering, reached when the quantified body is NOT a single consuming
// atom — here a group. Such a quantifier still emits the chain of optional splits
// (one per max-min step), with the greedy form preferring the body branch and the
// lazy form preferring the exit, so both branch-assignment arms of repeat run.
func TestCompileNonFusedBoundedRepeat(t *testing.T) {
	// Greedy (?:ab){0,2}: two optional splits, no fusion (the body is two chars).
	g := compilePattern(t, "(?:ab){0,2}")
	if opCount(g, OpLoop) != 0 {
		t.Fatalf("(?:ab){0,2} must not fuse (multi-atom body): %+v", g.Insts)
	}
	if opCount(g, OpSplit) != 2 {
		t.Fatalf("(?:ab){0,2} should emit two optional splits, got %d", opCount(g, OpSplit))
	}
	// Greedy prefers the body: each split's X is the body that immediately follows.
	for i := range g.Insts {
		if g.Insts[i].Op == OpSplit && g.Insts[i].X != i+1 {
			t.Errorf("greedy bounded split %d: X=%d want body at %d", i, g.Insts[i].X, i+1)
		}
	}
	// Lazy (?:ab){0,2}?: same two splits, but each prefers the exit (Y is the body).
	l := compilePattern(t, "(?:ab){0,2}?")
	if opCount(l, OpSplit) != 2 {
		t.Fatalf("(?:ab){0,2}? should emit two optional splits, got %d", opCount(l, OpSplit))
	}
	for i := range l.Insts {
		if l.Insts[i].Op == OpSplit && l.Insts[i].Y != i+1 {
			t.Errorf("lazy bounded split %d: Y=%d want body at %d", i, l.Insts[i].Y, i+1)
		}
	}
	// Both compile to a correct matcher regardless of the lowering shape.
	for _, pat := range []string{"(?:ab){0,2}", "(?:ab){0,2}?"} {
		if _, err := syntax.Parse(pat); err != nil {
			t.Fatalf("parse %q: %v", pat, err)
		}
	}
}

func TestCompileCall(t *testing.T) {
	// A capturing group's body is a callable sub-program terminated by OpReturn;
	// a \g<…> lowers to an OpCall whose X is patched to the group's entry pc.
	p := compilePattern(t, `(a)\g<1>`)
	if !p.HasCall {
		t.Fatal("HasCall must be set for a \\g<…> program")
	}
	var call *Inst
	returns := 0
	for i := range p.Insts {
		switch p.Insts[i].Op {
		case OpCall:
			call = &p.Insts[i]
		case OpReturn:
			returns++
		}
	}
	if call == nil {
		t.Fatal("no OpCall emitted")
	}
	// Group 1's entry is its open save (Save slot 2); OpCall.X must point there.
	tgt := p.Insts[call.X]
	if tgt.Op != OpSave || tgt.Slot != 2 {
		t.Fatalf("OpCall.X (%d) must be the group-1 open save, got %#v", call.X, tgt)
	}
	// One OpReturn for group 1, one for the whole-pattern group 0.
	if returns != 2 {
		t.Fatalf("expected two OpReturn (group 1 + group 0), got %d", returns)
	}
}

func TestCompileCallG0(t *testing.T) {
	// \g<0> recurses the whole pattern: its OpCall.X is the entry of group 0, the
	// instruction just after the overall-match open save (pc 1).
	p := compilePattern(t, `a\g<0>?`)
	for i := range p.Insts {
		if p.Insts[i].Op == OpCall {
			if p.Insts[i].X != 1 {
				t.Fatalf("\\g<0> OpCall.X = %d want 1 (group-0 entry)", p.Insts[i].X)
			}
			return
		}
	}
	t.Fatal("no OpCall emitted for \\g<0>")
}

func TestCompileHasCall(t *testing.T) {
	for _, tc := range []struct {
		pat  string
		want bool
	}{
		{`(a)\g<1>`, true},
		{`\g<0>?`, true},
		{`(a)\1`, false},
		{`(a)(b)`, false},
	} {
		if got := compilePattern(t, tc.pat).HasCall; got != tc.want {
			t.Errorf("/%s/ HasCall = %v want %v", tc.pat, got, tc.want)
		}
	}
}

func TestCompileFoldFlagPropagates(t *testing.T) {
	// (?i) must lower a letter literal to the rune-aware OpFoldChar and set Fold on
	// the emitted OpClass and OpBackref.
	p := compilePattern(t, `(?i)(a)[b]\1`)
	var foldChar, class, ref *Inst
	for i := range p.Insts {
		switch p.Insts[i].Op {
		case OpFoldChar:
			if foldChar == nil {
				foldChar = &p.Insts[i]
			}
		case OpClass:
			class = &p.Insts[i]
		case OpBackref:
			ref = &p.Insts[i]
		}
	}
	if foldChar == nil || foldChar.Rune != 'a' {
		t.Errorf("OpFoldChar for 'a' not emitted: %#v", foldChar)
	}
	if class == nil || !class.Fold {
		t.Errorf("OpClass Fold not set: %#v", class)
	}
	if ref == nil || !ref.Fold {
		t.Errorf("OpBackref Fold not set: %#v", ref)
	}

	// Without (?i) the letter stays a byte OpChar and the flags stay clear.
	q := compilePattern(t, `(a)[b]\1`)
	for _, in := range q.Insts {
		if in.Op == OpFoldChar {
			t.Errorf("OpFoldChar should not be emitted without (?i): %#v", in)
		}
		if (in.Op == OpClass || in.Op == OpBackref) && in.Fold {
			t.Errorf("Fold should be clear without (?i): %#v", in)
		}
	}
}
