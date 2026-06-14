// Package vm executes a compiled program against an input using explicit
// backtracking with greedy, leftmost-first semantics (as in Ruby/Onigmo).
package vm

import (
	"errors"

	"github.com/go-onigmo/regexp/internal/compile"
)

// ErrBudget is returned when a match exceeds the configured backtrack-step
// budget. It is the deterministic hook later phases use for ReDoS hardening.
var ErrBudget = errors.New("backtrack step budget exceeded")

// DefaultBudget is the maximum number of VM steps a single search may take
// before it aborts. It is intentionally high so well-behaved patterns never hit
// it.
const DefaultBudget = 100_000_000

// thread is one entry on the backtrack stack: a program counter, an input
// position, and a snapshot of the capture slots.
type thread struct {
	pc   int
	sp   int
	caps []int
}

// machine holds the per-search execution state.
type machine struct {
	prog   *compile.Program
	input  string
	budget int
	stack  []thread
	// visited guards against empty-width loops: it records the (pc, sp) pairs
	// reached without consuming input since the last advance, so an
	// empty-matching body under * cannot spin forever.
	visited map[int64]bool
}

// Match runs prog against input, scanning start positions left to right until a
// match is found. It returns the capture slots (len == prog.NumSlots), whether
// a match was found, and an error only when the step budget is exhausted.
func Match(prog *compile.Program, input string, budget int) ([]int, bool, error) {
	m := &machine{
		prog:    prog,
		input:   input,
		budget:  budget,
		visited: make(map[int64]bool),
	}
	for start := 0; start <= len(input); start++ {
		caps := make([]int, prog.NumSlots())
		for i := range caps {
			caps[i] = -1
		}
		result, ok, err := m.run(start, caps)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return result, true, nil
		}
	}
	return nil, false, nil
}

// run attempts a match anchored at position start. On success it returns the
// final capture slots.
func (m *machine) run(start int, caps []int) ([]int, bool, error) {
	m.stack = m.stack[:0]
	clear(m.visited)
	pc := 0
	sp := start
	for {
		if m.budget <= 0 {
			return nil, false, ErrBudget
		}
		m.budget--

		in := m.prog.Insts[pc]
		switch in.Op {
		case compile.OpChar:
			if sp < len(m.input) && m.input[sp] == in.B {
				pc++
				sp++
				clear(m.visited)
				continue
			}
		case compile.OpAny:
			if sp < len(m.input) && m.input[sp] != '\n' {
				pc++
				sp++
				clear(m.visited)
				continue
			}
		case compile.OpClass:
			if sp < len(m.input) && classMatch(in, m.input[sp]) {
				pc++
				sp++
				clear(m.visited)
				continue
			}
		case compile.OpSplit:
			key := int64(pc)<<32 | int64(sp)
			if m.visited[key] {
				// Already explored this split at this position without
				// progress; do not re-enter the body. Fall through to the Y
				// branch directly instead of looping.
				pc = in.Y
				continue
			}
			m.visited[key] = true
			m.push(in.Y, sp, caps)
			pc = in.X
			continue
		case compile.OpJmp:
			pc = in.X
			continue
		case compile.OpSave:
			caps = m.saveSlot(caps, in.Slot, sp)
			pc++
			continue
		case compile.OpAssertBeginText:
			if sp == 0 {
				pc++
				continue
			}
		case compile.OpAssertEndText:
			if sp == len(m.input) {
				pc++
				continue
			}
		case compile.OpAssertEndTextOptNL:
			if sp == len(m.input) || (sp == len(m.input)-1 && m.input[sp] == '\n') {
				pc++
				continue
			}
		case compile.OpAssertBeginLine:
			if sp == 0 || m.input[sp-1] == '\n' {
				pc++
				continue
			}
		case compile.OpAssertEndLine:
			if sp == len(m.input) || m.input[sp] == '\n' {
				pc++
				continue
			}
		case compile.OpMatch:
			return caps, true, nil
		}

		// Failure: backtrack to the most recent alternative, if any.
		if len(m.stack) == 0 {
			return nil, false, nil
		}
		t := m.stack[len(m.stack)-1]
		m.stack = m.stack[:len(m.stack)-1]
		pc = t.pc
		sp = t.sp
		caps = t.caps
	}
}

// saveSlot writes pos into slot, copying caps first so that backtrack threads
// keep their snapshot intact.
func (m *machine) saveSlot(caps []int, slot, pos int) []int {
	nc := make([]int, len(caps))
	copy(nc, caps)
	nc[slot] = pos
	return nc
}

func (m *machine) push(pc, sp int, caps []int) {
	snap := make([]int, len(caps))
	copy(snap, caps)
	m.stack = append(m.stack, thread{pc: pc, sp: sp, caps: snap})
}

// classMatch reports whether byte b is accepted by an OpClass instruction.
func classMatch(in compile.Inst, b byte) bool {
	inSet := false
	for _, r := range in.Ranges {
		if b >= r.Lo && b <= r.Hi {
			inSet = true
			break
		}
	}
	return inSet != in.Negate
}
