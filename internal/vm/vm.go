// Package vm executes a compiled program against an input using explicit
// backtracking with greedy, leftmost-first semantics (as in Ruby/Onigmo).
package vm

import (
	"errors"
	"strings"
	"time"

	"github.com/go-ruby-regexp/regexp/internal/ast"
	"github.com/go-ruby-regexp/regexp/internal/charset"
	"github.com/go-ruby-regexp/regexp/internal/compile"
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

// trailKind tags an undo-trail entry by which piece of mutable VM state it
// rewinds when the search backtracks.
type trailKind uint8

const (
	// trailCap restores caps[slot] to val (undoes one OpSave / call-return write).
	trailCap trailKind = iota
	// trailOpenPush undoes an open-group push: pop the open-group stack.
	trailOpenPush
	// trailOpenPop undoes an open-group pop: push val back onto the stack.
	trailOpenPop
	// trailAtomicPush undoes an atomic-mark push: pop the atomic-mark stack.
	trailAtomicPush
	// trailAtomicPop undoes an atomic-mark pop: push val back.
	trailAtomicPop
	// trailCallPush undoes a call-frame push: pop the call stack.
	trailCallPush
	// trailCallPop undoes a call-frame pop: push the saved frame back.
	trailCallPop
)

// trailEntry is one reversible mutation recorded so a backtrack can rewind the
// machine's mutable state (captures, open-group / atomic / call stacks) to an
// earlier thread's snapshot. slot/val carry the datum a trailCap / *Pop entry
// restores; frame carries the call-frame a trailCallPop restores. The unified
// trail replaces the old per-thread copy of every snapshot: a thread now stores
// only the trail length at the moment it was forked, and on backtrack the trail
// is replayed in reverse down to that mark.
type trailEntry struct {
	kind  trailKind
	slot  int // trailCap: capture slot; trailOpenPop: group index restored
	val   int // trailCap: previous capture value; *Pop: stack value restored
	frame callFrame
}

// threadKind distinguishes an ordinary backtrack alternative from a fused-loop
// give-back / take-more resume point.
type threadKind uint8

const (
	// threadNormal resumes plain control flow at (pc, sp).
	threadNormal threadKind = iota
	// threadLoop resumes a fused OpLoop's next alternative: the loop recorded the
	// byte boundaries of its run in loopArena[loopBase:], and loopIdx selects the
	// count this resume corresponds to. For a greedy loop successive resumes step
	// to shorter counts; for a lazy loop they step to longer counts. pc is the
	// loop's continuation and sp the boundary position for loopIdx.
	threadLoop
)

// thread is one entry on the backtrack stack: a program counter, an input
// position, and the undo-trail length at the moment this alternative was forked.
// All mutable state (captures, the call stack, the open-group stack, the atomic
// marks) is shared in the machine and rewound via the trail down to trailMark
// when this thread is resumed, so a thread is now allocation-free. A threadLoop
// thread additionally carries the fused-loop resume cursor (loopBase/loopIdx into
// m.loopArena, loopMin the floor count, greedy the direction).
type thread struct {
	pc        int
	sp        int
	trailMark int
	kind      threadKind
	loopBase  int
	loopIdx   int
	loopMin   int
	greedy    bool
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
	// caps is the single mutable capture array, written in place by OpSave and
	// rewound on backtrack via the trail. run() hands back a fresh copy on success
	// so the live array can be reused across start positions.
	caps []int
	// trail records reversible mutations (see trailEntry) so a backtrack can
	// restore caps and the call/open/atomic stacks without per-thread copies.
	trail []trailEntry
	// calls/openg/atomic are the live mutable stacks the trail rewinds. calls holds
	// pending subexpression-call return frames; openg the indices of currently-open
	// capture groups (for call save/restore); atomic the backtrack-stack depths
	// recorded by open atomic (?>…) groups.
	calls  []callFrame
	openg  []int
	atomic []int
	// loopArena is a reused arena holding the byte boundaries of fused-loop runs.
	// A greedy/lazy OpLoop appends its run's boundary positions here and its
	// give-back / take-more backtrack threads index into it (loopBase/loopIdx), so
	// the loop produces no per-invocation allocation.
	loopArena []int
	// memo is the flat (pc, sp) bitset used at OpSplit decision points. It always
	// guards against empty-width loops; when memoize is set (no backreference, no
	// call) it persists across consumed input and becomes full ReDoS memoization,
	// collapsing catastrophic backtracking from exponential to polynomial while
	// preserving leftmost-first semantics (a second arrival at the same (pc, sp)
	// has an identical future). When memoize is false a backreference can read
	// captured text, so the future is not a pure function of (pc, sp); the bitset
	// is then cleared on every consumed byte and only its empty-loop role remains.
	memo memoGen
	// memoize enables the persistent (pc, sp) memo. It is the program's
	// no-backreference, no-call property, hoisted here for the hot loop.
	memoize bool
}

// maxDenseMemoCells bounds the (pc × position) table size for which the memo uses
// a dense []uint32 stamp array. The dense form is the fastest possible memo — a
// flat array index and compare — but it must allocate nPC*(inputLen+1) cells up
// front, which for a large haystack is wasteful when only a few positions are
// actually visited (e.g. an anchored `\A\w+` that matches a short word and stops,
// or any split pattern that hits early). Above this cap the memo falls back to a
// generation-stamped map that grows only to the positions truly touched. The cap
// (~256 K cells = 1 MB) comfortably covers every catastrophic-backtracking case,
// whose inputs are short (the canonical ReDoS strings are tens of bytes), so the
// ReDoS-defusing memo always stays on the fast dense path; only split patterns
// over large haystacks — where the visited-position set is far smaller than the
// full table — take the sparse path and so skip the multi-megabyte allocation.
const maxDenseMemoCells = 1 << 18

// memoGen is a generation-stamped memo over (pc, sp) decision points, replacing
// the old map[int64]bool. A cell is "marked" iff its stored stamp equals the
// current generation cur, so clearing the whole set is O(1): bumping cur
// invalidates every stamp at once. That makes the per-start and per-consumed-byte
// resets free — a length-of-input dense bitset zeroed in full would turn a long
// no-split scan into O(n²); the generation bump avoids it.
//
// It has two backings chosen by table size at init (see maxDenseMemoCells): a
// dense []uint32 stamp array (fastest: a flat index and compare) for small tables,
// or a generation-stamped map[int64]uint32 that grows only to the visited
// positions for large haystacks, so a split pattern over a big input does not pay
// a multi-megabyte up-front allocation. Either way it preserves the exact
// linear-time ReDoS guarantee of the old memo: a (pc, sp) reached a second time
// within the memo's validity window (one whole attempt when memoization is on,
// since the last consumed byte when off) is pruned identically.
type memoGen struct {
	gen    []uint32         // dense backing (nil when the sparse map is used)
	sparse map[int64]uint32 // sparse backing (nil when the dense array is used)
	cur    uint32
	stride int  // len(input)+1
	active bool // false when the program has no OpSplit, so the memo is never used
}

// init prepares the memo for nPC instructions over an input of length inputLen
// when hasSplit is true; otherwise it leaves the memo inactive and allocates
// nothing (a split-free program never reaches test/set). It picks the dense or
// sparse backing by table size and reuses the existing backing across calls when
// possible.
func (b *memoGen) init(nPC, inputLen int, hasSplit bool) {
	b.active = hasSplit
	if !hasSplit {
		return
	}
	b.stride = inputLen + 1
	n := nPC * b.stride
	if n <= maxDenseMemoCells {
		b.sparse = nil
		if cap(b.gen) >= n {
			b.gen = b.gen[:n]
		} else {
			b.gen = make([]uint32, n)
			b.cur = 0
		}
	} else {
		// Large table: a sparse map that grows only to visited positions, avoiding
		// the multi-megabyte dense allocation. Reuse an existing map; the generation
		// stamp lets stale entries expire without clearing it.
		b.gen = nil
		if b.sparse == nil {
			b.sparse = make(map[int64]uint32)
			b.cur = 0
		}
	}
	// Start at generation 1 so a freshly made (all-zero) dense slice has no cell
	// accidentally matching the current generation.
	b.bump()
}

// bump advances to a fresh generation, invalidating every previously-set stamp in
// O(1). On the rare wraparound back to 0 it resets the backing (zeroing the dense
// slice or recreating the map) and restarts at 1, so a stale stamp can never
// collide with the live generation.
func (b *memoGen) bump() {
	b.cur++
	if b.cur == 0 {
		if b.gen != nil {
			clear(b.gen)
		} else {
			clear(b.sparse)
		}
		b.cur = 1
	}
}

// clearAll forgets every marked (pc, sp) in O(1) (used per start position, and on
// every consumed byte when memoization is disabled so the set only guards
// empty-width loops since the last advance).
func (b *memoGen) clearAll() {
	if b.active {
		b.bump()
	}
}

// test reports whether (pc, sp) is marked in the current generation.
func (b *memoGen) test(pc, sp int) bool {
	if b.gen != nil {
		return b.gen[pc*b.stride+sp] == b.cur
	}
	return b.sparse[int64(pc)*int64(b.stride)+int64(sp)] == b.cur
}

// set marks (pc, sp) in the current generation.
func (b *memoGen) set(pc, sp int) {
	if b.gen != nil {
		b.gen[pc*b.stride+sp] = b.cur
		return
	}
	b.sparse[int64(pc)*int64(b.stride)+int64(sp)] = b.cur
}

// Match runs prog against input, scanning start positions left to right until a
// match is found. It returns the capture slots (len == prog.NumSlots), whether
// a match was found, and an error only when the step budget is exhausted. It
// imposes no wall-clock limit; use MatchTimeout for that.
func Match(prog *compile.Program, input string, budget int) ([]int, bool, error) {
	return MatchTimeout(prog, input, budget, time.Time{})
}

// MatchAt attempts a match anchored exactly at byte offset pos, with \G bound to
// pos, while the whole input string remains visible so the line/text anchors
// (^ \A) and lookbehind see the real prefix input[:pos]. Unlike Match it does
// not scan forward: it either matches at pos or fails. This is the primitive a
// StringScanner-style tokenizer (e.g. a Rouge RegexLexer) needs so that a
// pattern's ^ matches only at a true line start and \G pins to the cursor. It
// returns the capture slots, whether a match occurred, and an error only when
// the step budget is exhausted.
func MatchAt(prog *compile.Program, input string, pos, budget int) ([]int, bool, error) {
	return MatchTimeoutAt(prog, input, pos, budget, time.Time{})
}

// MatchTimeoutAt is MatchAt with a wall-clock deadline, mirroring the
// MatchTimeout / Match relationship.
func MatchTimeoutAt(prog *compile.Program, input string, pos, budget int, deadline time.Time) ([]int, bool, error) {
	m := &machine{
		prog:     prog,
		input:    input,
		budget:   budget,
		deadline: deadline,
		memoize:  !prog.HasBackref && !prog.HasCall,
	}
	m.memo.init(len(prog.Insts), len(input), prog.HasSplit)
	m.gpos = pos
	m.caps = make([]int, prog.NumSlots())
	return m.run(pos)
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
		// The persistent (pc, sp) memo is sound only when the future is a pure
		// function of (pc, sp). A backreference reads captured text, and a
		// subexpression call (\g<…>) re-runs/re-captures a group and carries its own
		// recursion state, so either makes two arrivals at the same (pc, sp) differ;
		// memoization is then disabled and the step budget bounds the work.
		memoize: !prog.HasBackref && !prog.HasCall,
	}
	m.memo.init(len(prog.Insts), len(input), prog.HasSplit)
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
	m.caps = make([]int, prog.NumSlots())
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
		result, ok, err := m.run(start)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return result, true, nil
		}
	}
	return nil, false, nil
}

// run attempts a match anchored at position start. On success it returns a fresh
// copy of the final capture slots.
func (m *machine) run(start int) ([]int, bool, error) {
	m.stack = m.stack[:0]
	m.trail = m.trail[:0]
	m.calls = m.calls[:0]
	m.openg = m.openg[:0]
	m.atomic = m.atomic[:0]
	m.loopArena = m.loopArena[:0]
	for i := range m.caps {
		m.caps[i] = -1
	}
	// A fresh search starts from an empty memo. Each start position is an
	// independent attempt; a (pc, sp) that failed from an earlier start could in
	// principle be re-reached, but the memo is reset per start to keep its meaning
	// simple (failure of this whole attempt) and bounded.
	m.memo.clearAll()
	pc := 0
	sp := start
	for {
		if err := m.tick(); err != nil {
			return nil, false, err
		}

		in := m.prog.Insts[pc]
		switch in.Op {
		case compile.OpChar:
			if sp < len(m.input) && m.input[sp] == in.B {
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
			if m.memo.test(pc, sp) {
				// Already explored this split at this position without
				// progress; do not re-enter the body. Jump to the split's exit
				// branch (GuardTo) instead of looping — for a lazy loop the exit is
				// X, not Y, so a fixed "go to Y" would spin the empty body.
				pc = in.GuardTo
				continue
			}
			m.memo.set(pc, sp)
			m.push(in.Y, sp)
			pc = in.X
			continue
		case compile.OpJmp:
			pc = in.X
			continue
		case compile.OpSave:
			m.saveSlot(in.Slot, sp)
			m.trackOpen(in.Slot)
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
		case compile.OpAssertWordBoundary:
			if wordBoundaryAt(m.input, m.prog.Enc, sp) {
				pc++
				continue
			}
		case compile.OpAssertNonWordBoundary:
			if !wordBoundaryAt(m.input, m.prog.Enc, sp) {
				pc++
				continue
			}
		case compile.OpLook:
			matched, err := m.look(pc, in, sp)
			if err != nil {
				return nil, false, err
			}
			if matched != in.Negate {
				// Positive look that matched, or negative look that did not:
				// the assertion holds. (Positive lookaround's inner captures are
				// written through m.caps by the sub-search and rewound by the trail if
				// the outer search later backtracks past this point.)
				pc = in.X
				continue
			}
		case compile.OpBackref:
			bgn, end := m.caps[2*in.Slot], m.caps[2*in.Slot+1]
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
			if len(m.calls) >= MaxCallDepth {
				break
			}
			m.pushCall(callFrame{group: in.Slot, ret: pc + 1, saved: saveOpenSlots(m.caps, m.openg)})
			pc = in.X
			continue
		case compile.OpReturn:
			if n := len(m.calls); n > 0 && m.calls[n-1].group == in.Slot {
				// This is the terminator of the group the active call targets:
				// return to the caller, restoring the open-group captures the call
				// may have overwritten.
				f := m.popCall()
				m.restoreSlots(f.saved)
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
			m.pushAtomic(len(m.stack))
			pc++
			continue
		case compile.OpAtomicEnd:
			// The atomic body matched: discard every backtrack point it created,
			// committing this sub-match (no shorter repetition / alternate is ever
			// retried). The matching OpAtomicBegin is always the most recent mark on
			// this path, so it is the top of the atomic stack.
			mark := m.atomic[len(m.atomic)-1]
			m.popAtomic()
			m.stack = m.stack[:mark]
			pc++
			continue
		case compile.OpLoop:
			npc, nsp, ok, err := m.loopEnter(pc, in, sp)
			if err != nil {
				return nil, false, err
			}
			if ok {
				if nsp != sp {
					m.consumed()
				}
				pc = npc
				sp = nsp
				continue
			}
			// The loop could not even match its required minimum: fall through to
			// backtrack.
		case compile.OpMatch:
			out := make([]int, len(m.caps))
			copy(out, m.caps)
			return out, true, nil
		}

		// Failure: backtrack to the most recent usable alternative, if any. A
		// threadLoop give-back/take-more may be exhausted, in which case we keep
		// popping until we find a live alternative or the stack empties.
		npc, nsp, ok := m.backtrack()
		if !ok {
			return nil, false, nil
		}
		pc = npc
		sp = nsp
	}
}

// backtrack pops backtrack alternatives until one yields a live resume point,
// rewinding the trail to each popped thread's mark. A normal thread resumes
// directly at its (pc, sp); a threadLoop thread asks the fused loop for its next
// give-back / take-more count, and is skipped when that loop is exhausted. It
// returns the resume (pc, sp) and true, or ok=false when no alternative remains.
func (m *machine) backtrack() (int, int, bool) {
	for len(m.stack) > 0 {
		t := m.stack[len(m.stack)-1]
		m.stack = m.stack[:len(m.stack)-1]
		m.rewind(t.trailMark)
		if t.kind == threadLoop {
			npc, nsp, ok := m.loopResume(t)
			if !ok {
				continue
			}
			return npc, nsp, true
		}
		return t.pc, t.sp, true
	}
	return 0, 0, false
}

// loopEnter executes a fused OpLoop at pc starting at sp. It scans the maximal
// run of the repeated atom (up to Max), recording each boundary in m.loopArena.
// If fewer than Min atoms match it returns ok=false (the loop fails and the
// caller backtracks). Otherwise it picks the leftmost-first preferred count
// (greedy: the longest; lazy: Min), pushes one threadLoop alternative carrying the
// run so backtracking can step to the next count, and returns the continuation pc
// and the chosen end position. Each atom scanned costs one step of the
// deterministic budget (via tick), exactly as the unfused per-atom form did, so
// the fused loop cannot evade the step/time bound on a long run; a budget or
// deadline exhaustion mid-scan returns a non-nil error.
func (m *machine) loopEnter(pc int, in compile.Inst, sp int) (int, int, bool, error) {
	// Record boundary positions of the run: bounds[0] = sp (zero atoms),
	// bounds[k] = position after k atoms. The loop body is a single consuming atom,
	// so each step advances by at least one byte and the run is finite.
	base := len(m.loopArena)
	m.loopArena = append(m.loopArena, sp)
	count := 0
	pos := sp
	for in.Max < 0 || count < in.Max {
		// Charge one step per atom examined so the budget keeps bounding total work
		// (the fused loop must be no cheaper, budget-wise, than the per-OpChar form).
		if err := m.tick(); err != nil {
			m.loopArena = m.loopArena[:base]
			return 0, 0, false, err
		}
		ok, w := m.loopAtomStep(in, pos)
		if !ok {
			break
		}
		pos += w
		count++
		m.loopArena = append(m.loopArena, pos)
	}
	if count < in.Min {
		// Not enough repetitions; discard the recorded run and fail.
		m.loopArena = m.loopArena[:base]
		return 0, 0, false, nil
	}
	cont := in.X
	if in.Greedy {
		// Greedy: take the longest run now; on backtrack give back toward Min.
		t := thread{pc: cont, trailMark: len(m.trail), kind: threadLoop,
			loopBase: base, loopIdx: count, loopMin: in.Min, greedy: true}
		// The current (longest) count is consumed by this very step; the pushed
		// thread's next alternative is count-1, so record loopIdx = count and let
		// loopResume pre-decrement.
		m.stack = append(m.stack, t)
		return cont, m.loopArena[base+count], true, nil
	}
	// Lazy: take the minimum now; on backtrack take one more toward the longest.
	t := thread{pc: cont, trailMark: len(m.trail), kind: threadLoop,
		loopBase: base, loopIdx: in.Min, loopMin: count, greedy: false}
	m.stack = append(m.stack, t)
	return cont, m.loopArena[base+in.Min], true, nil
}

// loopResume advances a threadLoop alternative to its next count and returns the
// continuation pc and end position, or ok=false when the loop is exhausted (the
// greedy give-back has reached Min, or the lazy take-more has reached the longest
// run). A greedy loop steps to a shorter count, a lazy loop to a longer one.
func (m *machine) loopResume(t thread) (int, int, bool) {
	if t.greedy {
		next := t.loopIdx - 1
		if next < t.loopMin {
			return 0, 0, false
		}
		// Re-push so a further backtrack can give back again.
		nt := t
		nt.loopIdx = next
		m.stack = append(m.stack, nt)
		return t.pc, m.loopArena[t.loopBase+next], true
	}
	next := t.loopIdx + 1
	if next > t.loopMin {
		// loopMin holds the maximal matched count for a lazy loop.
		return 0, 0, false
	}
	nt := t
	nt.loopIdx = next
	m.stack = append(m.stack, nt)
	return t.pc, m.loopArena[t.loopBase+next], true
}

// loopAtomStep matches the single repeated atom of a fused OpLoop at position sp,
// reporting success and the byte width consumed. It dispatches on in.Sub (the
// atom's real opcode) to the same per-atom step the unfused VM uses, so the fused
// loop accepts exactly the characters the unfused atom would.
func (m *machine) loopAtomStep(in compile.Inst, sp int) (bool, int) {
	switch in.Sub {
	case compile.OpChar:
		if sp < len(m.input) && m.input[sp] == in.B {
			return true, 1
		}
		return false, 0
	case compile.OpFoldChar:
		return m.foldCharStep(in, sp)
	case compile.OpAny:
		return m.anyStep(in, sp)
	case compile.OpClass:
		return m.classStep(in, sp)
	case compile.OpUniProp:
		return m.propStep(in, sp)
	}
	return false, 0
}

// rewind replays the undo trail in reverse down to mark, restoring caps and the
// call/open/atomic stacks to the state captured when the resumed thread was
// forked.
func (m *machine) rewind(mark int) {
	for i := len(m.trail) - 1; i >= mark; i-- {
		e := m.trail[i]
		switch e.kind {
		case trailCap:
			m.caps[e.slot] = e.val
		case trailOpenPush:
			m.openg = m.openg[:len(m.openg)-1]
		case trailOpenPop:
			m.openg = append(m.openg, e.val)
		case trailAtomicPush:
			m.atomic = m.atomic[:len(m.atomic)-1]
		case trailAtomicPop:
			m.atomic = append(m.atomic, e.val)
		case trailCallPush:
			m.calls = m.calls[:len(m.calls)-1]
		case trailCallPop:
			m.calls = append(m.calls, e.frame)
		}
	}
	m.trail = m.trail[:mark]
}

// look evaluates a lookaround assertion whose OpLook is at lookPC. It reports
// whether the sub-pattern matched; the outer position is never advanced. A
// successful positive lookaround leaves its inner captures written through
// m.caps (trail-recorded, so they unwind with the outer search); a failed or
// negative one leaves caps as it found them.
//
// For lookahead the sub-program is run anchored at sp. For lookbehind it is run
// from each candidate start position sp-w (w in [Min,Max], widest first to
// match Ruby's greedy preference), requiring the run to end exactly at sp.
func (m *machine) look(lookPC int, in compile.Inst, sp int) (bool, error) {
	body := lookPC + 1
	// Only a POSITIVE lookaround (ahead or behind) exposes its inner captures to
	// the rest of the pattern; a negative one asserts non-existence and must leave
	// caps untouched. keep tells execLook whether to retain the sub-search's
	// capture writes on success or rewind them.
	keep := !in.Negate
	if !in.Behind {
		return m.execLook(body, sp, -1, keep)
	}
	for w := in.Max; w >= in.Min; w-- {
		start := sp - w
		if start < 0 {
			continue
		}
		ok, err := m.execLook(body, start, sp, keep)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// execLook runs a lookaround sub-program from pc=body at position sp using an
// isolated backtrack stack and empty-loop guard, so it cannot disturb the outer
// search's control flow. endAt is the position the run must reach at OpLookEnd
// (-1 means any, used by lookahead; lookbehind passes the outer position so the
// sub-pattern must consume exactly the right number of bytes). Capture writes go
// through the shared m.caps and the shared trail: on success they persist (a
// positive lookaround exposes its captures); on failure they are rewound here so
// the outer search sees no change.
func (m *machine) execLook(body, sp, endAt int, keep bool) (bool, error) {
	// A lookaround sub-search runs against an isolated backtrack stack and memo so
	// it cannot disturb the outer search, but it shares m.caps / m.trail so a
	// positive lookaround's captures can survive into the outer match. The outer
	// trail mark lets us rewind every capture write on sub-search failure (and on
	// success when keep is false, i.e. a negative lookaround that internally
	// matched: it must leave no capture trace).
	savedStack := m.stack
	savedTrailLen := len(m.trail)
	savedCallLen := len(m.calls)
	savedOpenLen := len(m.openg)
	savedAtomicLen := len(m.atomic)
	m.stack = nil
	var subMemo memoGen
	subMemo.init(len(m.prog.Insts), len(m.input), m.prog.HasSplit)
	pc := body
	fail := func() {
		// Restore the outer search's backtrack stack and rewind any capture / stack
		// mutations the sub-search made.
		m.stack = savedStack
		m.rewind(savedTrailLen)
		m.calls = m.calls[:savedCallLen]
		m.openg = m.openg[:savedOpenLen]
		m.atomic = m.atomic[:savedAtomicLen]
	}
	for {
		if err := m.tick(); err != nil {
			m.stack = savedStack
			return false, err
		}

		in := m.prog.Insts[pc]
		switch in.Op {
		case compile.OpChar:
			if sp < len(m.input) && m.input[sp] == in.B {
				pc++
				sp++
				subMemo.clearAll()
				continue
			}
		case compile.OpFoldChar:
			if ok, w := m.foldCharStep(in, sp); ok {
				pc++
				sp += w
				subMemo.clearAll()
				continue
			}
		case compile.OpAny:
			if ok, w := m.anyStep(in, sp); ok {
				pc++
				sp += w
				subMemo.clearAll()
				continue
			}
		case compile.OpClass:
			if ok, w := m.classStep(in, sp); ok {
				pc++
				sp += w
				subMemo.clearAll()
				continue
			}
		case compile.OpUniProp:
			if ok, w := m.propStep(in, sp); ok {
				pc++
				sp += w
				subMemo.clearAll()
				continue
			}
		case compile.OpSplit:
			if subMemo.test(pc, sp) {
				pc = in.GuardTo
				continue
			}
			subMemo.set(pc, sp)
			m.push(in.Y, sp)
			pc = in.X
			continue
		case compile.OpJmp:
			pc = in.X
			continue
		case compile.OpAtomicBegin:
			// Atomic (?>…) inside a lookaround body: same mechanism, scoped to this
			// sub-search's own backtrack stack.
			m.pushAtomic(len(m.stack))
			pc++
			continue
		case compile.OpAtomicEnd:
			mark := m.atomic[len(m.atomic)-1]
			m.popAtomic()
			m.stack = m.stack[:mark]
			pc++
			continue
		case compile.OpCall:
			// A subexpression call inside a lookaround body, with the same
			// recursion-depth cap and open-group save/restore as the main search.
			if len(m.calls) >= MaxCallDepth {
				break
			}
			m.pushCall(callFrame{group: in.Slot, ret: pc + 1, saved: saveOpenSlots(m.caps, m.openg)})
			pc = in.X
			continue
		case compile.OpReturn:
			if n := len(m.calls); n > 0 && m.calls[n-1].group == in.Slot {
				f := m.popCall()
				m.restoreSlots(f.saved)
				pc = f.ret
				continue
			}
			pc++
			continue
		case compile.OpSave:
			m.saveSlot(in.Slot, sp)
			m.trackOpen(in.Slot)
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
		case compile.OpAssertWordBoundary:
			if wordBoundaryAt(m.input, m.prog.Enc, sp) {
				pc++
				continue
			}
		case compile.OpAssertNonWordBoundary:
			if !wordBoundaryAt(m.input, m.prog.Enc, sp) {
				pc++
				continue
			}
		case compile.OpLook:
			matched, err := m.look(pc, in, sp)
			if err != nil {
				m.stack = savedStack
				return false, err
			}
			if matched != in.Negate {
				pc = in.X
				continue
			}
		case compile.OpBackref:
			bgn, end := m.caps[2*in.Slot], m.caps[2*in.Slot+1]
			if bgn < 0 || end < 0 {
				pc++
				continue
			}
			ref := m.input[bgn:end]
			if sp+len(ref) <= len(m.input) && bytesEqual(m.input[sp:sp+len(ref)], ref, in.Fold) {
				pc++
				sp += len(ref)
				if len(ref) > 0 {
					subMemo.clearAll()
				}
				continue
			}
		case compile.OpLoop:
			npc, nsp, ok, err := m.loopEnter(pc, in, sp)
			if err != nil {
				// Budget/deadline exhausted while scanning the loop's run: restore the
				// outer search's stack and unwind any mutations, then propagate.
				fail()
				return false, err
			}
			if ok {
				if nsp != sp {
					subMemo.clearAll()
				}
				pc = npc
				sp = nsp
				continue
			}
		case compile.OpLookEnd:
			if endAt < 0 || sp == endAt {
				// Success. Discard the sub-search's own backtrack alternatives by
				// restoring the outer stack. For a positive lookaround (keep) the
				// capture writes belong to the matched assertion and persist on the
				// trail; for a negative one that internally matched (keep false) the
				// assertion will FAIL at the caller, and it must leave no capture
				// trace, so rewind the trail and restore the stacks now.
				if keep {
					m.stack = savedStack
				} else {
					fail()
				}
				return true, nil
			}
		}

		// Failure: backtrack within this sub-search only (its own m.stack), honoring
		// fused-loop give-back/take-more just like the main search.
		npc, nsp, ok := m.backtrack()
		if !ok {
			fail()
			return false, nil
		}
		pc = npc
		sp = nsp
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
		m.memo.clearAll()
	}
}

// saveSlot records the previous value of caps[slot] on the trail, then writes
// pos in place. The trail entry lets a backtrack restore the old value without
// the per-save array copy the engine used before.
func (m *machine) saveSlot(slot, pos int) {
	m.trail = append(m.trail, trailEntry{kind: trailCap, slot: slot, val: m.caps[slot]})
	m.caps[slot] = pos
}

// push records a backtrack alternative: the current trail length is its rewind
// mark, so resuming it replays the trail back to here.
func (m *machine) push(pc, sp int) {
	m.stack = append(m.stack, thread{pc: pc, sp: sp, trailMark: len(m.trail)})
}

// pushCall pushes a pending subexpression-call frame and trails the push so a
// backtrack pops it.
func (m *machine) pushCall(f callFrame) {
	m.calls = append(m.calls, f)
	m.trail = append(m.trail, trailEntry{kind: trailCallPush})
}

// popCall pops the top call frame and trails the pop so a backtrack restores it.
func (m *machine) popCall() callFrame {
	n := len(m.calls) - 1
	f := m.calls[n]
	m.calls = m.calls[:n]
	m.trail = append(m.trail, trailEntry{kind: trailCallPop, frame: f})
	return f
}

// pushAtomic pushes an atomic-group mark (a backtrack-stack depth) and trails it.
func (m *machine) pushAtomic(v int) {
	m.atomic = append(m.atomic, v)
	m.trail = append(m.trail, trailEntry{kind: trailAtomicPush})
}

// popAtomic pops the top atomic mark and trails the pop.
func (m *machine) popAtomic() {
	v := m.atomic[len(m.atomic)-1]
	m.atomic = m.atomic[:len(m.atomic)-1]
	m.trail = append(m.trail, trailEntry{kind: trailAtomicPop, val: v})
}

// trackOpen updates the open-group stack for an OpSave at the given slot,
// trailing each push/pop so a backtrack restores it. An even slot (2*index)
// opens capture group index; the matching odd slot closes it.
func (m *machine) trackOpen(slot int) {
	group := slot / 2
	if slot%2 == 0 {
		m.openg = append(m.openg, group)
		m.trail = append(m.trail, trailEntry{kind: trailOpenPush})
		return
	}
	// Close: pop the matching open. The compiler always pairs an open with its
	// close on the same path, so the top of the stack is this group.
	if len(m.openg) > 0 {
		v := m.openg[len(m.openg)-1]
		m.openg = m.openg[:len(m.openg)-1]
		m.trail = append(m.trail, trailEntry{kind: trailOpenPop, val: v})
	}
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

// restoreSlots writes the recorded (slot, value) pairs back into m.caps in
// place, trailing each write so a later backtrack rewinds it, undoing the
// capture writes a returning \g<…> call made to its enclosing groups' slots.
func (m *machine) restoreSlots(saved []slotVal) {
	for _, sv := range saved {
		m.saveSlot(sv.slot, sv.val)
	}
}

// foldCharStep reports whether the OpFoldChar instruction in matches the code
// point at position sp and, if so, its byte length. The input code point matches
// when it is in the same simple-case-folding orbit as in.Rune (so /É/i matches
// "é" and /k/i matches the Kelvin sign). Like every rune-aware atom it refuses to
// match at a UTF-8 continuation byte and returns ok=false at end of input.
func (m *machine) foldCharStep(in compile.Inst, sp int) (ok bool, width int) {
	return foldCharStepCtx(m.ctx(), in, sp)
}

// ctx packages the machine's read-only matching context (input, encoding, \G
// origin) so the atom acceptance tests can be shared verbatim with the DFA
// executor; both call the same foldCharStepCtx / anyStepCtx / classStepCtx /
// propStepCtx, guaranteeing the DFA accepts exactly the bytes the VM does.
func (m *machine) ctx() dfaCtx {
	return dfaCtx{input: m.input, enc: m.prog.Enc, gpos: m.gpos}
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
	return anyStepCtx(m.ctx(), in, sp)
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
	return classStepCtx(m.ctx(), in, sp)
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
	return propStepCtx(m.ctx(), in, sp)
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
	// RuneRanges hold multi-byte code-point members: a literal multi-byte member
	// or range in UTF8 mode (e.g. [é] or [à-ï]), a folded class's non-ASCII
	// members (under /i), or \R's linebreak set (NEL/LS/PS), which is not folded.
	// Membership uses simple case folding only when the class is folded;
	// otherwise it is a plain inclusive containment.
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
