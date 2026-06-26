package vm

import (
	"sync/atomic"

	"github.com/go-ruby-regexp/regexp/internal/compile"
)

// This file is the cached-DFA executor: a forward scan that drives the memoized
// transition table in dfa_cache.go, falling back to the per-step NFA simulation in
// dfa_run.go for the positions the cache cannot key (a multi-byte UTF-8 lead byte,
// or a frontier whose epsilon-closure crosses a position-dependent assertion). It
// produces the identical leftmost-first [begin, end) span the per-step simulation
// (and the backtracking VM) produces, but in steady state spends roughly one table
// lookup plus one begin-gather per input byte instead of an epsilon-closure walk.
//
// The scan mirrors dfaSim.search exactly. clist is replaced by an interned DFA
// state (its priority-ordered frontier) plus a parallel begins slice carrying each
// frontier node's match-start offset. "Hunting" means no match has begun, so a
// fresh lowest-priority start thread is seeded each step (the transHunt row);
// "committed" means a match begin is fixed, so no new start is seeded (the trans
// row). A state flagged matchHere accepts at the current position: the highest
// -priority such arrival fixes the end and the lower-priority threads are cut off
// (leftmost-first), exactly as the simulation's "first nfaMatch wins and breaks".

// classCtx is the read-only context used to step a representative byte of a byte
// class when computing a transition on a cache miss. The representative is always an
// ASCII byte (< 0x80) — its own complete one-byte code point in both UTF8 and
// ASCII8BIT — but a rune-aware atom (a fold class, a Unicode property) decides
// differently under the two encodings even on an ASCII byte, so the program's own
// encoding is used so the cached transition matches the real search exactly.
func (c *dfaCache) classCtx(cl uint8) dfaCtx {
	return dfaCtx{input: c.classRep[cl], enc: c.enc}
}

// stepCached returns the transition out of state s on byte class cl, in hunting or
// committed mode, computing and caching it on first sight. next is the successor
// state, src its per-node begin-source map, and matchSrc the begin-source of next's
// accepting arrival (used only when next.matchHere is set). On a closure that cannot
// be memoized (it crosses a position-dependent assertion) it returns next=nil with
// uncacheable=true, and the caller falls back to the per-step simulation for this
// position.
// stepRaceHook is a test-only seam: when non-nil it runs inside stepCached after the
// cache lock is taken on a miss but before the double-check load, so a test can
// deterministically reproduce the "a racing goroutine filled this slot first" branch.
// It is nil in production.
var stepRaceHook func(slot *atomic.Pointer[transEntry])

func (c *dfaCache) stepCached(s *dfaCacheState, cl uint8, hunting bool) (next *dfaCacheState, src []int32, matchSrc int32, uncacheable bool) {
	row := s.trans
	if hunting {
		row = s.transHunt
	}
	// Steady-state hit: a previously computed entry is published atomically, so it is
	// read without the cache lock — this is the hot path the lever optimises.
	if e := row[cl].Load(); e != nil {
		return e.next, e.src, e.matchSrc, e.uncacheable
	}
	// Miss: compute under the lock, then double-check in case a concurrent search
	// filled the slot first (publishing whichever entry wins; both are equivalent
	// since a transition is a pure function of (state, class, hunting)).
	c.mu.Lock()
	if stepRaceHook != nil {
		// Test seam: deterministically reproduce the concurrent-fill race by letting a
		// test publish the slot after the lock is held but before the double-check load.
		stepRaceHook(&row[cl])
	}
	if e := row[cl].Load(); e != nil {
		c.mu.Unlock()
		return e.next, e.src, e.matchSrc, e.uncacheable
	}
	nx, sr, ms, ok := c.computeTrans(s, cl, hunting)
	e := &transEntry{next: nx, src: sr, matchSrc: ms, uncacheable: !ok}
	row[cl].Store(e)
	c.mu.Unlock()
	return e.next, e.src, e.matchSrc, e.uncacheable
}

// computeTrans steps every consuming node of s with class cl's representative byte
// and forms the successor frontier (the epsilon-closure of the successors, taken
// from the precomputed ctxFree closures) plus the begin-source map and, when the
// successor accepts, the begin-source of the accepting arrival. It returns ok=false
// when any successor — or the seeded start, when hunting — has a context-dependent
// closure that cannot be cached. The caller holds c.mu.
func (c *dfaCache) computeTrans(s *dfaCacheState, cl uint8, hunting bool) (next *dfaCacheState, src []int32, matchSrc int32, ok bool) {
	nfa := c.nfa
	ctx := c.classCtx(cl)
	n := len(nfa.insts)
	visited := make([]bool, n)
	var outNodes []int32
	matchHere := false
	matchSrc = -1

	// addClosure appends the ctxFree epsilon-closure of node to the successor
	// frontier, deduplicating (keeping the highest-priority arrival) and recording
	// srcIdx as the index into s.nodes whose begin each new node inherits (-1 for a
	// freshly seeded start). When the closure reaches the accepting node it records the
	// match (begin-source = srcIdx) and truncates the frontier (leftmost-first). It
	// returns false if the closure is context-dependent.
	addClosure := func(node int32, srcIdx int32) bool {
		if !nfa.ctxFree[node] {
			return false
		}
		for _, cnode := range nfa.closure[node] {
			if visited[cnode] {
				continue
			}
			visited[cnode] = true
			if nfa.insts[cnode].op == nfaMatch {
				matchHere = true
				matchSrc = srcIdx
				return true // a match truncates the frontier (leftmost-first)
			}
			outNodes = append(outNodes, cnode)
			src = append(src, srcIdx)
		}
		return true
	}

	for i, node := range s.nodes {
		if matchHere {
			break
		}
		in := nfa.insts[node].inst
		stepOK, w := atomStepCtx(ctx, in, 0)
		if !stepOK || w != 1 {
			continue
		}
		if !addClosure(int32(nfa.insts[node].x), int32(i)) {
			return nil, nil, 0, false
		}
	}
	if hunting && !matchHere {
		if !addClosure(int32(nfa.start), -1) {
			return nil, nil, 0, false
		}
	}
	st := c.internState(outNodes, matchHere)
	return st, src, matchSrc, true
}

// dfaCacheSim is one cached-DFA search's executor state. It drives the memoized
// transition table for the width-1 ASCII positions that dominate the inner loop and
// borrows a dfaSim (sharing its thread pool, atom tests, and prefilter) for the
// positions the cache cannot key — a multi-byte UTF-8 lead byte, or a transition
// whose epsilon-closure crosses a position-dependent assertion. The two paths
// produce byte-identical leftmost-first bounds.
type dfaCacheSim struct {
	c   *dfaCache
	sim *dfaSim // shares the thread pool / prefilter; used for fallback positions
	// bufA / bufB are two reusable begin-offset buffers the hot cached loop ping-pongs
	// between: each width-1 step gathers the next frontier's begins into the spare
	// buffer and swaps, so the steady-state inner loop performs no per-byte allocation
	// (the begins slice is the only per-position data the cached transition carries).
	bufA, bufB []int32
	// fbNodes / fbBegins / fbSeeds are reusable scratch the fallback path (multi-byte
	// and assertion-crossing positions) refills each call instead of allocating, so a
	// dot-over-UTF-8 scan that falls back at every multi-byte rune stays allocation
	// -light. They are length-reset (not reallocated) per use.
	fbNodes  []int32
	fbBegins []int32
	fbSeeds  []dfaThread
}

// closeFrontier runs the position-aware epsilon-closure of every (node, begin) seed
// at position sp into the borrowed thread list and converts the closed, priority
// -ordered result into an interned cached state: the consuming nodes before the
// first accepting arrival become the frontier (with their begins), the first
// accepting arrival sets matchHere and fixes the match begin, and everything after
// it is dropped (leftmost-first). When seedStart is true a fresh lowest-priority
// start thread beginning at sp is unioned in (the hunting rule). It is the cached
// driver's universal frontier builder: used to seed the initial / re-seeded state
// and to land after a fallback step, so the cached state always matches what the
// per-step simulation would carry.
func (s *dfaCacheSim) closeFrontier(seeds []dfaThread, sp int, seedStart bool) (st *dfaCacheState, begins []int32, matchHere bool, matchBegin int32) {
	sim := s.sim
	sim.th.bump()
	sim.th.clist = sim.th.clist[:0]
	for _, t := range seeds {
		sim.add(&sim.th.clist, t.node, t.begin, sp)
	}
	if seedStart {
		sim.add(&sim.th.clist, int32(sim.nfa.start), int32(sp), sp)
	}
	nodes := s.fbNodes[:0]
	begins = s.fbBegins[:0]
	matchBegin = -1
	for _, t := range sim.th.clist {
		if sim.nfa.insts[t.node].op == nfaMatch {
			matchHere = true
			matchBegin = t.begin
			break // first accepting arrival truncates the frontier
		}
		nodes = append(nodes, t.node)
		begins = append(begins, t.begin)
	}
	s.fbNodes, s.fbBegins = nodes, begins
	s.c.mu.Lock()
	st = s.c.internState(nodes, matchHere)
	s.c.mu.Unlock()
	return st, begins, matchHere, matchBegin
}

// fallbackStep advances one input position the cached table cannot key: it steps
// every consuming node of st against the real input at sp (so a rune-aware atom
// consumes its true width and a position-dependent assertion sees the right
// context), seeding a fresh start at the landing position when hunting, and returns
// the resulting cached state at sp+w plus that width. It is the per-step simulation
// confined to a single position; the driver resumes the cached path afterwards.
func (s *dfaCacheSim) fallbackStep(st *dfaCacheState, begins []int32, sp int, hunting bool) (next *dfaCacheState, nextBegins []int32, matchHere bool, matchBegin int32, width int) {
	sim := s.sim
	seeds := s.fbSeeds[:0]
	stepWidth := 0
	for i, node := range st.nodes {
		ok, w := sim.atomStep(sim.nfa.insts[node].inst, sp)
		if !ok {
			continue
		}
		seeds = append(seeds, dfaThread{node: int32(sim.nfa.insts[node].x), begin: begins[i]})
		stepWidth = w
	}
	s.fbSeeds = seeds
	if stepWidth == 0 {
		// No consuming thread advanced: the successor frontier is whatever a freshly
		// seeded start (when hunting) closes to at the next byte boundary, exactly as
		// the simulation's stepWidth==0 arm advances the cursor by one code point.
		width = sim.advanceWidth(sp)
		next, nextBegins, matchHere, matchBegin = s.closeFrontier(nil, sp+width, hunting)
		return next, nextBegins, matchHere, matchBegin, width
	}
	width = stepWidth
	// Close the successors at the landing position; seed a fresh start there too when
	// still hunting (the simulation seeds the next start at sp+stepWidth).
	next, nextBegins, matchHere, matchBegin = s.closeFrontier(seeds, sp+width, hunting)
	return next, nextBegins, matchHere, matchBegin, width
}

// fbGateWindow / fbGateMin set the adaptive fallback-dominance gate. Over the first
// fbGateWindow consumed positions the scan counts how many took the per-step fallback
// (a multi-byte UTF-8 lead byte, or an assertion-crossing closure the byte-keyed table
// cannot express); if at least fbGateMin of them did, it abandons the cached path
// (returns useSim=true) and DFA.Search reruns the whole search on the per-step NFA
// simulation. A fallback interns a DFA state per position (an allocation), so when the
// fallbacks dominate the cached table is not paying for itself and the simulation —
// which handles every position uniformly with no per-position interning and runs
// allocation-free — is the faster engine. This catches both regression classes the
// cached DFA would otherwise lose on versus the bare simulation: a multi-byte-heavy
// UTF-8 haystack (e.g. `.x` over mixed CJK/Greek/Latin, which falls back at roughly
// every other position), and an assertion-driven state churn (e.g. the `\A`-anchored
// ReDoS patterns, which fall back at essentially every position).
//
// The window is short so the abandoned work is negligible (a fixed handful of interns
// before the bail), and only consumed positions count, so a long prefilter skip over
// dead input does not trip it. The threshold is 3/8 of the window — comfortably above
// the near-zero fallback rate of an ASCII-dominated scan (a literal/class/anchor run
// over ASCII never falls back, and the odd interior `\b`/`^` is sparse), and
// comfortably below the ~50–100% rate of the multi-byte and assertion-churn cases — so
// the two regimes are separated with wide margin and an ASCII-winning pattern is never
// misrouted off the cached path.
const (
	fbGateWindow = 16
	fbGateMin    = fbGateWindow * 3 / 8
)

// searchCached is the cached-DFA forward scan. It returns the leftmost-first match's
// [begin, end) byte span and whether any match was found, identical to dfaSim.search
// but driving the memoized transition table for width-1 ASCII positions. anchored
// restricts the scan to start offset 0. It returns useSim=true (with the span fields
// unset) when it detects, within the first fbGateWindow consumed positions, that the
// per-step fallback dominates and the simulation would be faster; the caller then
// reruns the search on the simulation.
func (s *dfaCacheSim) searchCached(anchored bool) (begin, end int, found, useSim bool) {
	sim := s.sim
	c := s.c
	input := sim.ctx.input
	matchBegin, matchEnd := -1, -1
	// Adaptive gate state: positions consumed so far and how many took the fallback.
	consumed, fellBack := 0, 0

	// seed mirrors dfaSim.search's prefilter-driven start locator.
	seed := func(at int) int {
		if anchored {
			if at == 0 {
				return 0
			}
			return -1
		}
		if sim.usePF {
			return sim.pf.nextStart(input, at)
		}
		return at
	}

	// cur / spare are the two fixed-capacity ping-pong buffers (each sized to the NFA
	// node count, the maximum frontier width). begins is always cur[:k]; the hot
	// cached step fills spare and swaps, so the steady-state loop never allocates. A
	// seed / fallback returns a fresh begins slice, which is copied into cur to keep
	// the ping-pong invariant.
	cur, spare := s.bufA, s.bufB
	adopt := func(b []int32) []int32 {
		cur = cur[:len(b)]
		copy(cur, b)
		return cur
	}

	sp := seed(0)
	if sp < 0 {
		return -1, -1, false, false
	}
	st, fresh, matchHere, mBegin := s.closeFrontier(nil, sp, true)
	begins := adopt(fresh)
	if matchHere {
		matchBegin, matchEnd = int(mBegin), sp
	}

	for {
		// Adaptive fallback-dominance gate. Once the opening window of consumed positions
		// is full, if at least fbGateMin of them took the per-step fallback the cached
		// table is not paying for itself (each fallback interns a DFA state — an
		// allocation), so the per-step simulation — which pays no per-position interning
		// and runs allocation-free — is the faster engine. Abandon the cached path (the
		// caller reruns the whole search on the simulation). Evaluated here, at the loop
		// top, so it fires regardless of which path filled the final window slot. It is
		// gated on matchBegin < 0 because once a match begin is fixed the remaining scan is
		// bounded and a switch would not pay off; the counters are frozen once the window
		// fills (below), so this equality is a one-shot at the boundary, never a recurring
		// per-position cost afterwards.
		if matchBegin < 0 && consumed == fbGateWindow && fellBack >= fbGateMin {
			return 0, 0, false, true
		}
		// No surviving consuming thread: finish (if a match is fixed) or jump the cursor
		// to the next viable start and re-seed, mirroring dfaSim.search's clist-empty arm.
		if len(st.nodes) == 0 {
			if matchBegin >= 0 {
				break
			}
			ns := seed(sp)
			if ns < 0 {
				break
			}
			sp = ns
			st, fresh, matchHere, mBegin = s.closeFrontier(nil, sp, true)
			begins = adopt(fresh)
			if matchHere {
				matchBegin, matchEnd = int(mBegin), sp
			}
			if len(st.nodes) == 0 {
				if matchBegin >= 0 {
					break
				}
				if sp >= len(input) {
					break
				}
				sp += sim.advanceWidth(sp)
				continue
			}
		}

		if sp >= len(input) {
			break // no byte to consume; any match is already recorded
		}

		hunting := matchBegin < 0
		b := input[sp]
		// Width-1 cacheable position: an ASCII byte (its own one-byte code point in both
		// encodings) whose transition does not cross a position-dependent assertion.
		if b < 0x80 || sim.ctx.enc == compile.ASCII8BIT {
			cl := c.byteClass[b]
			next, src, mSrc, uncacheable := c.stepCached(st, cl, hunting)
			if !uncacheable {
				// Gather the next frontier's begins into the spare buffer, then ping-pong:
				// begins becomes the just-filled buffer and the old one becomes the spare —
				// no per-byte allocation.
				nextBegins := spare[:len(next.nodes)]
				for i, sidx := range src {
					if sidx < 0 {
						// A node inherited from the freshly seeded start: its match began at
						// the landing position (the next cursor), not from a prior thread.
						nextBegins[i] = int32(sp + 1)
					} else {
						nextBegins[i] = begins[sidx]
					}
				}
				if next.matchHere {
					mb := int32(sp + 1)
					if mSrc >= 0 {
						mb = begins[mSrc]
					}
					matchBegin, matchEnd = int(mb), sp+1
				}
				cur, spare = nextBegins, cur
				st, begins = next, nextBegins
				sp++
				if consumed < fbGateWindow {
					consumed++ // a cached (width-1) step: counts toward the gate window
				}
				continue
			}
		}

		// Fallback for this one position: a multi-byte UTF-8 lead byte, or an
		// uncacheable (assertion-crossing) transition. Resume cached afterwards. Both
		// counters feed the adaptive gate evaluated at the loop top; they freeze once the
		// window fills so the gate is a one-shot at the boundary.
		if consumed < fbGateWindow {
			consumed++
			fellBack++
		}
		next, fresh, fMatch, fBegin, w := s.fallbackStep(st, begins, sp, hunting)
		begins = adopt(fresh)
		if fMatch {
			matchBegin, matchEnd = int(fBegin), sp+w
		}
		st = next
		sp += w
	}

	if matchBegin < 0 {
		return -1, -1, false, false
	}
	return matchBegin, matchEnd, true, false
}
