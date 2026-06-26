package vm

import (
	"unicode/utf8"

	"github.com/go-ruby-regexp/regexp/internal/charset"
	"github.com/go-ruby-regexp/regexp/internal/compile"
)

// This file is the leftmost-first NFA executor over the expanded nfaProg built in
// dfa.go. It is a Pike-VM-style on-the-fly simulation that tracks only the
// whole-match span (no submatches), so a priority-ordered thread list — each
// thread carrying just its start offset — suffices: the highest-priority thread
// to reach nfaMatch fixes the match, and the simulation is linear in the input
// length. It is used for the search / is-match case of subset programs and to find
// the leftmost-match bounds the backtracker is then anchored to for submatch
// extraction.

// dfaThread is one live NFA thread: the node it sits on and the input offset its
// match began at (so a completed match reports the right begin for the unanchored
// scan). Threads are held in a priority-ordered list; earlier means preferred.
type dfaThread struct {
	node  int32
	begin int32
}

// dfaThreads is the reusable pair of priority-ordered thread lists (current and
// next step) plus a generation-stamped visited set that deduplicates nodes so each
// is added at most once per step, keeping its highest-priority arrival. It is held
// on the per-program DFA cache and reused across searches.
type dfaThreads struct {
	clist, nlist []dfaThread
	visited      []uint32
	gen          uint32
}

func newDFAThreads(n int) *dfaThreads {
	return &dfaThreads{
		clist:   make([]dfaThread, 0, n+1),
		nlist:   make([]dfaThread, 0, n+1),
		visited: make([]uint32, n),
	}
}

// bump advances the visited generation, invalidating every prior stamp in O(1).
func (t *dfaThreads) bump() {
	t.gen++
	if t.gen == 0 {
		for i := range t.visited {
			t.visited[i] = 0
		}
		t.gen = 1
	}
}

// dfaCtx carries the read-only matching context an atom acceptance test and an
// epsilon assertion need: the input, the encoding, and the scan origin for \G.
type dfaCtx struct {
	input string
	enc   compile.Encoding
	gpos  int
}

// add performs the epsilon-closure of one thread into list `dst`, following
// epsilon nodes (splits, jumps, saves, assertions) in priority order and stopping
// at consuming (nfaChar) and accepting (nfaMatch) nodes, which are appended. sp is
// the current input position (for assertions). A node already visited this
// generation is skipped, preserving its higher-priority earlier arrival. The
// thread's begin offset is propagated to every node reached.
func (d *dfaSim) add(dst *[]dfaThread, node int32, begin int32, sp int) {
	// Fast path: when this node's epsilon-closure is free of position-dependent
	// assertions it was precomputed once at build time, so expanding the thread is a
	// dedup'd append of the cached consuming / accepting nodes — no recursive walk
	// per step. This is the hot path for the common assertion-free pattern (a class
	// run, a literal, an alternation of literals).
	if d.nfa.ctxFree[node] {
		for _, c := range d.nfa.closure[node] {
			if d.th.visited[c] != d.th.gen {
				d.th.visited[c] = d.th.gen
				*dst = append(*dst, dfaThread{node: c, begin: begin})
			}
		}
		return
	}
	if d.th.visited[node] == d.th.gen {
		return
	}
	d.th.visited[node] = d.th.gen
	n := d.nfa.insts[node]
	switch n.op {
	case nfaSplit:
		d.add(dst, int32(n.x), begin, sp)
		d.add(dst, int32(n.y), begin, sp)
	case nfaJmp, nfaSave:
		d.add(dst, int32(n.x), begin, sp)
	case nfaAssertBeginText:
		if sp == 0 {
			d.add(dst, int32(n.x), begin, sp)
		}
	case nfaAssertEndText:
		if sp == len(d.ctx.input) {
			d.add(dst, int32(n.x), begin, sp)
		}
	case nfaAssertEndTextNL:
		if sp == len(d.ctx.input) || (sp == len(d.ctx.input)-1 && d.ctx.input[sp] == '\n') {
			d.add(dst, int32(n.x), begin, sp)
		}
	case nfaAssertBeginLine:
		if sp == 0 || d.ctx.input[sp-1] == '\n' {
			d.add(dst, int32(n.x), begin, sp)
		}
	case nfaAssertEndLine:
		if sp == len(d.ctx.input) || d.ctx.input[sp] == '\n' {
			d.add(dst, int32(n.x), begin, sp)
		}
	case nfaAssertPrevMatch:
		if sp == d.ctx.gpos {
			d.add(dst, int32(n.x), begin, sp)
		}
	}
	// nfaChar / nfaMatch nodes never reach this switch: they are always context-free
	// (their epsilon-closure is themselves), so the fast path above appended them
	// directly. Only the position-dependent epsilon and assertion nodes — the reason
	// a closure is context-dependent — take this slow recursive walk.
}

// dfaSim is one search's executor state.
type dfaSim struct {
	nfa *nfaProg
	th  *dfaThreads
	ctx dfaCtx
	// pf is the same start-locating prefilter the backtracking VM uses (a required
	// literal prefix searched with strings.Index, a constrained first-byte set, or a
	// \A anchor). The DFA consults it to JUMP the scan cursor over positions that
	// provably cannot begin a match instead of stepping the NFA byte by byte through
	// dead input — this is what keeps literal / prefix-anchored scans at
	// strings.Index speed while the NFA inner loop accelerates the matching region.
	pf    prefilter
	usePF bool
}

// atomStep applies the VM's exact per-atom acceptance test for a consuming atom at
// position sp, returning whether it matches and the byte width it consumes. It
// reuses the same logic the backtracking machine uses (foldCharStep / anyStep /
// classStep / propStep / OpChar), so the DFA accepts exactly the bytes the VM
// would.
func (d *dfaSim) atomStep(in compile.Inst, sp int) (bool, int) {
	switch in.Op {
	case compile.OpFoldChar:
		return foldCharStepCtx(d.ctx, in, sp)
	case compile.OpAny:
		return anyStepCtx(d.ctx, in, sp)
	case compile.OpClass:
		return classStepCtx(d.ctx, in, sp)
	case compile.OpUniProp:
		return propStepCtx(d.ctx, in, sp)
	default:
		// OpChar: a nfaChar node only ever carries one of the five consuming atom
		// opcodes, and OpChar is the residual — match the single literal byte.
		if sp < len(d.ctx.input) && d.ctx.input[sp] == in.B {
			return true, 1
		}
		return false, 0
	}
}

// advanceWidth returns how far the unanchored scan advances when no thread is live
// at sp: one whole code point in UTF8 mode at a boundary, otherwise one byte. This
// matches the backtracker's per-start scan, which also tries every byte offset;
// advancing by a code point here only skips offsets that land inside a multi-byte
// sequence, where no rune-aware atom can begin a match anyway, and a byte-oriented
// atom in UTF8 mode also positions on code-point boundaries.
// It is only ever called at a position strictly inside the input (the callers
// break out at end of input first), so it always has a code point to measure.
func (d *dfaSim) advanceWidth(sp int) int {
	if d.ctx.enc == compile.ASCII8BIT {
		return 1
	}
	_, w := utf8.DecodeRuneInString(d.ctx.input[sp:])
	return w
}

// --- context-form atom acceptance tests ---------------------------------- //
// These mirror the machine.*Step methods exactly but take a read-only dfaCtx so
// both the DFA executor and (unchanged) the backtracking VM can share the logic.

func foldCharStepCtx(c dfaCtx, in compile.Inst, sp int) (bool, int) {
	if sp >= len(c.input) {
		return false, 0
	}
	if c.enc == compile.ASCII8BIT {
		b := c.input[sp]
		if in.Rune < 128 && (byte(in.Rune) == b || swapASCIICase(byte(in.Rune)) == b) {
			return true, 1
		}
		return false, 0
	}
	if isContinuationByte(c.input[sp]) {
		return false, 0
	}
	r, w := utf8.DecodeRuneInString(c.input[sp:])
	if charset.FoldEqual(in.Rune, r) {
		return true, w
	}
	return false, 0
}

func anyStepCtx(c dfaCtx, in compile.Inst, sp int) (bool, int) {
	if sp >= len(c.input) {
		return false, 0
	}
	if !in.DotAll && c.input[sp] == '\n' {
		return false, 0
	}
	if c.enc == compile.ASCII8BIT {
		return true, 1
	}
	if isContinuationByte(c.input[sp]) {
		return false, 0
	}
	_, w := utf8.DecodeRuneInString(c.input[sp:])
	return true, w
}

func classStepCtx(c dfaCtx, in compile.Inst, sp int) (bool, int) {
	if sp >= len(c.input) {
		return false, 0
	}
	runeAware := len(in.Props) != 0 || len(in.RuneRanges) != 0 || in.Fold
	if c.enc == compile.ASCII8BIT {
		if !runeAware {
			if classMatch(in, c.input[sp]) {
				return true, 1
			}
			return false, 0
		}
		if classMatchByteRanges(in, c.input[sp]) {
			return true, 1
		}
		return false, 0
	}
	if !runeAware {
		if isContinuationByte(c.input[sp]) {
			return false, 0
		}
		r, w := utf8.DecodeRuneInString(c.input[sp:])
		if rangesContainRune(in.Ranges, r) != in.Negate {
			return true, w
		}
		return false, 0
	}
	if isContinuationByte(c.input[sp]) {
		return false, 0
	}
	r, w := utf8.DecodeRuneInString(c.input[sp:])
	if classMatchRune(in, r) {
		return true, w
	}
	return false, 0
}

func propStepCtx(c dfaCtx, in compile.Inst, sp int) (bool, int) {
	if sp >= len(c.input) {
		return false, 0
	}
	if c.enc == compile.ASCII8BIT {
		b := c.input[sp]
		inSet := b < 0x80 && charset.Match(in.Prop.Name, false, rune(b))
		if inSet != in.Prop.Negate {
			return true, 1
		}
		return false, 0
	}
	if isContinuationByte(c.input[sp]) {
		return false, 0
	}
	r, w := utf8.DecodeRuneInString(c.input[sp:])
	if charset.Match(in.Prop.Name, in.Prop.Negate, r) {
		return true, w
	}
	return false, 0
}
