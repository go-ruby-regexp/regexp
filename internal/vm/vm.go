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
	gpos   int // scan start of the current attempt, for \G
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
	// \G anchors to where the overall search began. For a single Match call that
	// is position 0; iterative scanning (gsub/scan) advances it on each step,
	// which later phases will thread through here.
	m.gpos = 0
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
		case compile.OpAssertPrevMatch:
			if sp == m.gpos {
				pc++
				continue
			}
		case compile.OpLook:
			ncaps, matched, err := m.look(pc, in, sp, caps)
			if err != nil {
				return nil, false, err
			}
			if matched != in.Negate {
				// Positive look that matched, or negative look that did not:
				// the assertion holds. Positive lookaround exposes its inner
				// captures to the rest of the pattern.
				if !in.Negate {
					caps = ncaps
				}
				pc = in.X
				continue
			}
		case compile.OpBackref:
			bgn, end := caps[2*in.Slot], caps[2*in.Slot+1]
			if bgn < 0 || end < 0 {
				// A group that did not participate matches the empty string.
				pc++
				continue
			}
			ref := m.input[bgn:end]
			if sp+len(ref) <= len(m.input) && m.input[sp:sp+len(ref)] == ref {
				pc++
				sp += len(ref)
				if len(ref) > 0 {
					clear(m.visited)
				}
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

// look evaluates a lookaround assertion whose OpLook is at lookPC. It reports
// the captures produced by a successful sub-match (for propagating positive
// lookaround captures) and whether the sub-pattern matched. The outer position
// is never advanced.
//
// For lookahead the sub-program is run anchored at sp. For lookbehind it is run
// from each candidate start position sp-w (w in [Min,Max], widest first to
// match Ruby's greedy preference), requiring the run to end exactly at sp.
func (m *machine) look(lookPC int, in compile.Inst, sp int, caps []int) ([]int, bool, error) {
	body := lookPC + 1
	if !in.Behind {
		return m.execLook(body, sp, -1, caps)
	}
	for w := in.Max; w >= in.Min; w-- {
		start := sp - w
		if start < 0 {
			continue
		}
		ncaps, ok, err := m.execLook(body, start, sp, caps)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return ncaps, true, nil
		}
	}
	return nil, false, nil
}

// execLook runs a lookaround sub-program from pc=body at position sp using an
// isolated backtrack stack and empty-loop guard, so it cannot disturb the outer
// search. endAt is the position the run must reach at OpLookEnd (-1 means any,
// used by lookahead; lookbehind passes the outer position so the sub-pattern
// must consume exactly the right number of bytes). It returns the final capture
// slots on success.
func (m *machine) execLook(body, sp, endAt int, caps []int) ([]int, bool, error) {
	var stack []thread
	visited := make(map[int64]bool)
	pc := body
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
				clear(visited)
				continue
			}
		case compile.OpAny:
			if sp < len(m.input) && m.input[sp] != '\n' {
				pc++
				sp++
				clear(visited)
				continue
			}
		case compile.OpClass:
			if sp < len(m.input) && classMatch(in, m.input[sp]) {
				pc++
				sp++
				clear(visited)
				continue
			}
		case compile.OpSplit:
			key := int64(pc)<<32 | int64(sp)
			if visited[key] {
				pc = in.Y
				continue
			}
			visited[key] = true
			snap := make([]int, len(caps))
			copy(snap, caps)
			stack = append(stack, thread{pc: in.Y, sp: sp, caps: snap})
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
		case compile.OpAssertPrevMatch:
			if sp == m.gpos {
				pc++
				continue
			}
		case compile.OpLook:
			ncaps, matched, err := m.look(pc, in, sp, caps)
			if err != nil {
				return nil, false, err
			}
			if matched != in.Negate {
				if !in.Negate {
					caps = ncaps
				}
				pc = in.X
				continue
			}
		case compile.OpBackref:
			bgn, end := caps[2*in.Slot], caps[2*in.Slot+1]
			if bgn < 0 || end < 0 {
				pc++
				continue
			}
			ref := m.input[bgn:end]
			if sp+len(ref) <= len(m.input) && m.input[sp:sp+len(ref)] == ref {
				pc++
				sp += len(ref)
				if len(ref) > 0 {
					clear(visited)
				}
				continue
			}
		case compile.OpLookEnd:
			if endAt < 0 || sp == endAt {
				return caps, true, nil
			}
		}

		// Failure: backtrack within this sub-search only.
		if len(stack) == 0 {
			return nil, false, nil
		}
		t := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
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
