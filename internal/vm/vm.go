// Package vm executes a compiled program against an input using explicit
// backtracking with greedy, leftmost-first semantics (as in Ruby/Onigmo).
package vm

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-onigmo/regexp/internal/ast"
	"github.com/go-onigmo/regexp/internal/charset"
	"github.com/go-onigmo/regexp/internal/compile"
)

// ErrBudget is returned when a match exceeds the configured backtrack-step
// budget. It is the deterministic hook later phases use for ReDoS hardening.
var ErrBudget = errors.New("backtrack step budget exceeded")

// ErrTimeout is returned when a match exceeds the configured wall-clock deadline
// (Ruby's Regexp.timeout equivalent). It is the real-time backstop that
// complements the deterministic step budget: a pathological match is aborted by
// whichever limit it hits first.
var ErrTimeout = errors.New("regexp match timeout exceeded")

// DefaultBudget is the maximum number of VM steps a single search may take
// before it aborts. It is intentionally high so well-behaved patterns never hit
// it.
const DefaultBudget = 100_000_000

// clockCheckMask gates how often the wall-clock deadline is polled. Reading the
// monotonic clock on every VM step would dominate the hot loop, so the deadline
// is checked once every clockCheckMask+1 steps (a power-of-two mask makes the
// gate a single AND). The interval is small enough that the real overrun past a
// deadline stays negligible yet large enough that the clock read is amortized to
// noise.
const clockCheckMask = 0xfff

// MaxCallDepth bounds the depth of nested subexpression calls (\g<…>) on the VM's
// call stack. A recursive grammar (e.g. balanced parentheses, \g<0> whole-pattern
// recursion) that would otherwise recurse without bound is cut off here so the
// match fails deterministically rather than exhausting the step budget or the Go
// stack. It is generous enough that any realistic nesting matches: the canonical
// balanced-parens idiom needs one call frame per nesting level. A call that would
// exceed this depth is treated as a local failure (the engine backtracks), which
// is how Onigmo's own recursion limit surfaces.
const MaxCallDepth = 4096

// callFrame is one pending subexpression call (\g<…>). group is the called group
// index, used so only that group's own OpReturn completes the call (a nested
// group's OpReturn, reached while merely passing linearly through the callee's
// body, must not steal this frame). ret is the return address (the pc just past
// the OpCall). saved holds the capture slots of every group that was open (its
// OpSave-open had fired but its OpSave-close had not) at the moment of the call,
// paired as (slot, value): on return those slots are restored so a group that
// recurses into itself keeps its *outer* binding, exactly as Onigmo/Ruby does. A
// call to a group that is not currently open saves nothing, so that group's
// freshly matched capture persists (the call's value wins, as in (\d+)-\g<1>
// where \g<1> re-captures).
type callFrame struct {
	group int
	ret   int
	saved []slotVal
}

// slotVal is one (capture-slot index, value) pair recorded for restore-on-return.
type slotVal struct {
	slot, val int
}

// thread is one entry on the backtrack stack: a program counter, an input
// position, a snapshot of the capture slots, a snapshot of the call stack (the
// pending subexpression-call return frames), and the stack of currently-open
// group indices. All are part of the thread because a \g<…> call or an open
// group entered along one branch must be unwound when the search backtracks to an
// earlier alternative.
type thread struct {
	pc     int
	sp     int
	caps   []int
	calls  []callFrame
	openg  []int
	atomic []int
}

// machine holds the per-search execution state.
type machine struct {
	prog   *compile.Program
	input  string
	budget int
	// deadline is the wall-clock instant past which the search aborts with
	// ErrTimeout. A zero deadline (deadline.IsZero()) means no time limit, so the
	// clock is never read and there is no per-step cost. clockTick counts steps so
	// the clock is polled only once every clockCheckMask+1 steps.
	deadline  time.Time
	clockTick uint64
	gpos      int // scan start of the current attempt, for \G
	stack     []thread
	// visited records (pc, sp) pairs seen at OpSplit decision points.
	//
	// It serves two related purposes. It always guards against empty-width
	// loops: an empty-matching body under * that returns to the same split at the
	// same position is cut off rather than spun forever. When memoize is set (the
	// program has no backreference), it additionally persists across consumed
	// input and so becomes full ReDoS memoization: a split state reached a second
	// time — by any backtracking path — has an identical future, so its X branch
	// is not re-explored. That collapses catastrophic backtracking (e.g.
	// (a*)*b, (a|aa)*c) from exponential to polynomial. When memoize is false a
	// backreference can read captured text, so the future is not a pure function
	// of (pc, sp); the set is then cleared on every consumed byte and only its
	// empty-loop role remains.
	visited map[int64]bool
	// memoize enables the persistent (pc, sp) memo. It is the program's
	// no-backreference property, hoisted here for the hot loop.
	memoize bool
}

// Match runs prog against input, scanning start positions left to right until a
// match is found. It returns the capture slots (len == prog.NumSlots), whether
// a match was found, and an error only when the step budget is exhausted. It
// imposes no wall-clock limit; use MatchTimeout for that.
func Match(prog *compile.Program, input string, budget int) ([]int, bool, error) {
	return MatchTimeout(prog, input, budget, time.Time{})
}

// MatchTimeout is Match with an additional wall-clock deadline (Ruby's
// Regexp.timeout equivalent). When deadline is non-zero the search aborts with
// ErrTimeout if it is still running past that instant; a pathological match is
// then bounded by whichever of the step budget or the deadline it reaches first.
// A zero deadline means no time limit, identical to Match, and incurs no
// per-step clock cost.
func MatchTimeout(prog *compile.Program, input string, budget int, deadline time.Time) ([]int, bool, error) {
	m := &machine{
		prog:     prog,
		input:    input,
		budget:   budget,
		deadline: deadline,
		visited:  make(map[int64]bool),
		// The persistent (pc, sp) memo is sound only when the future is a pure
		// function of (pc, sp). A backreference reads captured text, and a
		// subexpression call (\g<…>) re-runs/re-captures a group and carries its own
		// recursion state, so either makes two arrivals at the same (pc, sp) differ;
		// memoization is then disabled and the step budget bounds the work.
		memoize: !prog.HasBackref && !prog.HasCall,
	}
	// \G anchors to where the overall search began. For a single Match call that
	// is position 0; iterative scanning (gsub/scan) advances it on each step,
	// which later phases will thread through here.
	m.gpos = 0
	// The prefilter is a transparent accelerator: it only ever advances the scan
	// cursor to the next position that could begin a match (an exact necessary
	// condition derived from a required literal prefix, a constrained first byte,
	// or a \A anchor). Every position it yields is still run through the full VM,
	// so results are identical to the unfiltered scan; it merely skips positions
	// that provably cannot match.
	pf := analyze(prog)
	usePF := pf.usable()
	// A required interior literal must appear SOMEWHERE in every match, even when
	// the pattern has no anchor or leading literal (the "foo" of \d+foo\d+). A
	// single whole-haystack search rejects inputs that lack it before the VM runs
	// at any position. It is a necessary-but-not-sufficient condition — a present
	// literal does not imply a match — so when present the per-position scan and
	// the VM still verify exactly as before; only the no-occurrence case is
	// short-circuited. This is orthogonal to the start-locating filters above and
	// runs at most once. It is skipped when a leading literal prefix is present:
	// that prefix's own start-locating search already rejects a haystack lacking
	// it, so the gate would only repeat work.
	//
	// The same required literal also BOUNDS THE SCAN ON THE RIGHT: a match starting
	// at offset s spans [s, e) and must contain the literal wholly within it, so the
	// literal's start index j satisfies j >= s. The largest such j is the literal's
	// LAST occurrence, so no match can begin past it; the scan stops once start
	// exceeds that offset instead of grinding to end-of-input. -1 (the cap is
	// inactive) leaves the scan unbounded.
	lastViableStart := -1
	if pf.required != "" && pf.prefix == "" {
		// A forward strings.Index is the fast absence test (the common
		// non-matching case): the optimized runtime search rejects a literal-free
		// haystack in one pass. Only when the literal is present do we pay the
		// backward LastIndex to compute the right bound.
		if !strings.Contains(input, pf.required) {
			return nil, false, nil
		}
		lastViableStart = strings.LastIndex(input, pf.required)
	}
	for start := 0; start <= len(input); start++ {
		if lastViableStart >= 0 && start > lastViableStart {
			// Past the last position from which a match could still contain the
			// required literal: the scan is exhausted.
			return nil, false, nil
		}
		if usePF {
			next := pf.nextStart(input, start)
			if next < 0 {
				// No further position can begin a match.
				return nil, false, nil
			}
			// nextStart only ever returns a position in [start, len(input)] (a
			// required prefix or in-set byte is found within bounds) or -1, so the
			// advanced cursor stays a valid start offset.
			start = next
		}
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
	// A fresh search starts from an empty memo. Each start position is an
	// independent attempt; a (pc, sp) that failed from an earlier start could in
	// principle be re-reached, but the memo is reset per start to keep its meaning
	// simple (failure of this whole attempt) and bounded.
	clear(m.visited)
	pc := 0
	sp := start
	var calls []callFrame // pending subexpression calls (\g<…>)
	var openg []int       // indices of groups currently open (for call save/restore)
	var atomic []int      // backtrack-stack depths recorded by open atomic groups (?>…)
	for {
		if err := m.tick(); err != nil {
			return nil, false, err
		}

		in := m.prog.Insts[pc]
		switch in.Op {
		case compile.OpChar:
			if sp < len(m.input) && charMatch(in, m.input[sp]) {
				pc++
				sp++
				m.consumed()
				continue
			}
		case compile.OpFoldChar:
			if ok, w := m.foldCharStep(in, sp); ok {
				pc++
				sp += w
				m.consumed()
				continue
			}
		case compile.OpAny:
			if ok, w := m.anyStep(in, sp); ok {
				pc++
				sp += w
				m.consumed()
				continue
			}
		case compile.OpClass:
			if ok, w := m.classStep(in, sp); ok {
				pc++
				sp += w
				m.consumed()
				continue
			}
		case compile.OpUniProp:
			if ok, w := m.propStep(in, sp); ok {
				pc++
				sp += w
				m.consumed()
				continue
			}
		case compile.OpSplit:
			key := int64(pc)<<32 | int64(sp)
			if m.visited[key] {
				// Already explored this split at this position without
				// progress; do not re-enter the body. Jump to the split's exit
				// branch (GuardTo) instead of looping — for a lazy loop the exit is
				// X, not Y, so a fixed "go to Y" would spin the empty body.
				pc = in.GuardTo
				continue
			}
			m.visited[key] = true
			m.push(in.Y, sp, caps, calls, openg, atomic)
			pc = in.X
			continue
		case compile.OpJmp:
			pc = in.X
			continue
		case compile.OpSave:
			caps = m.saveSlot(caps, in.Slot, sp)
			openg = trackOpen(openg, in.Slot)
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
			if sp+len(ref) <= len(m.input) && bytesEqual(m.input[sp:sp+len(ref)], ref, in.Fold) {
				pc++
				sp += len(ref)
				if len(ref) > 0 {
					m.consumed()
				}
				continue
			}
		case compile.OpCall:
			// A subexpression call (\g<…>): record a return frame (capturing the
			// slots of every currently-open group so a recursive self-call restores
			// the outer binding on return) and jump to the callee's entry. The depth
			// cap turns unbounded recursion into a local failure so the engine
			// backtracks rather than blowing the Go stack.
			if len(calls) >= MaxCallDepth {
				break
			}
			calls = append(calls, callFrame{group: in.Slot, ret: pc + 1, saved: saveOpenSlots(caps, openg)})
			pc = in.X
			continue
		case compile.OpReturn:
			if n := len(calls); n > 0 && calls[n-1].group == in.Slot {
				// This is the terminator of the group the active call targets:
				// return to the caller, restoring the open-group captures the call
				// may have overwritten.
				f := calls[n-1]
				calls = calls[:n-1]
				caps = restoreSlots(caps, f.saved)
				pc = f.ret
				continue
			}
			// No active call for this group: reached by ordinary execution (or by
			// passing linearly through a nested group's terminator during an
			// enclosing group's call). Fall through to the next instruction.
			pc++
			continue
		case compile.OpAtomicBegin:
			// Enter an atomic (?>…) span: remember how deep the backtrack stack is
			// now, so its OpAtomicEnd can drop every alternative the body adds.
			atomic = pushInt(atomic, len(m.stack))
			pc++
			continue
		case compile.OpAtomicEnd:
			// The atomic body matched: discard every backtrack point it created,
			// committing this sub-match (no shorter repetition / alternate is ever
			// retried). The matching OpAtomicBegin is always the most recent mark on
			// this path, so it is the top of the atomic stack.
			mark := atomic[len(atomic)-1]
			atomic = atomic[:len(atomic)-1]
			m.stack = m.stack[:mark]
			pc++
			continue
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
		calls = t.calls
		openg = t.openg
		atomic = t.atomic
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
	var calls []callFrame // \g<…> return frames pending inside this sub-search
	var openg []int       // groups currently open inside this sub-search
	var atomic []int      // open atomic-group marks inside this sub-search
	visited := make(map[int64]bool)
	pc := body
	for {
		if err := m.tick(); err != nil {
			return nil, false, err
		}

		in := m.prog.Insts[pc]
		switch in.Op {
		case compile.OpChar:
			if sp < len(m.input) && charMatch(in, m.input[sp]) {
				pc++
				sp++
				clear(visited)
				continue
			}
		case compile.OpFoldChar:
			if ok, w := m.foldCharStep(in, sp); ok {
				pc++
				sp += w
				clear(visited)
				continue
			}
		case compile.OpAny:
			if ok, w := m.anyStep(in, sp); ok {
				pc++
				sp += w
				clear(visited)
				continue
			}
		case compile.OpClass:
			if ok, w := m.classStep(in, sp); ok {
				pc++
				sp += w
				clear(visited)
				continue
			}
		case compile.OpUniProp:
			if ok, w := m.propStep(in, sp); ok {
				pc++
				sp += w
				clear(visited)
				continue
			}
		case compile.OpSplit:
			key := int64(pc)<<32 | int64(sp)
			if visited[key] {
				pc = in.GuardTo
				continue
			}
			visited[key] = true
			snap := make([]int, len(caps))
			copy(snap, caps)
			stack = append(stack, thread{pc: in.Y, sp: sp, caps: snap, calls: snapshotCalls(calls), openg: snapshotInts(openg), atomic: snapshotInts(atomic)})
			pc = in.X
			continue
		case compile.OpJmp:
			pc = in.X
			continue
		case compile.OpAtomicBegin:
			// Atomic (?>…) inside a lookaround body: same mechanism, scoped to this
			// sub-search's own backtrack stack.
			atomic = pushInt(atomic, len(stack))
			pc++
			continue
		case compile.OpAtomicEnd:
			mark := atomic[len(atomic)-1]
			atomic = atomic[:len(atomic)-1]
			stack = stack[:mark]
			pc++
			continue
		case compile.OpCall:
			// A subexpression call inside a lookaround body, with the same
			// recursion-depth cap and open-group save/restore as the main search.
			if len(calls) >= MaxCallDepth {
				break
			}
			calls = append(calls, callFrame{group: in.Slot, ret: pc + 1, saved: saveOpenSlots(caps, openg)})
			pc = in.X
			continue
		case compile.OpReturn:
			if n := len(calls); n > 0 && calls[n-1].group == in.Slot {
				f := calls[n-1]
				calls = calls[:n-1]
				caps = restoreSlots(caps, f.saved)
				pc = f.ret
				continue
			}
			pc++
			continue
		case compile.OpSave:
			caps = m.saveSlot(caps, in.Slot, sp)
			openg = trackOpen(openg, in.Slot)
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
			if sp+len(ref) <= len(m.input) && bytesEqual(m.input[sp:sp+len(ref)], ref, in.Fold) {
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
		calls = t.calls
		openg = t.openg
		atomic = t.atomic
	}
}

// tick accounts one VM step against both limits and reports the error to abort
// with, if any. It always decrements the deterministic step budget (ErrBudget on
// exhaustion). When a wall-clock deadline is set it additionally polls the
// monotonic clock, but only once every clockCheckMask+1 steps so the clock read
// is amortized away from the hot path; a zero deadline skips the clock entirely
// so an unlimited search pays nothing for the timeout machinery.
func (m *machine) tick() error {
	if m.budget <= 0 {
		return ErrBudget
	}
	m.budget--
	if !m.deadline.IsZero() {
		m.clockTick++
		if m.clockTick&clockCheckMask == 0 && time.Now().After(m.deadline) {
			return ErrTimeout
		}
	}
	return nil
}

// consumed is called after the main search advances past one or more input
// bytes. When memoization is off it resets the (pc, sp) set so it only guards
// empty-width loops since the last advance; when memoization is on the set
// persists across consumed input (becoming the ReDoS memo) and nothing is reset.
func (m *machine) consumed() {
	if !m.memoize {
		clear(m.visited)
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

func (m *machine) push(pc, sp int, caps []int, calls []callFrame, openg []int, atomic []int) {
	snap := make([]int, len(caps))
	copy(snap, caps)
	m.stack = append(m.stack, thread{pc: pc, sp: sp, caps: snap, calls: snapshotCalls(calls), openg: snapshotInts(openg), atomic: snapshotInts(atomic)})
}

// snapshotCalls returns an independent copy of a call stack so a backtrack thread
// keeps its pending-return frames intact while the live stack is mutated. A nil
// or empty stack snapshots to nil, avoiding an allocation for the common
// no-active-call case. The per-frame saved slices are immutable once built (a new
// one is allocated per call), so they are shared rather than deep-copied.
func snapshotCalls(calls []callFrame) []callFrame {
	if len(calls) == 0 {
		return nil
	}
	snap := make([]callFrame, len(calls))
	copy(snap, calls)
	return snap
}

// pushInt appends v to s, copying first so a backtrack-thread snapshot that
// shares s's backing array is not disturbed. It backs the atomic-group mark
// stack, whose entries (backtrack-stack depths) must persist independently in
// each thread that captured the stack before a (?>…) was entered.
func pushInt(s []int, v int) []int {
	nc := make([]int, len(s)+1)
	copy(nc, s)
	nc[len(s)] = v
	return nc
}

// snapshotInts returns an independent copy of an int slice (the open-group stack),
// or nil when it is empty so the common no-open-group case allocates nothing.
func snapshotInts(s []int) []int {
	if len(s) == 0 {
		return nil
	}
	snap := make([]int, len(s))
	copy(snap, s)
	return snap
}

// trackOpen updates the open-group stack for an OpSave at the given slot. An even
// slot (2*index) opens capture group index; the matching odd slot closes it. The
// whole-match group 0 (slots 0 and 1) is never a \g<…> self-recursion target in a
// way that needs restoring beyond what group 0's own OpReturn handles, but it is
// tracked uniformly so a \g<0> recursion restores the outer whole-match span too.
func trackOpen(openg []int, slot int) []int {
	group := slot / 2
	if slot%2 == 0 {
		// Open: push the group, copying first so a backtrack snapshot sharing the
		// backing array is not disturbed.
		nc := make([]int, len(openg)+1)
		copy(nc, openg)
		nc[len(openg)] = group
		return nc
	}
	// Close: pop the matching open. The compiler always pairs an open with its
	// close on the same path, so the top of the stack is this group.
	if len(openg) > 0 {
		return openg[:len(openg)-1]
	}
	return openg
}

// saveOpenSlots records the capture slots (open and close) of every currently
// open group, so OpReturn can restore them after a \g<…> call. It returns nil
// when no group is open, which is the common case (a call made outside any
// capturing group), keeping the hot path allocation-free.
func saveOpenSlots(caps []int, openg []int) []slotVal {
	if len(openg) == 0 {
		return nil
	}
	saved := make([]slotVal, 0, 2*len(openg))
	for _, g := range openg {
		saved = append(saved, slotVal{slot: 2 * g, val: caps[2*g]}, slotVal{slot: 2*g + 1, val: caps[2*g+1]})
	}
	return saved
}

// restoreSlots writes the recorded (slot, value) pairs back into a fresh copy of
// caps, undoing the capture writes a returning \g<…> call made to its enclosing
// groups' slots. When saved is empty caps is returned unchanged.
func restoreSlots(caps []int, saved []slotVal) []int {
	if len(saved) == 0 {
		return caps
	}
	nc := make([]int, len(caps))
	copy(nc, caps)
	for _, sv := range saved {
		nc[sv.slot] = sv.val
	}
	return nc
}

// charMatch reports whether input byte b is accepted by an OpChar instruction.
// OpChar is byte-exact: case-insensitive (/i) matching of a character with a
// Unicode case partner is handled by the rune-aware OpFoldChar instead, so
// OpChar never folds.
func charMatch(in compile.Inst, b byte) bool {
	return b == in.B
}

// foldCharStep reports whether the OpFoldChar instruction in matches the code
// point at position sp and, if so, its byte length. The input code point matches
// when it is in the same simple-case-folding orbit as in.Rune (so /É/i matches
// "é" and /k/i matches the Kelvin sign). Like every rune-aware atom it refuses to
// match at a UTF-8 continuation byte and returns ok=false at end of input.
func (m *machine) foldCharStep(in compile.Inst, sp int) (ok bool, width int) {
	if sp >= len(m.input) {
		return false, 0
	}
	if m.prog.Enc == compile.ASCII8BIT {
		// Binary mode: folding is ASCII-only and per byte. The pattern code point
		// is matched against the single input byte with ASCII case flipping; a
		// non-ASCII pattern code point (or input byte) only matches its exact
		// byte, which a one-byte read of a multi-byte partner never is.
		b := m.input[sp]
		if in.Rune < 128 && (byte(in.Rune) == b || swapASCIICase(byte(in.Rune)) == b) {
			return true, 1
		}
		return false, 0
	}
	if isContinuationByte(m.input[sp]) {
		return false, 0
	}
	r, w := utf8.DecodeRuneInString(m.input[sp:])
	if charset.FoldEqual(in.Rune, r) {
		return true, w
	}
	return false, 0
}

// anyStep reports whether the dot (OpAny) matches at position sp and, if so, how
// many bytes it consumes. In UTF8 mode it consumes a whole code point, so `.`
// matches a multi-byte character as one unit (MRI's behaviour on a UTF-8
// string); in ASCII8BIT mode it consumes a single byte. A newline is excluded
// unless in.DotAll (Ruby's /m): the exclusion tests the leading byte, which is a
// '\n' only for a one-byte code point, so it is identical in both modes. An
// invalid UTF-8 lead byte decodes as utf8.RuneError with width 1, so the scan
// advances one byte rather than stalling (MRI raises on invalid UTF-8; this
// engine is lenient — a documented divergence). It returns ok=false at end of
// input.
func (m *machine) anyStep(in compile.Inst, sp int) (ok bool, width int) {
	if sp >= len(m.input) {
		return false, 0
	}
	if !in.DotAll && m.input[sp] == '\n' {
		return false, 0
	}
	if m.prog.Enc == compile.ASCII8BIT {
		return true, 1
	}
	if isContinuationByte(m.input[sp]) {
		// UTF8 mode: like MRI, which positions only at character boundaries, the
		// dot never starts inside a multi-byte sequence, so a start offset landing
		// on a continuation byte fails and only a code-point-aligned offset matches.
		return false, 0
	}
	_, w := utf8.DecodeRuneInString(m.input[sp:])
	return true, w
}

// classStep reports whether the OpClass instruction in matches at position sp
// and, if so, how many bytes it consumes.
//
// A byte-oriented class (no \p{…} member, no code-point range, not folded) in
// UTF8 mode decodes one code point and tests it against the class's byte ranges
// interpreted as code-point bounds (all ASCII, since they come from byte
// syntax), so a negated class such as [^a] consumes a whole multi-byte character
// while a positive ASCII range such as [a-z] fails on one (its rune value
// exceeds the range) — exactly as MRI behaves on a UTF-8 string. In ASCII8BIT
// mode it tests and consumes a single byte. A rune-aware class — one carrying a
// \p{…} member, a code-point range, or folded under /i — always decodes one
// UTF-8 code point in UTF8 mode and tests a single byte in ASCII8BIT mode. It
// returns ok=false at end of input.
func (m *machine) classStep(in compile.Inst, sp int) (ok bool, width int) {
	if sp >= len(m.input) {
		return false, 0
	}
	runeAware := len(in.Props) != 0 || len(in.RuneRanges) != 0 || in.Fold
	if m.prog.Enc == compile.ASCII8BIT {
		// Binary mode: every class is byte-oriented and consumes one byte. A
		// rune-aware class tests its byte ranges per byte; its code-point members
		// (which require a multi-byte read) can never match a single byte, so a
		// byte is accepted only by the byte ranges, then negated.
		if !runeAware {
			if classMatch(in, m.input[sp]) {
				return true, 1
			}
			return false, 0
		}
		if classMatchByteRanges(in, m.input[sp]) {
			return true, 1
		}
		return false, 0
	}
	if !runeAware {
		// UTF8 mode, byte-oriented class: decode a code point and test it against
		// the byte ranges as code-point bounds, advancing the whole code point.
		if isContinuationByte(m.input[sp]) {
			return false, 0
		}
		r, w := utf8.DecodeRuneInString(m.input[sp:])
		if rangesContainRune(in.Ranges, r) != in.Negate {
			return true, w
		}
		return false, 0
	}
	if isContinuationByte(m.input[sp]) {
		// Mid-code-point: a rune-aware atom never matches off a UTF-8 boundary,
		// so the byte-oriented scan skips past continuation bytes just as MRI,
		// which positions by character, never starts inside one.
		return false, 0
	}
	r, w := utf8.DecodeRuneInString(m.input[sp:])
	if classMatchRune(in, r) {
		return true, w
	}
	return false, 0
}

// classMatchByteRanges reports whether byte b falls in any of a class's byte
// ranges, applying the class's own Negate. It is the ASCII8BIT-mode test for a
// rune-aware class: its code-point members cannot match a single byte, so only
// the byte ranges (then negation) decide.
func classMatchByteRanges(in compile.Inst, b byte) bool {
	return rangesContain(in.Ranges, b) != in.Negate
}

// rangesContainRune reports whether code point r falls in any of the inclusive
// byte ranges, whose bounds (from byte syntax) are ASCII and so are interpreted
// as code points. A multi-byte code point therefore matches only a range whose
// upper bound it does not exceed — never an ASCII-only range.
func rangesContainRune(ranges []ast.ClassRange, r rune) bool {
	for _, rg := range ranges {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return true
		}
	}
	return false
}

// propStep reports whether the OpUniProp instruction in matches the code point
// at position sp and, if so, its byte length. It returns ok=false at end of
// input.
func (m *machine) propStep(in compile.Inst, sp int) (ok bool, width int) {
	if sp >= len(m.input) {
		return false, 0
	}
	if m.prog.Enc == compile.ASCII8BIT {
		// Binary mode: a property is ASCII-only and tests the single input byte,
		// advancing one byte. A high byte (0x80–0xFF) is a lone byte belonging to no
		// property — it must NOT be interpreted as the Latin-1 code point of the
		// same value (e.g. 0xC3 is not a word character in /n, unlike U+00C3), so
		// membership is consulted only for ASCII bytes and the member-local Negate
		// is applied last, exactly as MRI does on an ASCII-8BIT string.
		b := m.input[sp]
		inSet := b < 0x80 && charset.Match(in.Prop.Name, false, rune(b))
		if inSet != in.Prop.Negate {
			return true, 1
		}
		return false, 0
	}
	if isContinuationByte(m.input[sp]) {
		return false, 0
	}
	r, w := utf8.DecodeRuneInString(m.input[sp:])
	if charset.Match(in.Prop.Name, in.Prop.Negate, r) {
		return true, w
	}
	return false, 0
}

// isContinuationByte reports whether b is a UTF-8 continuation byte (0x80–0xBF),
// i.e. an interior byte of a multi-byte code point. A rune-aware atom refuses to
// match at such a position so the byte-oriented scan never starts a code-point
// test mid-character.
func isContinuationByte(b byte) bool { return b&0xc0 == 0x80 }

// classMatch reports whether byte b is accepted by a byte-oriented OpClass
// instruction (one that is neither folded nor carrying a \p{…} member). The
// class's Negate flag is applied after range membership.
func classMatch(in compile.Inst, b byte) bool {
	return rangesContain(in.Ranges, b) != in.Negate
}

// classMatchRune reports whether code point r is accepted by a rune-aware
// OpClass instruction — one carrying a \p{…} member, an explicit code-point
// range, or folded under /i. r is in the positive set if it falls in any byte
// range (whose bounds, from byte syntax, are all ASCII), any code-point range
// (the multi-byte members of a folded class or of \R's linebreak set), or
// satisfies any property member; the class's own Negate is applied last. When the class is folded, range membership uses simple case folding, so
// the input code point matches when it, or any rune in its simple-case-folding
// orbit, lies in the range — making (?i)[a-z] accept the Kelvin sign and
// (?i)[α-ω] accept an uppercase Greek letter.
func classMatchRune(in compile.Inst, r rune) bool {
	inSet := rangeRuneMatch(in, r)
	if !inSet {
		for _, pr := range in.Props {
			if charset.Match(pr.Name, pr.Negate, r) {
				inSet = true
				break
			}
		}
	}
	return inSet != in.Negate
}

// rangeRuneMatch reports whether code point r falls in any of an OpClass
// instruction's byte ranges or code-point ranges, applying simple case folding
// when the class is folded (/i).
func rangeRuneMatch(in compile.Inst, r rune) bool {
	for _, rg := range in.Ranges {
		lo, hi := rune(rg.Lo), rune(rg.Hi)
		if in.Fold {
			if charset.FoldRangeContains(r, lo, hi) {
				return true
			}
		} else if r >= lo && r <= hi {
			return true
		}
	}
	// RuneRanges hold multi-byte code-point members: either a folded class's
	// non-ASCII members (parseFoldRuneMember, under /i) or \R's linebreak set
	// (NEL/LS/PS), which is not folded. Membership uses simple case folding only
	// when the class is folded; otherwise it is a plain inclusive containment.
	for _, rg := range in.RuneRanges {
		if in.Fold {
			if charset.FoldRangeContains(r, rg.Lo, rg.Hi) {
				return true
			}
		} else if r >= rg.Lo && r <= rg.Hi {
			return true
		}
	}
	return false
}

// bytesEqual reports whether the equal-length strings a and b are equal. When
// fold is set the comparison is ASCII case-insensitive (used by a backreference
// under /i). Callers pass slices of identical length (the OpBackref handler
// length-checks before calling), so only the per-byte comparison varies.
func bytesEqual(a, b string, fold bool) bool {
	if !fold {
		return a == b
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] && swapASCIICase(a[i]) != b[i] {
			return false
		}
	}
	return true
}

// rangesContain reports whether byte b falls in any of the inclusive ranges.
func rangesContain(ranges []ast.ClassRange, b byte) bool {
	for _, r := range ranges {
		if b >= r.Lo && b <= r.Hi {
			return true
		}
	}
	return false
}

// swapASCIICase returns b with its ASCII letter case flipped (A-Z <-> a-z); any
// other byte is returned unchanged. It backs the deliberately ASCII-only folding
// of a backreference under /i (literals and classes fold rune-aware via
// unicode.SimpleFold instead).
func swapASCIICase(b byte) byte {
	switch {
	case b >= 'A' && b <= 'Z':
		return b + ('a' - 'A')
	case b >= 'a' && b <= 'z':
		return b - ('a' - 'A')
	default:
		return b
	}
}
