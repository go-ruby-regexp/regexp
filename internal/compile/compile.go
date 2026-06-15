// Package compile lowers a syntax AST into the flat instruction program that
// the backtracking VM executes.
package compile

import (
	"github.com/go-onigmo/regexp/internal/ast"
	"github.com/go-onigmo/regexp/internal/syntax"
)

// Op is the opcode of a VM instruction.
type Op int

const (
	// OpChar matches the single byte B and advances.
	OpChar Op = iota
	// OpAny matches any byte and advances; unless DotAll is set it excludes
	// '\n' (Ruby's /m option makes the dot match a newline too).
	OpAny
	// OpClass matches a byte in (or, if Negate, not in) Ranges and advances. When
	// Props is non-empty the class is rune-aware: it decodes one UTF-8 code point
	// and tests it against both Ranges and Props before Negate is applied,
	// advancing by the code point's byte length.
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
	// OpMatch reports a successful match.
	OpMatch
)

// Inst is a single VM instruction. Only the fields relevant to its Op are used.
type Inst struct {
	Op     Op
	B      byte             // OpChar
	X, Y   int              // OpSplit, OpJmp
	Slot   int              // OpSave
	Ranges []ast.ClassRange // OpClass
	Props  []ast.PropRef    // OpClass (rune-aware members)
	Prop   ast.PropRef      // OpUniProp
	Negate bool             // OpClass, OpLook
	Behind bool             // OpLook
	Fold   bool             // OpChar, OpClass (case-insensitive, /i)
	DotAll bool             // OpAny (Ruby /m: the dot also matches '\n')
	Min    int              // OpLook (lookbehind width lower bound)
	Max    int              // OpLook (lookbehind width upper bound)
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
type Program struct {
	Insts      []Inst
	NumCapture int
	Names      map[string]int
	HasBackref bool
}

// NumSlots returns the number of save slots the VM must allocate (two per
// capture group, including group 0).
func (p *Program) NumSlots() int {
	return 2 * (p.NumCapture + 1)
}

// builder accumulates instructions during compilation.
type builder struct {
	insts []Inst
}

func (b *builder) emit(in Inst) int {
	b.insts = append(b.insts, in)
	return len(b.insts) - 1
}

// Compile turns a parse result into an executable program. It wraps the whole
// pattern in save slots 0/1 (the overall match span) and terminates with
// OpMatch.
func Compile(r syntax.Result) *Program {
	b := &builder{}
	b.emit(Inst{Op: OpSave, Slot: 0})
	b.node(r.Root)
	b.emit(Inst{Op: OpSave, Slot: 1})
	b.emit(Inst{Op: OpMatch})
	hasBackref := false
	for i := range b.insts {
		if b.insts[i].Op == OpBackref {
			hasBackref = true
			break
		}
	}
	return &Program{Insts: b.insts, NumCapture: r.NumCapture, Names: r.Names, HasBackref: hasBackref}
}

// node compiles one AST node, appending its instructions.
func (b *builder) node(n ast.Node) {
	switch t := n.(type) {
	case *ast.Empty:
		// Nothing to emit.
	case *ast.Literal:
		b.emit(Inst{Op: OpChar, B: t.B, Fold: t.Fold})
	case *ast.AnyChar:
		b.emit(Inst{Op: OpAny, DotAll: t.DotAll})
	case *ast.Class:
		b.emit(Inst{Op: OpClass, Ranges: t.Ranges, Props: t.Props, Negate: t.Negate, Fold: t.Fold})
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
	case *ast.Look:
		b.look(t)
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

func (b *builder) group(g *ast.Group) {
	if g.Capture {
		b.emit(Inst{Op: OpSave, Slot: 2 * g.Index})
		b.node(g.Sub)
		b.emit(Inst{Op: OpSave, Slot: 2*g.Index + 1})
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
		}
	}
	end := len(b.insts)
	for _, j := range jmps {
		b.insts[j].X = end
	}
}

// repeat compiles a quantifier {min,max} greedily. It unrolls the required
// minimum, then emits the optional part as a loop (max == -1) or a chain of
// optional copies (bounded max).
func (b *builder) repeat(s *ast.Star) {
	// Required copies.
	for i := 0; i < s.Min; i++ {
		b.node(s.Sub)
	}
	switch {
	case s.Max == -1:
		b.starLoop(s.Sub)
	default:
		// Optional copies: max-min nested optional groups.
		var splits []int
		for i := 0; i < s.Max-s.Min; i++ {
			split := b.emit(Inst{Op: OpSplit})
			splits = append(splits, split)
			b.insts[split].X = len(b.insts)
			b.node(s.Sub)
		}
		end := len(b.insts)
		for _, sp := range splits {
			b.insts[sp].Y = end
		}
	}
}

// starLoop emits a greedy unbounded loop over sub: split (prefer body), body,
// jmp back to split.
func (b *builder) starLoop(sub ast.Node) {
	split := b.emit(Inst{Op: OpSplit})
	b.insts[split].X = len(b.insts)
	b.node(sub)
	b.emit(Inst{Op: OpJmp, X: split})
	b.insts[split].Y = len(b.insts)
}
