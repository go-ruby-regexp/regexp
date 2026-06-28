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

// dfaThread is one live NFA thread: the node it sits on, the input offset its
// match began at (so a completed match reports the right begin for the unanchored
// scan), and the absolute input offset `at` at which the thread is positioned —
// i.e. where its consuming node will read next. Threads are held in a priority
// -ordered list; earlier means preferred.
//
// `at` exists because this engine's consuming atoms have VARIABLE byte width: a
// byte-oriented OpChar always advances one byte, but a rune-aware atom (the dot
// OpAny, OpFoldChar, a rune-aware OpClass, OpUniProp) advances a whole UTF-8 code
// point — 1 to 4 bytes. Two threads alive at the same code-point boundary can
// therefore consume DIFFERENT widths (e.g. `a|γ.` on "γγ": the dot consumes the
// 2-byte γ while a freshly-seeded byte-literal start consumes 1 byte), so their
// successors land at different offsets and a single shared step width is wrong
// (it truncated the dot's span by a byte). The executor instead advances the
// cursor to the MINIMUM successor offset each step and carries any thread that
// landed further ahead untouched until the cursor reaches it.
type dfaThread struct {
	node  int32
	begin int32
	at    int32
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
				*dst = append(*dst, dfaThread{node: c, begin: begin, at: int32(sp)})
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
	case nfaAssertWordBoundary:
		if wordBoundaryAt(d.ctx.input, d.ctx.enc, sp) {
			d.add(dst, int32(n.x), begin, sp)
		}
	case nfaAssertNonWordBoundary:
		if !wordBoundaryAt(d.ctx.input, d.ctx.enc, sp) {
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

// search is the leftmost-first NFA simulation: a per-step Pike-VM scan that
// recomputes each position's frontier from the live thread list. It is the
// multi-byte-path engine — the cached DFA in dfa_cache_run.go falls back to one of
// these per-step transitions for every multi-byte UTF-8 position, and a haystack
// that is mostly multi-byte would fall back at almost every position (paying a
// state-intern per position), so the cached driver detects that case and runs the
// whole search here instead, where every position is handled uniformly with no
// per-position interning or allocation. It produces the identical leftmost-first
// [begin, end) span the cached driver (and the backtracking VM) produces.
// anchored restricts the scan to start offset 0.
func (d *dfaSim) search(anchored bool) (int, int, bool) {
	input := d.ctx.input
	matchBegin, matchEnd := -1, -1

	// seed locates the next position at or after `at` where a fresh start thread
	// should be planted, using the prefilter to skip provably-dead input (a literal
	// prefix located by strings.Index, a constrained first byte, or a \A anchor). It
	// returns -1 when no further start can match, so the caller can stop the scan
	// rather than grind to end-of-input. When the prefilter is unusable it returns
	// `at` unchanged (every position is a candidate).
	seed := func(at int) int {
		if anchored {
			if at == 0 {
				return 0
			}
			return -1
		}
		if d.usePF {
			return d.pf.nextStart(input, at)
		}
		return at
	}

	// Plant the first start thread at the first viable position. If the prefilter
	// can already prove no start is viable, the search is over.
	sp := seed(0)
	if sp < 0 {
		return -1, -1, false
	}
	d.th.bump()
	d.th.clist = d.th.clist[:0]
	d.add(&d.th.clist, int32(d.nfa.start), int32(sp), sp)

	for {
		// A match is fixed once no higher-priority (earlier-listed) thread survives to
		// possibly extend it: clist empty after a match means done.
		if len(d.th.clist) == 0 {
			if matchBegin >= 0 {
				break
			}
			// No live thread and no match yet: jump the cursor to the next viable start
			// (prefilter-driven) rather than stepping dead bytes one at a time.
			ns := seed(sp)
			if ns < 0 {
				break
			}
			sp = ns
			d.th.bump()
			d.add(&d.th.clist, int32(d.nfa.start), int32(sp), sp)
			if len(d.th.clist) == 0 {
				// The start closure produced no waiting thread (e.g. an unsatisfiable
				// leading assertion at this position). Advance and retry; guard against a
				// non-advancing seed by stepping at least one position.
				if sp >= len(input) {
					break
				}
				// advanceWidth returns the exact width of the code point at sp (sp is
				// strictly inside the input here), so the cursor lands on the next
				// boundary, never past end of input.
				sp += d.advanceWidth(sp)
				continue
			}
		}

		d.th.bump()
		d.th.nlist = d.th.nlist[:0]
		// nextSP is the smallest landing offset any surviving successor (or carried
		// thread) wants the cursor to advance to. Because consuming atoms have variable
		// width, threads alive at sp can produce successors at sp+1 … sp+4; the cursor
		// must advance to the MINIMUM of those so a thread that landed further ahead is
		// re-examined at its own offset rather than read a byte early. -1 means "no
		// successor yet".
		nextSP := -1
		advance := func(to int) {
			if nextSP < 0 || to < nextSP {
				nextSP = to
			}
		}
		consumed := false
		for i := 0; i < len(d.th.clist); i++ {
			t := d.th.clist[i]
			// A thread positioned ahead of the cursor is not active yet: carry it forward
			// untouched and let it advance the target so the cursor reaches it. Its node
			// is preserved in priority order in nlist.
			if int(t.at) > sp {
				d.th.nlist = append(d.th.nlist, t)
				advance(int(t.at))
				continue
			}
			n := d.nfa.insts[t.node]
			if n.op == nfaMatch {
				matchBegin, matchEnd = int(t.begin), sp
				break // lower-priority threads cannot win
			}
			if sp >= len(input) {
				continue // nothing to consume at end of input
			}
			ok, w := d.atomStep(n.inst, sp)
			if ok {
				// The successor's closure is evaluated at its OWN landing position (sp+w) so
				// assertions there see the right context and its `at` is recorded for the
				// min-offset advance.
				d.add(&d.th.nlist, int32(n.x), t.begin, sp+w)
				advance(sp + w)
				consumed = true
			}
		}

		if sp >= len(input) {
			break
		}
		if !consumed && nextSP < 0 {
			// No thread consumed at this position and none was carried ahead. Swap in the
			// (now empty) nlist; the loop top finishes the search (if a match is already
			// fixed) or prefilter-jumps to the next viable start. When still hunting,
			// advance the cursor past this position first. advanceWidth is exact (sp is
			// inside the input, the sp>=len case broke above), so the cursor never
			// overshoots end of input.
			d.th.clist, d.th.nlist = d.th.nlist, d.th.clist
			if matchBegin < 0 {
				sp += d.advanceWidth(sp)
			}
			continue
		}
		// Advance the cursor to the nearest successor/carried offset. A carried thread
		// (at > old sp) may pin nextSP without any consumption this step, so nextSP is
		// always set here (consumed || a carry advanced it).
		newSP := nextSP
		// Seed a new start thread for the next position (lowest priority) unless a match
		// is found or we are anchored. It begins at the new cursor; the prefilter is not
		// consulted here: once any thread is live the scan must visit every following
		// position to honour a thread already in progress, and seeding each is cheap
		// (dedup'd by the visited set).
		if matchBegin < 0 && !anchored {
			d.add(&d.th.nlist, int32(d.nfa.start), int32(newSP), newSP)
		}
		d.th.clist, d.th.nlist = d.th.nlist, d.th.clist
		sp = newSP
	}
	if matchBegin < 0 {
		return -1, -1, false
	}
	return matchBegin, matchEnd, true
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

// wordBoundaryAt reports whether position sp in input is a word boundary (\b):
// the character ending just before sp and the character starting at sp differ in
// word-ness, where one side being a string edge counts as a non-word side. The
// word-character notion mirrors Onigmo/MRI's \b exactly — Unicode-aware in UTF8
// mode (\p{Word}: letter, mark, decimal number, or connector punctuation) and
// ASCII-only in ASCII8BIT (/n) mode ([0-9A-Za-z_]). Note this is deliberately
// MRI's own \b rule, which is Unicode-aware in UTF8 mode even though \w is
// ASCII-only there. \B is the complement (!wordBoundaryAt).
func wordBoundaryAt(input string, enc compile.Encoding, sp int) bool {
	return wordCharBefore(input, enc, sp) != wordCharAfter(input, enc, sp)
}

// wordCharBefore reports whether the character ending at offset sp is a word
// character. The empty-prefix edge (sp == 0) is a non-word side.
func wordCharBefore(input string, enc compile.Encoding, sp int) bool {
	if sp <= 0 {
		return false
	}
	if enc == compile.ASCII8BIT {
		return asciiWordByte(input[sp-1])
	}
	r, _ := utf8.DecodeLastRuneInString(input[:sp])
	return charset.Match("Word", false, r)
}

// wordCharAfter reports whether the character starting at offset sp is a word
// character. The end-of-string edge (sp >= len) is a non-word side.
func wordCharAfter(input string, enc compile.Encoding, sp int) bool {
	if sp >= len(input) {
		return false
	}
	if enc == compile.ASCII8BIT {
		return asciiWordByte(input[sp])
	}
	r, _ := utf8.DecodeRuneInString(input[sp:])
	return charset.Match("Word", false, r)
}

// asciiWordByte reports whether b is an ASCII word byte ([0-9A-Za-z_]), the /n
// (ASCII8BIT) notion of a \b word character.
func asciiWordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z')
}
