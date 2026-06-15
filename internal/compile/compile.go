// Package compile lowers a syntax AST into the flat instruction program that
// the backtracking VM executes.
package compile

import (
	"github.com/go-onigmo/regexp/internal/ast"
	"github.com/go-onigmo/regexp/internal/syntax"
)

// Encoding selects how the byte-oriented input-advancing atoms — the dot
// (OpAny) and a byte-oriented character class (OpClass without a \p{…} member,
// code-point range, or /i fold) — traverse the input. It is the same type the
// parser uses (it governs lookbehind byte-width validation there).
//
// In UTF8 mode (the default, matching Ruby's behaviour on a UTF-8 string) these
// atoms decode one full UTF-8 code point and advance by its byte length, so the
// dot matches a whole multi-byte character (`/./` on "é" consumes "é", as MRI
// does). In ASCII8BIT mode (Ruby's /n binary encoding) every atom advances a
// single byte, so the dot consumes one byte of a multi-byte sequence — the
// engine's original byte-oriented behaviour. The rune-aware atoms (OpFoldChar,
// OpUniProp, and a rune-aware OpClass) always decode a code point in UTF8 mode
// and are forced to a single byte in ASCII8BIT mode (where Unicode folding and
// properties operate per byte, ASCII-only). Match offsets are byte offsets in
// both modes.
type Encoding = syntax.Encoding

const (
	// UTF8 is the default encoding: the dot and byte-oriented classes advance by
	// a whole UTF-8 code point.
	UTF8 = syntax.UTF8
	// ASCII8BIT is Ruby's binary (/n) encoding: every atom advances one byte.
	ASCII8BIT = syntax.ASCII8BIT
)

// Op is the opcode of a VM instruction.
type Op int

const (
	// OpChar matches the single byte B and advances.
	OpChar Op = iota
	// OpFoldChar matches one UTF-8 code point case-insensitively (/i): it decodes
	// the code point at the cursor and accepts it when it is in the same simple
	// -case-folding orbit as Rune, advancing by that code point's byte length. It
	// is rune-aware, like OpUniProp.
	OpFoldChar
	// OpAny matches any byte and advances; unless DotAll is set it excludes
	// '\n' (Ruby's /m option makes the dot match a newline too).
	OpAny
	// OpClass matches a byte in (or, if Negate, not in) Ranges and advances. When
	// Props or RuneRanges is non-empty, or Fold is set, the class is rune-aware: it
	// decodes one UTF-8 code point and tests it against Ranges, RuneRanges and Props
	// before Negate is applied, advancing by the code point's byte length.
	OpClass
	// OpUniProp matches one UTF-8 code point that is a member of (or, if Negate,
	// not a member of) the Unicode property Prop, advancing by its byte length.
	OpUniProp
	// OpSplit forks: try X first, then Y on backtrack (greedy ordering).
	OpSplit
	// OpJmp jumps to X.
	OpJmp
	// OpSave records the current position into capture slot Slot.
	OpSave
	// OpAssertBeginText asserts the start of the input (\A).
	OpAssertBeginText
	// OpAssertEndText asserts the end of the input (\z).
	OpAssertEndText
	// OpAssertEndTextOptNL asserts end of input, allowing one trailing '\n' (\Z).
	OpAssertEndTextOptNL
	// OpAssertBeginLine asserts the start of a line (^).
	OpAssertBeginLine
	// OpAssertEndLine asserts the end of a line ($).
	OpAssertEndLine
	// OpBackref matches the text previously captured by group Slot.
	OpBackref
	// OpCall invokes a subexpression call (\g<…>): it pushes the return address
	// (the pc just past this instruction) onto the VM's call stack and jumps to X,
	// the entry pc of the referenced group's sub-program (group 0's entry is the
	// whole pattern). The called sub-program re-runs and re-captures, and on
	// reaching its closing OpReturn control returns to the saved address.
	OpCall
	// OpReturn ends the callable sub-program of the group whose index it carries in
	// Slot. It completes a \g<…> call only when the active call frame is a call to
	// *this* group (frame.group == Slot): it then pops that frame and jumps to the
	// saved return address. Otherwise — there is no active call, or the active call
	// targets an enclosing group and execution is merely passing linearly through a
	// nested group's terminator — it falls through to the next instruction. Tagging
	// the return with its group index is what lets a nested group's OpReturn be
	// skipped during an outer group's recursive call instead of stealing its frame.
	OpReturn
	// OpAssertPrevMatch asserts the position equals the scan/previous-match start
	// (\G).
	OpAssertPrevMatch
	// OpLook begins a lookaround assertion. Its sub-program is emitted inline
	// immediately after it and is terminated by OpLookEnd; X is the continuation
	// pc just past that OpLookEnd. Negate selects the negative form, Behind the
	// lookbehind form, and Min/Max bound the lookbehind width.
	OpLook
	// OpLookEnd marks a successful run of a lookaround sub-program.
	OpLookEnd
	// OpAtomicBegin opens an atomic (possessive) group (?>…): it records the
	// current backtrack-stack depth so the matching OpAtomicEnd can discard every
	// alternative created while the group's body matched. It is a no-op on input
	// position; only the backtrack stack is touched.
	OpAtomicBegin
	// OpAtomicEnd closes an atomic group: it truncates the backtrack stack back to
	// the depth its OpAtomicBegin recorded, dropping every backtrack point created
	// inside the group. After this the group's sub-match is committed — the engine
	// can never re-enter the body to try a shorter repetition or an alternate
	// sub-match — which is exactly the possessive/atomic barrier.
	OpAtomicEnd
	// OpMatch reports a successful match.
	OpMatch
)

// Inst is a single VM instruction. Only the fields relevant to its Op are used.
type Inst struct {
	Op         Op
	B          byte                 // OpChar
	Rune       rune                 // OpFoldChar
	X, Y       int                  // OpSplit, OpJmp
	GuardTo    int                  // OpSplit: where the empty-loop guard jumps on revisit
	Quant      bool                 // OpSplit: this split is a quantifier (*, +, ?, {m,n}) decision, not an alternation fork. Its GuardTo is the deterministic continuation past the whole quantifier, which the prefilter's mandatory-spine walk follows; an alternation split (Quant=false) is a true branch the spine must stop at.
	Slot       int                  // OpSave
	Ranges     []ast.ClassRange     // OpClass
	RuneRanges []ast.RuneClassRange // OpClass (rune-aware code-point ranges: literal multi-byte members in UTF8, /i-folded members, \R)
	Props      []ast.PropRef        // OpClass (rune-aware members)
	Prop       ast.PropRef          // OpUniProp
	Negate     bool                 // OpClass, OpLook
	Behind     bool                 // OpLook
	Fold       bool                 // OpClass (case-insensitive, /i: rune-aware folding)
	DotAll     bool                 // OpAny (Ruby /m: the dot also matches '\n')
	Min        int                  // OpLook (lookbehind width lower bound)
	Max        int                  // OpLook (lookbehind width upper bound)
}

// Program is a compiled regular expression: the instruction list, the number of
// capture groups (group 0 being the whole match), and the named-group map.
//
// HasBackref records whether any instruction reads a capture (OpBackref). The VM
// uses it to decide whether (instruction, position) memoization is sound: with no
// backreference, captures are write-only and never influence whether a match can
// succeed, so two arrivals at the same (pc, sp) have identical futures and the
// later one can be pruned. A backreference makes the future depend on captured
// text, so memoization is disabled for such programs.
//
// HasCall records whether any instruction is a subexpression call (OpCall). A
// call re-runs and re-captures a group, so like a backreference it makes the
// future depend on captured/recursive state; the VM therefore disables the
// persistent (pc, sp) memo for such programs and relies on the recursion-depth
// and step budgets to bound pathological recursion.
type Program struct {
	Insts      []Inst
	NumCapture int
	Names      map[string]int
	HasBackref bool
	HasCall    bool
	// Enc is the input encoding (UTF8 by default, ASCII8BIT for binary /n). It
	// governs how the dot and byte-oriented classes advance: by a whole code
	// point in UTF8 mode, by one byte in ASCII8BIT mode.
	Enc Encoding
}

// NumSlots returns the number of save slots the VM must allocate (two per
// capture group, including group 0).
func (p *Program) NumSlots() int {
	return 2 * (p.NumCapture + 1)
}

// builder accumulates instructions during compilation. It also records, for each
// capturing group (and group 0, the whole pattern), the entry pc of its callable
// sub-program, plus the OpCall sites that must be patched to point at those entry
// pcs once every group has been laid out — a \g<…> may call a group that is
// compiled later in the instruction stream.
type builder struct {
	insts   []Inst
	entry   map[int]int // group index → entry pc of its callable sub-program
	patches []callSite  // OpCall instructions awaiting an entry pc
}

// callSite is one OpCall instruction (at index pc) and the group index it calls.
type callSite struct {
	pc    int
	group int
}

func (b *builder) emit(in Inst) int {
	b.insts = append(b.insts, in)
	return len(b.insts) - 1
}

// Compile turns a parse result into an executable program in the default UTF-8
// encoding. It wraps the whole pattern in save slots 0/1 (the overall match
// span) and terminates with OpMatch.
func Compile(r syntax.Result) *Program {
	return CompileEnc(r, UTF8)
}

// CompileEnc is Compile with an explicit input encoding (see Encoding). UTF8
// makes the dot and byte-oriented classes advance by a whole code point;
// ASCII8BIT makes every atom advance one byte.
func CompileEnc(r syntax.Result, enc Encoding) *Program {
	b := &builder{entry: map[int]int{}}
	b.emit(Inst{Op: OpSave, Slot: 0})
	// Group 0's callable sub-program (the target of \g<0>) is the whole pattern,
	// entered just after the overall-match open save.
	b.entry[0] = len(b.insts)
	b.node(r.Root)
	b.emit(Inst{Op: OpSave, Slot: 1})
	// An OpReturn (tagged with group index 0) terminates group 0 so a \g<0>
	// recursion returns here; reached by ordinary execution (no active call to
	// group 0) it falls through to OpMatch.
	b.emit(Inst{Op: OpReturn, Slot: 0})
	b.emit(Inst{Op: OpMatch})
	// Now that every group's entry pc is known, patch the call sites.
	for _, c := range b.patches {
		b.insts[c.pc].X = b.entry[c.group]
	}
	hasBackref := false
	hasCall := false
	for i := range b.insts {
		switch b.insts[i].Op {
		case OpBackref:
			hasBackref = true
		case OpCall:
			hasCall = true
		}
	}
	return &Program{Insts: b.insts, NumCapture: r.NumCapture, Names: r.Names, HasBackref: hasBackref, HasCall: hasCall, Enc: enc}
}

// node compiles one AST node, appending its instructions.
func (b *builder) node(n ast.Node) {
	switch t := n.(type) {
	case *ast.Empty:
		// Nothing to emit.
	case *ast.Literal:
		b.emit(Inst{Op: OpChar, B: t.B})
	case *ast.FoldLiteral:
		b.emit(Inst{Op: OpFoldChar, Rune: t.R})
	case *ast.AnyChar:
		b.emit(Inst{Op: OpAny, DotAll: t.DotAll})
	case *ast.Class:
		b.emit(Inst{Op: OpClass, Ranges: t.Ranges, RuneRanges: t.RuneRanges, Props: t.Props, Negate: t.Negate, Fold: t.Fold})
	case *ast.UnicodeProp:
		b.emit(Inst{Op: OpUniProp, Prop: ast.PropRef{Name: t.Name, Negate: t.Negate}})
	case *ast.Anchor:
		b.anchor(t)
	case *ast.Concat:
		for _, s := range t.Subs {
			b.node(s)
		}
	case *ast.Alternate:
		b.alternate(t)
	case *ast.Group:
		b.group(t)
	case *ast.Backref:
		b.emit(Inst{Op: OpBackref, Slot: t.Index, Fold: t.Fold})
	case *ast.Call:
		// Emit a call whose target entry pc is patched in once every group's
		// sub-program has been laid out (the callee may be defined later). Slot
		// carries the called group index so OpReturn can tell whether a group's
		// terminator belongs to this call or is merely on the linear path.
		pc := b.emit(Inst{Op: OpCall, Slot: t.Index})
		b.patches = append(b.patches, callSite{pc: pc, group: t.Index})
	case *ast.Look:
		b.look(t)
	case *ast.Atomic:
		b.atomic(t)
	case *ast.Star:
		b.repeat(t)
	}
}

func (b *builder) anchor(a *ast.Anchor) {
	switch a.Kind {
	case ast.AnchorBeginText:
		b.emit(Inst{Op: OpAssertBeginText})
	case ast.AnchorEndText:
		b.emit(Inst{Op: OpAssertEndText})
	case ast.AnchorEndTextOptNL:
		b.emit(Inst{Op: OpAssertEndTextOptNL})
	case ast.AnchorBeginLine:
		b.emit(Inst{Op: OpAssertBeginLine})
	case ast.AnchorEndLine:
		b.emit(Inst{Op: OpAssertEndLine})
	case ast.AnchorPrevMatch:
		b.emit(Inst{Op: OpAssertPrevMatch})
	}
}

// look compiles a lookaround assertion. It emits OpLook, then the sub-program
// inline, then OpLookEnd, and finally patches OpLook.X to the continuation just
// past OpLookEnd. For lookbehind it records the sub-pattern's byte-width bounds
// so the VM can position the nested run correctly.
func (b *builder) look(l *ast.Look) {
	look := b.emit(Inst{Op: OpLook, Negate: l.Negate, Behind: l.Behind, Min: l.Min, Max: l.Max})
	b.node(l.Sub)
	b.emit(Inst{Op: OpLookEnd})
	b.insts[look].X = len(b.insts)
}

// atomic compiles an atomic (possessive) group (?>…): OpAtomicBegin, the body,
// OpAtomicEnd. The pair brackets the body so that once it matches, every
// backtrack point the body created is discarded — a non-backtrackable barrier.
// Possessive quantifiers are lowered by the parser to an Atomic wrapping the
// equivalent greedy quantifier, so they reuse this exact emission.
func (b *builder) atomic(a *ast.Atomic) {
	b.emit(Inst{Op: OpAtomicBegin})
	b.node(a.Sub)
	b.emit(Inst{Op: OpAtomicEnd})
}

func (b *builder) group(g *ast.Group) {
	if g.Capture {
		// Record this group's entry pc so a \g<index> call can jump here, then emit
		// the open save, the body, the close save, and an OpReturn that completes a
		// call (or, with an empty call stack, falls through to whatever follows the
		// group in ordinary execution).
		b.entry[g.Index] = len(b.insts)
		b.emit(Inst{Op: OpSave, Slot: 2 * g.Index})
		b.node(g.Sub)
		b.emit(Inst{Op: OpSave, Slot: 2*g.Index + 1})
		b.emit(Inst{Op: OpReturn, Slot: g.Index})
		return
	}
	b.node(g.Sub)
}

// alternate compiles a|b|c as a chain of splits, preferring earlier
// alternatives (leftmost-first).
func (b *builder) alternate(a *ast.Alternate) {
	var jmps []int
	for i, sub := range a.Subs {
		last := i == len(a.Subs)-1
		var split int
		if !last {
			split = b.emit(Inst{Op: OpSplit})
		}
		start := len(b.insts)
		b.node(sub)
		if !last {
			b.insts[split].X = start
			jmps = append(jmps, b.emit(Inst{Op: OpJmp}))
			b.insts[split].Y = len(b.insts)
			// An alternation split is not a loop; revisiting it at the same position
			// means its X branch was already explored, so the empty-loop guard takes
			// the Y branch (the next alternative).
			b.insts[split].GuardTo = b.insts[split].Y
		}
	}
	end := len(b.insts)
	for _, j := range jmps {
		b.insts[j].X = end
	}
}

// repeat compiles a quantifier {min,max}. It unrolls the required minimum, then
// emits the optional part as a loop (max == -1) or a chain of optional copies
// (bounded max). The split at each optional decision point encodes the matching
// preference: greedy tries the body first and the exit on backtrack, non-greedy
// (lazy, s.Greedy == false) tries the exit first and the body only when forced.
func (b *builder) repeat(s *ast.Star) {
	// Required copies.
	for i := 0; i < s.Min; i++ {
		b.node(s.Sub)
	}
	switch {
	case s.Max == -1:
		b.starLoop(s.Sub, s.Greedy)
	default:
		// Optional copies: max-min nested optional splits. Each split's body
		// instructions follow immediately; greedy enters the body first (body is the
		// X/preferred branch) while lazy takes the exit first (body is the Y/give-back
		// branch, exit becomes X). The exit target is the common end, patched below.
		var splits []int
		for i := 0; i < s.Max-s.Min; i++ {
			split := b.emit(Inst{Op: OpSplit, Quant: true})
			splits = append(splits, split)
			bodyPC := len(b.insts)
			if s.Greedy {
				b.insts[split].X = bodyPC
			} else {
				b.insts[split].Y = bodyPC
			}
			b.node(s.Sub)
		}
		end := len(b.insts)
		for _, sp := range splits {
			if s.Greedy {
				b.insts[sp].Y = end
			} else {
				b.insts[sp].X = end
			}
			// On revisit, the empty-loop guard skips the body and goes to the exit
			// (the give-back branch for greedy, the preferred branch for lazy).
			b.insts[sp].GuardTo = end
		}
	}
}

// starLoop emits an unbounded loop over sub: split, body, jmp back to split. A
// greedy loop prefers the body (split.X = body, split.Y = exit); a lazy loop
// prefers the exit (split.X = exit, split.Y = body), so the body is entered only
// when continuing past the loop fails.
func (b *builder) starLoop(sub ast.Node, greedy bool) {
	split := b.emit(Inst{Op: OpSplit, Quant: true})
	bodyPC := len(b.insts)
	b.node(sub)
	b.emit(Inst{Op: OpJmp, X: split})
	exitPC := len(b.insts)
	if greedy {
		b.insts[split].X = bodyPC
		b.insts[split].Y = exitPC
	} else {
		b.insts[split].X = exitPC
		b.insts[split].Y = bodyPC
	}
	// The empty-loop guard must leave the loop on revisit, regardless of which
	// branch is the body: it always jumps to the exit. (For a greedy loop the exit
	// is Y, for a lazy loop it is X — the previous code assumed Y, which spun a
	// lazy empty loop until the step budget.)
	b.insts[split].GuardTo = exitPC
}
