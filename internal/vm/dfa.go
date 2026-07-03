package vm

import (
	"sync"

	"github.com/go-ruby-regexp/regexp/internal/compile"
)

// This file adds an on-the-fly (lazy-DFA-style) NFA simulation for the matchable
// subset of programs — the RE2 / Go-regexp style automaton that finds match
// bounds at roughly one step per input position, eliminating the per-character
// backtracking-VM dispatch that makes the engine 3–16× slower than C Onigmo on
// quantifier / class / alternation inner loops.
//
// Scope and fallback. The simulation only ever computes the leftmost-match BOUNDS
// (the whole-match span, group 0) for a program that contains no construct whose
// future depends on captured text or on a separate backtracking stack:
// no backreference, no subexpression call (\g<…>), no lookaround, and no atomic /
// possessive group. Anchors, classes, the dot, Unicode properties, alternation,
// and greedy / lazy quantifiers are all handled. Anything outside the subset
// routes to the existing backtracking VM, which also still runs whenever a caller
// needs captured submatches (the simulation finds the bounds; the VM, anchored to
// those bounds, extracts the groups). The backtracker therefore remains the
// source of truth for every feature the simulation excludes, and its bitset-memo
// keeps the linear-time ReDoS guarantee for the fallback. The NFA simulation is
// itself inherently linear in the input length.
//
// Leftmost-first semantics. Ruby / Onigmo is leftmost-FIRST (the greedy / lazy
// ordering decides where the whole match ends), not leftmost-longest, so a plain
// set automaton would report wrong bounds (e.g. a|ab on "ab" matches "a", not
// "ab"). The simulation preserves leftmost-first by carrying a PRIORITY-ORDERED
// thread list — the program-order of OpSplit's X (preferred) vs Y (fallback)
// branch is honoured during epsilon-closure exactly as the backtracker's push
// order is, and the first thread to reach OpMatch fixes the match end while every
// lower-priority thread is then dropped. This is the Pike-VM / RE2 priority rule,
// and it agrees with the backtracker on the whole-match span for every pattern in
// the subset.

// nfaOp is the opcode of one node in the byte-stepping NFA the simulation runs. It
// is a flattened, fully-expanded view of the compiled program: the fused OpLoop
// is unrolled back into explicit split / atom / jump nodes here so the simulation
// core is uniform, and only the subset of opcodes the simulation supports appears.
type nfaOp uint8

const (
	nfaChar                  nfaOp = iota // consume: matches the atom in inst, one transition
	nfaSplit                              // epsilon: try x (preferred) then y
	nfaJmp                                // epsilon: go to x
	nfaSave                               // epsilon: capture save — transparent (no submatch)
	nfaMatch                              // accepting
	nfaAssertBeginText                    // epsilon assertion: \A
	nfaAssertEndText                      // epsilon assertion: \z
	nfaAssertEndTextNL                    // epsilon assertion: \Z
	nfaAssertBeginLine                    // epsilon assertion: ^
	nfaAssertEndLine                      // epsilon assertion: $
	nfaAssertPrevMatch                    // epsilon assertion: \G
	nfaAssertWordBoundary                 // epsilon assertion: \b (position-dependent on surrounding chars)
	nfaAssertNonWordBoundary              // epsilon assertion: \B
)

// nfaInst is one NFA node. For a consuming node (nfaChar) inst holds the original
// atom instruction (OpChar / OpClass / OpAny / OpUniProp / OpFoldChar) so the
// simulation reuses the VM's exact per-atom acceptance test; x / y are successor
// node indices.
type nfaInst struct {
	op   nfaOp
	inst compile.Inst // the atom for nfaChar (its Op field names which atom)
	x, y int
}

// nfaProg is the expanded NFA derived once per program. start is the entry node.
//
// closure[node] is the precomputed epsilon-closure of node: the priority-ordered
// list of consuming (nfaChar) and accepting (nfaMatch) nodes reachable from node
// through only epsilon edges, with duplicates removed (highest priority kept). It
// is valid only when ctxFree[node] is true — i.e. no position-dependent assertion
// (^ $ \A \z \Z \G) lies on any epsilon path out of node — so the hot executor can
// expand a thread's successor by one slice copy instead of a fresh recursive walk.
// When ctxFree[node] is false the executor falls back to the position-aware add().
type nfaProg struct {
	insts   []nfaInst
	start   int
	closure [][]int32
	ctxFree []bool
}

// maxLoopUnroll bounds how many atom copies a bounded {m,n} loop may unroll into
// when building the NFA. A loop whose Max exceeds it keeps the program out of the
// DFA subset so the NFA never explodes; such a pattern simply runs on the
// backtracking VM. Unbounded loops (Max < 0) unroll to Min mandatory copies plus
// one back-edge and so are always small.
const maxLoopUnroll = 256

// buildNFA expands prog into a byte-stepping NFA, or returns ok=false when the
// program is outside the DFA subset (a backreference, call, lookaround, atomic
// group, or an over-large bounded loop). It is run once per program and cached.
func buildNFA(prog *compile.Program) (*nfaProg, bool) {
	b := &nfaBuilder{src: prog.Insts}
	if !b.plan() {
		return nil, false
	}
	np := &nfaProg{insts: b.out, start: b.srcMap[0]}
	np.computeClosures()
	return np, true
}

// computeClosures fills closure[node] and ctxFree[node] for every node: the
// priority-ordered epsilon-closure (the consuming / accepting nodes reachable via
// epsilon edges) and whether that closure is free of position-dependent assertions
// (so it is valid at any input position). The hot executor uses the precomputed
// list to expand a thread's successor by a slice copy; it falls back to the
// position-aware walk only for the few nodes whose closure crosses an assertion.
func (np *nfaProg) computeClosures() {
	n := len(np.insts)
	np.closure = make([][]int32, n)
	np.ctxFree = make([]bool, n)
	for i := 0; i < n; i++ {
		var out []int32
		ctxFree := true
		seen := make([]bool, n)
		var walk func(node int)
		walk = func(node int) {
			if node < 0 || seen[node] {
				return
			}
			seen[node] = true
			switch np.insts[node].op {
			case nfaSplit:
				walk(np.insts[node].x)
				walk(np.insts[node].y)
			case nfaJmp, nfaSave:
				walk(np.insts[node].x)
			case nfaChar, nfaMatch:
				out = append(out, int32(node))
			default:
				// An assertion node: its closure depends on the input position, so the
				// node's whole closure is context-dependent and not cacheable. Mark it
				// and stop following (the executor will use the position-aware walk).
				ctxFree = false
			}
		}
		walk(i)
		np.closure[i] = out
		np.ctxFree[i] = ctxFree
	}
}

// nfaBuilder accumulates NFA nodes while expanding the source program. Forward
// references to source-pc successors are recorded as fixups and patched once every
// source pc has an entry node (srcMap).
type nfaBuilder struct {
	src    []compile.Inst
	out    []nfaInst
	srcMap []int   // source pc → entry NFA node index
	fixups []fixup // out[node].{x|y} must become srcMap[srcPC]
}

// fixup records that out[node].x (field 0) or .y (field 1) must be set to
// srcMap[srcPC] after planning.
type fixup struct {
	node, field, srcPC int
}

func (b *nfaBuilder) emit(n nfaInst) int {
	b.out = append(b.out, n)
	return len(b.out) - 1
}

func (b *nfaBuilder) toSrc(node, field, srcPC int) {
	b.fixups = append(b.fixups, fixup{node: node, field: field, srcPC: srcPC})
}

// plan walks the source program, expanding each instruction into NFA nodes and
// recording src-pc → entry-node in srcMap. It returns false if any instruction is
// outside the subset.
func (b *nfaBuilder) plan() bool {
	src := b.src
	b.srcMap = make([]int, len(src))
	for i := range b.srcMap {
		b.srcMap[i] = -1
	}
	for pc := 0; pc < len(src); pc++ {
		in := src[pc]
		b.srcMap[pc] = len(b.out)
		switch in.Op {
		case compile.OpChar, compile.OpFoldChar, compile.OpAny, compile.OpClass, compile.OpUniProp:
			node := b.emit(nfaInst{op: nfaChar, inst: in})
			b.toSrc(node, 0, pc+1)
		case compile.OpSplit:
			node := b.emit(nfaInst{op: nfaSplit})
			b.toSrc(node, 0, in.X)
			b.toSrc(node, 1, in.Y)
		case compile.OpJmp:
			node := b.emit(nfaInst{op: nfaJmp})
			b.toSrc(node, 0, in.X)
		case compile.OpSave:
			node := b.emit(nfaInst{op: nfaSave})
			b.toSrc(node, 0, pc+1)
		case compile.OpReturn:
			// Group 0's terminator (the only OpReturn in a call-free program) is a
			// transparent epsilon falling through to the next instruction.
			node := b.emit(nfaInst{op: nfaJmp})
			b.toSrc(node, 0, pc+1)
		case compile.OpMatch:
			b.emit(nfaInst{op: nfaMatch})
		case compile.OpAssertBeginText:
			node := b.emit(nfaInst{op: nfaAssertBeginText})
			b.toSrc(node, 0, pc+1)
		case compile.OpAssertEndText:
			node := b.emit(nfaInst{op: nfaAssertEndText})
			b.toSrc(node, 0, pc+1)
		case compile.OpAssertEndTextOptNL:
			node := b.emit(nfaInst{op: nfaAssertEndTextNL})
			b.toSrc(node, 0, pc+1)
		case compile.OpAssertBeginLine:
			node := b.emit(nfaInst{op: nfaAssertBeginLine})
			b.toSrc(node, 0, pc+1)
		case compile.OpAssertEndLine:
			node := b.emit(nfaInst{op: nfaAssertEndLine})
			b.toSrc(node, 0, pc+1)
		case compile.OpAssertPrevMatch:
			node := b.emit(nfaInst{op: nfaAssertPrevMatch})
			b.toSrc(node, 0, pc+1)
		case compile.OpAssertWordBoundary:
			node := b.emit(nfaInst{op: nfaAssertWordBoundary})
			b.toSrc(node, 0, pc+1)
		case compile.OpAssertNonWordBoundary:
			node := b.emit(nfaInst{op: nfaAssertNonWordBoundary})
			b.toSrc(node, 0, pc+1)
		case compile.OpLoop:
			if !b.planLoop(pc, in) {
				return false
			}
		default:
			// OpBackref, OpCall, OpLook, OpLookEnd, OpAtomicBegin, OpAtomicEnd.
			return false
		}
	}
	for _, f := range b.fixups {
		t := b.srcMap[f.srcPC]
		if f.field == 0 {
			b.out[f.node].x = t
		} else {
			b.out[f.node].y = t
		}
	}
	return true
}

// planLoop expands a fused OpLoop at source pc into explicit NFA nodes with the
// identical match set and leftmost-first ordering as the backtracker's fused loop:
// Min mandatory atom copies, then the optional remainder as either an unbounded
// split-loop (Max < 0) or Max-Min nested optional copies, the greedy / lazy
// preference encoded in each split's branch order. The whole construct's entry
// node is recorded in srcMap[pc]; its exit flows to the loop continuation (source
// pc in.X). It returns false if a bounded unroll would be too large.
//
// The chain is built from the exit end backward so each node's successor already
// exists when it is emitted: tail (optional part) first, then the Min mandatory
// copies prepended. `succ` always names the node a freshly emitted atom should
// flow into; -1 means "the loop continuation" (patched via a fixup to in.X).
func (b *nfaBuilder) planLoop(pc int, in compile.Inst) bool {
	if in.Max >= 0 && in.Max > maxLoopUnroll {
		return false
	}
	atom := in
	atom.Op = in.Sub

	// emitAtom emits one consuming node flowing into succ (or the continuation when
	// succ < 0) and returns it.
	emitAtom := func(succ int) int {
		node := b.emit(nfaInst{op: nfaChar, inst: atom})
		if succ < 0 {
			b.toSrc(node, 0, in.X)
		} else {
			b.out[node].x = succ
		}
		return node
	}

	// Build the optional tail. succ is the node a thread reaches after the optional
	// part (and after each mandatory atom): for the unbounded case it is the split
	// itself, for the bounded case the first optional split, and with no optional
	// part it is the continuation sentinel (-1).
	succ := -1
	if in.Max < 0 {
		// Unbounded: split prefers (greedy) the atom-then-loop or (lazy) the exit.
		split := b.emit(nfaInst{op: nfaSplit})
		atomNode := emitAtom(split) // atom loops back to the split
		if in.Greedy {
			b.out[split].x = atomNode
			b.toSrc(split, 1, in.X) // exit on y
		} else {
			b.toSrc(split, 0, in.X) // exit on x
			b.out[split].y = atomNode
		}
		succ = split
	} else {
		// Bounded optional copies: Max-Min nested optionals, built inside-out.
		for i := 0; i < in.Max-in.Min; i++ {
			split := b.emit(nfaInst{op: nfaSplit})
			atomNode := emitAtom(succ) // this optional's atom flows into the prior tail
			if in.Greedy {
				b.out[split].x = atomNode
				if succ < 0 {
					b.toSrc(split, 1, in.X)
				} else {
					b.out[split].y = succ
				}
			} else {
				b.out[split].y = atomNode
				if succ < 0 {
					b.toSrc(split, 0, in.X)
				} else {
					b.out[split].x = succ
				}
			}
			succ = split
		}
	}

	// Mandatory copies: Min atoms in a row, the last flowing into the tail (succ).
	// The construct always has at least one node: the compiler never emits an OpLoop
	// for {0,0} (a zero-width loop is dropped at compile time), so either Min >= 1
	// (the mandatory atoms below) or an optional tail exists (succ >= 0) — entry is
	// therefore always a real node index after this.
	entry := succ
	for i := 0; i < in.Min; i++ {
		entry = emitAtom(entry)
	}
	b.srcMap[pc] = entry
	return true
}

// --- Public DFA API ------------------------------------------------------- //

// DFA is the per-program lazy-NFA accelerator: the expanded NFA plus a pool of
// reusable thread lists. It is built once from a compiled program (BuildDFA) and
// reused across matches; it is safe for concurrent use because each search borrows
// its own thread lists from the pool. A program outside the DFA subset (a
// backreference, call, lookaround, atomic group, or over-large bounded loop)
// yields a nil DFA, and the caller falls back to the backtracking VM.
type DFA struct {
	nfa      *nfaProg
	anchored bool      // the program is \A-anchored: only offset 0 can start a match
	pf       prefilter // start-locating prefilter, shared with the backtracking VM
	usePF    bool      // pf can actually skip positions
	cache    *dfaCache // memoized lazy-DFA transition table (the inner-loop accelerator)
	pool     sync.Pool // of *dfaRun: the reusable per-search scratch (see dfaRun)
	// classRun is non-nil when the whole program is a single anchored repeat of one
	// byte-decidable atom (`\s+`, `\S+`, `\w+`, `[0-9]+`, `.+`, `a+`). MatchAt then
	// consumes the run with a tight class-bitset loop instead of the per-step
	// simulation — the StringScanner#skip / #match? fast path. It is nil for every
	// other program, which keep the simulation.
	classRun *classRun
}

// dfaRun bundles all of one search's reusable scratch — the priority thread
// lists, the per-step simulation, and the cached-driver state with its ping-pong
// begin buffers — into a single pooled object. A search grabs one from the pool,
// re-points it at this call's input, runs, and returns it, so a steady-state
// Search / MatchAt loop (a StringScanner tokenizer re-matching from an advancing
// cursor) allocates nothing per call beyond the returned span. All heap backing
// (thread slices, visited set, the two int32 buffers, the fallback scratch) is
// built once by pool.New and reused; only the value fields are reset per call.
type dfaRun struct {
	th  dfaThreads
	sim dfaSim
	cs  dfaCacheSim
}

// BuildDFA expands prog into a DFA, returning nil when the program is outside the
// DFA subset (so the caller uses the backtracking VM). It is run once per compiled
// program. A program with a backreference or a subexpression call is rejected up
// front via the program's own flags; buildNFA rejects the remaining excluded
// constructs (lookaround, atomic groups, over-large bounded loops) while walking.
func BuildDFA(prog *compile.Program) *DFA {
	if prog.HasBackref || prog.HasCall {
		return nil
	}
	nfa, ok := buildNFA(prog)
	if !ok {
		return nil
	}
	pf := analyze(prog)
	// A strong literal filter — a required leading prefix or a required interior
	// literal — makes the backtracking VM's strings.Index-driven scan (a runtime
	// Boyer–Moore) the fastest path: it jumps directly to candidate positions and the
	// fused inner loop verifies them, beating the DFA's per-position byte stepping
	// through the matched region. The DFA's advantage is on the class / quantifier /
	// alternation / anchor-led patterns that have no such literal to seek, where the
	// VM must otherwise re-enter per start position. So when a literal prefix or
	// required interior literal is present we keep the VM (return nil here); the DFA
	// is built only for the cases it actually wins. This gate is purely a performance
	// choice — both paths produce identical results — measured across the parity
	// suite (literal/prefix scans regressed under the DFA; class/quantifier/anchor
	// and ReDoS cases improved 1.3–22×).
	if pf.prefix != "" || pf.required != "" {
		return nil
	}
	d := &DFA{nfa: nfa, anchored: leadingAnchored(prog), pf: pf, usePF: pf.usable(), cache: newDFACache(nfa, prog.Enc), classRun: detectClassRun(prog)}
	n := len(nfa.insts)
	d.pool.New = func() any {
		r := &dfaRun{}
		r.th = *newDFAThreads(n)
		r.cs.bufA = make([]int32, n)
		r.cs.bufB = make([]int32, n)
		return r
	}
	return d
}

// leadingAnchored reports whether every match must begin at offset 0 because the
// program opens with \A (OpAssertBeginText), reached only through the overall-match
// open save. When true the DFA search seeds a start thread at offset 0 only,
// turning the scan into a single anchored attempt.
func leadingAnchored(prog *compile.Program) bool {
	// Skip the leading overall-match open saves; the first instruction that consumes
	// or asserts decides. A compiled program always reaches such an instruction
	// (every program ends in OpMatch), so the scan terminates inside the loop.
	pc := 0
	for pc < len(prog.Insts) && prog.Insts[pc].Op == compile.OpSave {
		pc++
	}
	return pc < len(prog.Insts) && prog.Insts[pc].Op == compile.OpAssertBeginText
}

// Search returns the leftmost-first match's [begin, end) byte span in input and
// whether any match was found, using the linear-time NFA simulation. gpos is the
// scan origin for \G (0 for a plain whole-string match). Results are identical to
// the backtracking VM's whole-match span for every program the DFA accepts.
func (d *DFA) Search(input string, enc compile.Encoding, gpos int) (int, int, bool) {
	r := d.pool.Get().(*dfaRun)
	// Re-point the pooled scratch at this call. Only the value fields are reset; the
	// slice backings (thread lists, buffers, fallback scratch) are retained.
	r.sim = dfaSim{nfa: d.nfa, th: &r.th, ctx: dfaCtx{input: input, enc: enc, gpos: gpos}, pf: d.pf, usePF: d.usePF}
	r.cs.c = d.cache
	r.cs.sim = &r.sim
	// The cached-DFA driver accelerates the width-1 ASCII inner loop, borrowing this
	// sim (its thread pool, atom tests, and prefilter) for the multi-byte and
	// assertion-crossing positions it cannot key. It produces the identical leftmost
	// -first span the per-step simulation would. On a multi-byte-heavy UTF8 haystack
	// every position is a fallback (a per-position state intern), which is costlier
	// than the per-step simulation; the driver detects that adaptively and returns
	// useSim=true without scanning further, so DFA.Search re-runs the whole search on
	// the simulation (which handles every position uniformly, no interning, no
	// per-position allocation). The gate is a pure performance choice — both engines
	// produce the identical leftmost-first span.
	b, e, ok, useSim := r.cs.searchCached(d.anchored)
	if useSim {
		// The cached scan bailed early on a multi-byte-dominated prefix; rerun on the
		// per-step simulation. The borrowed sim's thread list is reset at the top of
		// search(), so resuming on it from a fresh start is safe.
		b, e, ok = r.sim.search(d.anchored)
	}
	d.pool.Put(r)
	return b, e, ok
}

// MatchAt runs the NFA anchored at pos: the whole match must BEGIN exactly at pos
// (so begin==pos on success), with the entire input visible so the text/line
// anchors (\A ^) and lookbehind see the real prefix input[:pos] and \G binds to
// pos. It plants a single start thread at pos and never scans forward, which is
// the cursor-anchored primitive a StringScanner-style tokenizer needs (Scan /
// Skip / match? re-matching from an advancing position). It returns the
// [begin,end) span and whether a match occurred.
//
// It runs on the per-step simulation directly rather than the cached driver: the
// cached transition table amortises its per-position state-interning cost over a
// long forward scan, but a single anchored attempt visits too few positions to
// repay it, so the plain simulation (sharing the same pooled thread lists) is
// both simpler and faster here. The span is identical to what the backtracking VM
// anchored at pos would report for every program the DFA accepts.
func (d *DFA) MatchAt(input string, enc compile.Encoding, pos int) (int, int, bool) {
	// Fast anchored consumer: when the whole program is a single anchored class /
	// dot / literal repeat, settle the run with a class-bitset loop rather than the
	// per-step simulation. It returns a definite answer for the ASCII fast path and
	// bows out (definite == false) only when it meets a byte it cannot decide (a
	// multi-byte UTF-8 lead), in which case the general engine below runs the match.
	// The span it returns is byte-identical to the simulation's for every input.
	if cr := d.classRun; cr != nil {
		if end, ok, definite := cr.match(input, enc, pos); definite {
			if !ok {
				return -1, -1, false
			}
			return pos, end, true
		}
	}
	r := d.pool.Get().(*dfaRun)
	r.sim = dfaSim{nfa: d.nfa, th: &r.th, ctx: dfaCtx{input: input, enc: enc, gpos: pos}, pf: d.pf, usePF: d.usePF, startAt: pos}
	b, e, ok := r.sim.search(true)
	d.pool.Put(r)
	return b, e, ok
}
