package vm

import "github.com/go-ruby-regexp/regexp/internal/compile"

// classRun is the fast anchored consumer for a program that is exactly a single
// anchored repeat of one byte-decidable atom — the StringScanner#skip / #match?
// shape (`\s+`, `\S+`, `\w+`, `[0-9]+`, `.+`, `a+`). Anchored at pos, such a
// pattern's whole answer is "how far does the class run from pos", which a tight
// loop over a 256-bit membership bitset settles in one indexed bit test per byte,
// with no per-position NFA state to seed, close, or intern. It replaces the
// per-step DFA simulation for exactly this shape; every other program keeps the
// general engine.
//
// The bitset is the ASCII fast path: a byte < 0x80 is its own one-byte code point
// in both encodings, so its membership is a bit read. Under UTF-8 a byte >= 0x80
// begins a multi-byte code point the byte bitset cannot decide (a negated class
// such as `\S` matches whole non-ASCII code points, a positive byte class does
// not), so the consumer stops and hands the whole match to the general engine —
// correct, just not accelerated, and rare on the ASCII-dominated tokenizer input
// this targets. Under ASCII8BIT every byte is a one-byte code point, so the whole
// 256-bit set is authoritative and no byte forces a fallback.
type classRun struct {
	// accept is the 256-bit membership set of the repeated atom: bit b is set iff
	// the atom accepts byte b as a complete one-byte code point. Built once at
	// compile time from the atom's class ranges (with Negate applied), the dot's
	// newline rule, or the single literal byte.
	accept [4]uint64
	min    int // repeat lower bound (OpLoop.Min)
	max    int // repeat upper bound, -1 unbounded (OpLoop.Max)
	greedy bool
}

// detectClassRun returns a classRun when prog is exactly a capture-free, single
// anchored repeat of one byte-decidable atom (an OpClass with only byte ranges,
// the dot, or a single ASCII literal), or nil otherwise. The recognised program
// shape is precisely what the compiler emits for a bare `X+`/`X*`/`X{m,n}` whose
// body is one such atom: the overall-match open save, the fused OpLoop, the close
// save, the group-0 return, and OpMatch — five instructions, no captures, no
// leading anchor or lookaround. Anything else (a capture, an anchor, a rune-aware
// class, a possessive/atomic wrapper, a multi-atom body) returns nil and keeps the
// general engine, so semantics are unchanged.
func detectClassRun(prog *compile.Program) *classRun {
	if prog.NumCapture != 0 {
		return nil
	}
	in := prog.Insts
	// Exact shape: OpSave 0, OpLoop, OpSave 1, OpReturn, OpMatch.
	if len(in) != 5 ||
		in[0].Op != compile.OpSave || in[0].Slot != 0 ||
		in[1].Op != compile.OpLoop ||
		in[2].Op != compile.OpSave || in[2].Slot != 1 ||
		in[3].Op != compile.OpReturn ||
		in[4].Op != compile.OpMatch {
		return nil
	}
	loop := &in[1]
	cr := &classRun{min: loop.Min, max: loop.Max, greedy: loop.Greedy}
	switch loop.Sub {
	case compile.OpClass:
		// Only a byte-oriented class is bitset-decidable. A rune-aware class — one
		// with code-point ranges, a \p{…} property member, or /i folding — matches
		// whole code points outside the 256-bit set, so it is left to the general
		// engine.
		if loop.Fold || len(loop.RuneRanges) != 0 || len(loop.Props) != 0 {
			return nil
		}
		for b := 0; b < 256; b++ {
			if loop.ClassHasByte(byte(b)) != loop.Negate {
				cr.accept[b>>6] |= 1 << (uint(b) & 63)
			}
		}
	case compile.OpAny:
		// The dot accepts every byte except '\n', unless DotAll (Ruby /m) lifts that.
		for b := 0; b < 256; b++ {
			if loop.DotAll || b != '\n' {
				cr.accept[b>>6] |= 1 << (uint(b) & 63)
			}
		}
	case compile.OpChar:
		// A single-byte literal repeat (a+). A high literal byte is never reached on
		// the ASCII fast loop, so such a program keeps the general engine.
		if loop.B >= 0x80 {
			return nil
		}
		cr.accept[loop.B>>6] |= 1 << (uint(loop.B) & 63)
	default:
		return nil
	}
	return cr
}

// accepts reports whether byte b is in the atom's membership set.
func (cr *classRun) accepts(b byte) bool {
	return cr.accept[b>>6]&(1<<(uint(b)&63)) != 0
}

// match consumes the anchored class-run at pos over input under enc and returns
// the whole-match end offset (begin is always pos) with ok, when the answer is
// definite. definite is false only when the scan meets a byte it cannot decide on
// the ASCII fast path — a byte >= 0x80 under UTF-8, which begins a multi-byte code
// point — in which case the caller runs the general engine for the whole match.
//
// It reproduces the fused OpLoop's leftmost-first semantics exactly: a greedy loop
// takes the maximal run (capped at max), a lazy loop with nothing after it takes
// the minimum, and either fails when fewer than min atoms are available.
func (cr *classRun) match(input string, enc compile.Encoding, pos int) (end int, ok, definite bool) {
	limit := len(input)
	if cr.max >= 0 {
		if m := pos + cr.max; m < limit {
			limit = m
		}
	}
	utf8 := enc == compile.UTF8
	i := pos
	for i < limit {
		b := input[i]
		if utf8 && b >= 0x80 {
			// A multi-byte code point starts here; the ASCII bitset cannot decide it.
			// Hand the whole match to the general engine.
			return 0, false, false
		}
		if !cr.accepts(b) {
			break
		}
		i++
	}
	// The run stopped at a definite boundary — the max cap, end of input, or an
	// ASCII byte the atom rejects — so its length is final.
	if i-pos < cr.min {
		return 0, false, true
	}
	if cr.greedy {
		return i, true, true
	}
	// A lazy quantifier with nothing following stops at the minimum.
	return pos + cr.min, true, true
}
