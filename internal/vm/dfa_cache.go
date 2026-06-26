package vm

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/go-ruby-regexp/regexp/internal/compile"
)

// This file adds a cached lazy DFA over the expanded nfaProg built in dfa.go. It
// memoizes the whole-state (NFA frontier) transitions that the per-step NFA
// simulation in dfa_run.go recomputes on every input byte. After the first
// encounter of a (frontier, input-byte) pair the next frontier is a single table
// lookup, so a steady-state scan over similar bytes (a [a-zA-Z]+ / \w+ / email
// run) costs roughly one array index per byte instead of an epsilon-closure walk
// plus a per-thread atom test.
//
// This is the canonical RE2 / Go-regexp lazy DFA, specialised to this engine:
//
//   - A DFA state is the priority-ORDERED, deduplicated frontier of NFA consuming
//     (nfaChar) / accepting (nfaMatch) nodes — exactly the clist the simulation
//     carries, with the begin offsets stripped out. Ordering encodes Ruby's
//     leftmost-FIRST priority: the first nfaMatch in the order fixes the match, so
//     a state is truncated at its first match node and flagged accepting; lower
//     -priority threads after it can never win and are dropped (collapsing them
//     would yield leftmost-LONGEST and break a|ab). Two identical orderings intern
//     to one state.
//
//   - A transition (state, input-byte) -> next state is computed once by stepping
//     every consuming node of the state with the VM's exact atom test and taking
//     the epsilon-closure of the successors (the same add() the simulation uses),
//     then interned and cached. Bytes are first folded to equivalence CLASSES
//     (bytes no atom distinguishes share a column) so the table is narrow.
//
//   - The begin offset each surviving thread carries is DATA, not part of the
//     cached state, so it is propagated alongside the cached transition via a
//     per-transition source map: nextBegin[i] = curBegin[src[i]]. The source map is
//     itself a pure function of (state, byte) and is cached with the transition, so
//     the hot loop only gathers begins through a small int slice — no closure walk.
//
// Scope. The cached DFA only handles WIDTH-1 transitions: an input byte that is a
// complete one-byte code point (every byte in ASCII8BIT mode, every byte < 0x80 in
// UTF8 mode). That covers the ASCII-dominated inner loops the lever targets. When
// the cursor sits on a multi-byte UTF-8 lead byte the search falls back to the
// uncached per-step simulation for that one position (a rune-aware atom there
// consumes >1 byte, which the byte-keyed table cannot express), then resumes the
// cached path. Assertion-bearing frontiers (a ^ $ \A \z \Z \G on an epsilon path)
// are also stepped uncached, because their closure depends on the input position;
// the cached table is keyed only on the byte. Everything else — classes, the dot,
// Unicode properties on ASCII input, alternation, greedy / lazy / bounded
// quantifiers — runs on the cached path. The result is identical to the per-step
// simulation (which is identical to the backtracking VM) on every input.
//
// Linearity / memory bound. The cache is bounded (maxDFAStates). On overflow it is
// cleared and rebuilt from the live frontier (RE2's flush): correctness is
// unaffected (only warmup is lost), and because each input position advances the
// cursor exactly once whether the transition was a hit or a freshly computed miss,
// the scan stays linear in the input length and the memory stays bounded. The
// ReDoS guarantee is therefore preserved: \A(a*)*b and \A(a|aa)+b remain linear.
//
// Concurrency. Transitions are stored as direct *dfaCacheState pointers, which stay
// valid for the lifetime of any search holding them (Go's GC keeps a referenced
// state alive even after a flush drops it from the intern map). Computing or
// interning a state takes c.mu; a steady-state hit reads a cached pointer under the
// same short lock. A flush only shrinks the intern map and the live-states slice,
// never invalidating a pointer an in-flight search already loaded — so the cache is
// safe for concurrent searches without per-search copies.

// maxDFAStates bounds the interned-state table. A frontier is a subset of the NFA
// nodes, so the reachable-state count is at worst exponential in the program size;
// this cap keeps memory linear by triggering a clear-and-rebuild (RE2's approach)
// well before any blow-up. Real Ruby patterns reach only a handful of states, so
// the cap is never hit in practice; it exists purely to bound pathology.
const maxDFAStates = 1 << 14

// dfaCache is the per-program memoized transition table, built lazily during
// searches and reused across them. c.mu guards the intern map, the live-states
// count, and the lazy fill of a state's transition rows.
type dfaCache struct {
	nfa       *nfaProg
	enc       compile.Encoding // the program's encoding; representatives are stepped under it
	byteClass [256]uint8       // input byte -> equivalence-class column
	classRep  []string         // class -> a one-byte string of a representative byte
	nClasses  int              // number of distinct byte classes (table width)

	mu     sync.Mutex
	intern map[string]*dfaCacheState
	count  int // live interned states, for the overflow bound
	dead   *dfaCacheState
}

// dfaCacheState is one interned DFA state: a priority-ordered frontier plus its
// lazily-filled transition rows. nodes lists the consuming NFA nodes in priority
// order; matchHere is true when the frontier's highest-priority arrival was the
// match node (the state accepts at the current position) — match nodes are not
// stored in nodes, since a match truncates the frontier. The two transition rows
// are keyed by byte class: trans is taken when the search is no longer seeding
// fresh start threads (a match has begun), transHunt while still hunting (a fresh
// lowest-priority start is unioned into the successor). Each entry holds the next
// *dfaCacheState (nil = unfilled) and the parallel src holds, per next-state node,
// the index into THIS state's nodes whose begin it inherits (-1 for a freshly
// seeded start, whose begin is the new cursor position).
// transEntry is one fully-computed (state, class) transition, published as an
// immutable unit via an atomic pointer so a steady-state cache hit reads it without
// taking the cache lock. next is the successor state (valid when uncacheable is
// false); src maps each next-state node to the index into the current state's nodes
// whose begin it inherits (-1 for a freshly seeded start); matchSrc is the begin
// -source of next's accepting arrival (used only when next.matchHere); uncacheable
// marks a transition whose closure crosses a position-dependent assertion, signalling
// the driver to fall back for that position. Once stored an entry is never mutated.
type transEntry struct {
	next        *dfaCacheState
	src         []int32
	matchSrc    int32
	uncacheable bool
}

type dfaCacheState struct {
	nodes     []int32
	matchHere bool

	// trans / transHunt hold, per byte class, an atomic pointer to the computed
	// transition (nil = not yet computed): the committed row (a match begin is fixed,
	// no fresh start seeded) and the hunting row (still seeking, a fresh lowest-priority
	// start unioned into each successor). A filled slot is read lock-free; only the
	// first computation of a slot takes the cache lock.
	trans     []atomic.Pointer[transEntry] // [nClasses] committed row
	transHunt []atomic.Pointer[transEntry] // [nClasses] hunting row
}

// newDFACache builds the byte-class partition for the program's atoms and returns
// an empty cache holding only the dead (empty-frontier) state.
func newDFACache(nfa *nfaProg, enc compile.Encoding) *dfaCache {
	c := &dfaCache{nfa: nfa, enc: enc, intern: make(map[string]*dfaCacheState)}
	c.buildByteClasses()
	c.dead = c.newCacheState(nil, false)
	c.intern[""] = c.dead
	c.count = 1
	return c
}

// buildByteClasses partitions byte values into equivalence classes: a signature is
// formed for each byte from the accept/reject decision of every nfaChar atom on a
// synthetic one-byte input, and bytes with identical signatures share a class.
//
// In ASCII8BIT mode every one of the 256 bytes is a complete one-byte code point an
// atom can match, so all 256 are partitioned and stepped on the cached path. In UTF8
// mode only bytes < 0x80 are complete one-byte code points; a byte >= 0x80 is a
// multi-byte lead or continuation that the cached table cannot key (the driver falls
// back for it), so all such bytes collapse into a single never-stepped class. The
// empty-program edge case (no atoms) yields a single class.
func (c *dfaCache) buildByteClasses() {
	classOf := make(map[string]uint8)
	next := uint8(0)
	var atoms []compile.Inst
	for _, n := range c.nfa.insts {
		if n.op == nfaChar {
			atoms = append(atoms, n.inst)
		}
	}
	mkSig := func(b byte) string {
		buf := make([]byte, 0, len(atoms))
		s := string([]byte{b})
		ctx := dfaCtx{input: s, enc: c.enc}
		for _, a := range atoms {
			ok, w := atomStepCtx(ctx, a, 0)
			if ok && w == 1 {
				buf = append(buf, 1)
			} else {
				buf = append(buf, 0)
			}
		}
		return string(buf)
	}
	// Bytes the cached path steps directly: all 256 under ASCII8BIT, only < 0x80 under
	// UTF8 (a >= 0x80 byte there is never a width-1 code point).
	cacheable := 0x80
	if c.enc == compile.ASCII8BIT {
		cacheable = 256
	}
	for b := 0; b < cacheable; b++ {
		s := mkSig(byte(b))
		cl, ok := classOf[s]
		if !ok {
			cl = next
			classOf[s] = cl
			next++
		}
		c.byteClass[b] = cl
	}
	if cacheable < 256 {
		// UTF8: the single never-stepped class for every multi-byte / continuation byte.
		nonASCII := next
		next++
		for b := cacheable; b < 256; b++ {
			c.byteClass[b] = nonASCII
		}
	}
	c.nClasses = int(next)
	// A representative one-byte string per class, for stepping atoms on a cache miss
	// without a per-step allocation. Each stepped class records its first member byte;
	// the UTF8 non-ASCII class is never stepped on the cached path (its representative
	// is a 0 byte, set only for completeness).
	c.classRep = make([]string, c.nClasses)
	for i := range c.classRep {
		c.classRep[i] = "\x00"
	}
	seen := make([]bool, c.nClasses)
	for b := 0; b < cacheable; b++ {
		cl := c.byteClass[b]
		if !seen[cl] {
			c.classRep[cl] = string([]byte{byte(b)})
			seen[cl] = true
		}
	}
}

// atomStepCtx applies the VM's exact per-atom acceptance test for a consuming atom
// at position sp using a read-only context, returning whether it matches and the
// byte width it consumes. It mirrors dfaSim.atomStep so the cache's byte-class
// signatures match the executor's decisions exactly.
func atomStepCtx(ctx dfaCtx, in compile.Inst, sp int) (bool, int) {
	switch in.Op {
	case compile.OpFoldChar:
		return foldCharStepCtx(ctx, in, sp)
	case compile.OpAny:
		return anyStepCtx(ctx, in, sp)
	case compile.OpClass:
		return classStepCtx(ctx, in, sp)
	case compile.OpUniProp:
		return propStepCtx(ctx, in, sp)
	default:
		if sp < len(ctx.input) && ctx.input[sp] == in.B {
			return true, 1
		}
		return false, 0
	}
}

func (c *dfaCache) newCacheState(nodes []int32, matchHere bool) *dfaCacheState {
	return &dfaCacheState{
		nodes:     nodes,
		matchHere: matchHere,
		trans:     make([]atomic.Pointer[transEntry], c.nClasses),
		transHunt: make([]atomic.Pointer[transEntry], c.nClasses),
	}
}

// frontierKey is the canonical interning key of a node-ordering: the priority
// -ordered node ids, comma-joined, with the match flag as a leading byte. Order is
// significant (leftmost-first), so two frontiers with the same nodes in a different
// order are distinct states.
func frontierKey(nodes []int32, matchHere bool) string {
	var b []byte
	if matchHere {
		b = append(b, 'M')
	}
	for i, n := range nodes {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, int64(n), 10)
	}
	return string(b)
}

// internState returns the state with the given priority-ordered frontier and match
// flag, creating it on first sight. The caller holds c.mu. On overflow it flushes
// the whole table (RE2-style clear-and-rebuild) and re-interns, keeping the scan
// linear and memory bounded at the cost of losing warmup.
func (c *dfaCache) internState(nodes []int32, matchHere bool) *dfaCacheState {
	key := frontierKey(nodes, matchHere)
	if st, ok := c.intern[key]; ok {
		return st
	}
	if c.count >= maxDFAStates {
		c.flush()
	}
	st := c.newCacheState(append([]int32(nil), nodes...), matchHere)
	c.intern[key] = st
	c.count++
	return st
}

// flush clears the interned-state table back to just the dead state, discarding all
// memoized transitions for the purpose of bounding memory. State pointers an
// in-flight search already holds stay valid (the GC keeps them); only future
// lookups re-derive, so correctness is preserved and just warmup is lost. The
// caller holds c.mu.
func (c *dfaCache) flush() {
	c.intern = make(map[string]*dfaCacheState)
	c.dead = c.newCacheState(nil, false)
	c.intern[""] = c.dead
	c.count = 1
}
