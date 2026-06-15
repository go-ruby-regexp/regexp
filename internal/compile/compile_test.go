package compile

import (
	"testing"

	"github.com/go-onigmo/regexp/internal/syntax"
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

func TestCompileStarLoop(t *testing.T) {
	p := compilePattern(t, "a*")
	// Expect: Save0, Split, Char, Jmp, Save1, Match.
	if opCount(p, OpSplit) != 1 || opCount(p, OpJmp) != 1 {
		t.Fatalf("a* should have one Split and one Jmp: %+v", p.Insts)
	}
}

func TestCompilePlus(t *testing.T) {
	p := compilePattern(t, "a+")
	// One required Char plus the loop.
	if opCount(p, OpChar) != 2 {
		t.Fatalf("a+ should emit two Char (required + loop body): %+v", p.Insts)
	}
}

func TestCompileBoundedRepeat(t *testing.T) {
	p := compilePattern(t, "a{2,4}")
	// Two required + two optional copies = four Char, two Split, no Jmp.
	if opCount(p, OpChar) != 4 {
		t.Fatalf("a{2,4} should emit four Char: %+v", p.Insts)
	}
	if opCount(p, OpSplit) != 2 {
		t.Fatalf("a{2,4} should emit two Split: %+v", p.Insts)
	}
	if opCount(p, OpJmp) != 0 {
		t.Fatalf("a{2,4} should emit no Jmp: %+v", p.Insts)
	}
}

func TestCompileExactRepeat(t *testing.T) {
	p := compilePattern(t, "a{3}")
	if opCount(p, OpChar) != 3 {
		t.Fatalf("a{3} should emit three Char: %+v", p.Insts)
	}
	if opCount(p, OpSplit) != 0 {
		t.Fatalf("a{3} should emit no Split: %+v", p.Insts)
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
	// A greedy a* prefers the body: split.X is the body (the next pc), split.Y the
	// exit. A lazy a*? prefers the exit: the branches are swapped.
	greedy := compilePattern(t, "a*")
	lazy := compilePattern(t, "a*?")
	var gs, ls *Inst
	for i := range greedy.Insts {
		if greedy.Insts[i].Op == OpSplit {
			gs = &greedy.Insts[i]
		}
	}
	for i := range lazy.Insts {
		if lazy.Insts[i].Op == OpSplit {
			ls = &lazy.Insts[i]
		}
	}
	if gs == nil || ls == nil {
		t.Fatal("expected one OpSplit in each of a* and a*?")
	}
	// In a*, the split sits just before the body, so its preferred X is split pc+1.
	for i := range greedy.Insts {
		if &greedy.Insts[i] == gs {
			if gs.X != i+1 {
				t.Errorf("greedy a*: split.X = %d want body at %d", gs.X, i+1)
			}
			// Lazy must have the branches swapped: its Y is the body (pc+1), X the exit.
			if ls.Y != i+1 {
				t.Errorf("lazy a*?: split.Y = %d want body at %d", ls.Y, i+1)
			}
			if ls.X == ls.Y {
				t.Errorf("lazy a*?: split branches not distinct: %#v", ls)
			}
		}
	}
}

func TestCompileLazyBoundedSwapsSplit(t *testing.T) {
	// a{0,2}? emits two optional splits, each preferring the exit (X) over the
	// body (Y). Confirm at least one split has X pointing past its body.
	p := compilePattern(t, "a{0,2}?")
	if opCount(p, OpSplit) != 2 {
		t.Fatalf("a{0,2}? should emit two splits: %+v", p.Insts)
	}
	for i := range p.Insts {
		if p.Insts[i].Op == OpSplit {
			// Lazy: the body (an OpChar) is the give-back Y branch.
			if p.Insts[p.Insts[i].Y].Op != OpChar {
				t.Errorf("lazy bounded split %d: Y should reach the body OpChar, got %#v", i, p.Insts[p.Insts[i].Y])
			}
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
