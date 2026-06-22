package vm

import (
	"strings"

	"github.com/go-ruby-regexp/regexp/internal/compile"
)

// byteSet is a 256-bit bitset over the possible first byte of a match. It is the
// prefilter's coarse "can a match begin with this byte?" oracle.
type byteSet [4]uint64

func (s *byteSet) add(b byte) { s[b>>6] |= 1 << (b & 63) }

func (s *byteSet) has(b byte) bool { return s[b>>6]&(1<<(b&63)) != 0 }

func (s *byteSet) addRange(lo, hi byte) {
	for i := int(lo); i <= int(hi); i++ {
		s.add(byte(i))
	}
}

// count returns how many bytes are in the set.
func (s *byteSet) count() int {
	n := 0
	for _, w := range s {
		n += popcount(w)
	}
	return n
}

func popcount(x uint64) int {
	n := 0
	for x != 0 {
		x &= x - 1
		n++
	}
	return n
}

// prefilter is a precomputed, purely-advisory accelerator for the start-position
// scan. It never changes which positions can match — it only lets the search
// skip positions that provably cannot begin a match, jumping the cursor forward
// to the next viable offset before the full backtracking VM is invoked there.
//
// It carries, in priority order:
//   - anchored: the pattern begins with \A (OpAssertBeginText), so only start
//     position 0 can ever match; every later start is skipped outright.
//   - prefix: a non-empty required literal byte string every match must start
//     with, searched for with strings.Index (Boyer–Moore-ish in the runtime).
//   - first: a set of possible first bytes; when no literal prefix is available
//     but the leading byte is constrained, a byte scan skips to the next position
//     whose byte is in the set.
//   - required: a fixed substring that must appear SOMEWHERE in every match, even
//     when the pattern has no anchored or leading literal (e.g. the "foo" in
//     \d+foo\d+). A single strings.Index over the whole haystack rejects inputs
//     that lack it outright; it is a necessary-but-not-sufficient condition, so the
//     VM still verifies every surviving position.
//
// usable reports whether any of these is actually exploitable; when false the
// scan runs unmodified (the fully general slow path), so correctness never
// depends on the analysis being complete.
type prefilter struct {
	anchored bool
	prefix   string
	first    byteSet
	hasFirst bool
	required string
}

// analyze derives a prefilter from a compiled program by walking the single
// linear, unconditional path from the entry. It is deliberately conservative:
// at the first instruction whose contribution to the match's leading bytes it
// cannot determine exactly, it stops, returning only what it proved so far.
// Anything it cannot prove leaves the corresponding field unusable, so the
// scan falls back to the general behaviour and the optimization stays
// transparent.
func analyze(prog *compile.Program) prefilter {
	insts := prog.Insts
	pc := 0
	var pf prefilter
	var lit strings.Builder
	// firstResolved records that the set of possible first bytes has been pinned
	// down by the first byte-consuming instruction on the path; once set, no later
	// instruction can widen it (we have already left the leading position).
	firstResolved := false

	// recordFirst captures, the first time a byte-consuming atom is reached, the
	// set of bytes that atom can start with. addToLit is non-nil only when the atom
	// also contributes a fixed literal byte to the required prefix.
	recordFirst := func(set byteSet) {
		if !firstResolved {
			pf.first = set
			pf.hasFirst = true
			firstResolved = true
		}
	}

loop:
	for pc >= 0 && pc < len(insts) {
		in := insts[pc]
		switch in.Op {
		case compile.OpSave, compile.OpAtomicBegin:
			// A capture open/close or an atomic-group open consumes no input and does
			// not branch; step over it to the next atom.
			pc++
		case compile.OpAssertBeginText:
			// \A at the very front: only offset 0 can match.
			if !firstResolved {
				pf.anchored = true
			}
			pc++
		case compile.OpAssertBeginLine, compile.OpAssertPrevMatch:
			// ^ (no /m as implemented here means start-of-text-or-after-newline) and
			// \G constrain the start too, but not to a single byte set we exploit
			// here; step over them without claiming a prefix.
			pc++
		case compile.OpChar:
			// A fixed leading byte: extend the literal prefix and pin the first-byte
			// set (if not already resolved by an earlier atom — there is none on a
			// pure literal run, so the first OpChar sets it).
			var set byteSet
			set.add(in.B)
			recordFirst(set)
			lit.WriteByte(in.B)
			pc++
		case compile.OpClass:
			// A byte-oriented class (no rune-aware members) yields a first-byte set;
			// a rune-aware one (folded or carrying \p{…}/code-point members) is not
			// reducible to bytes here, so give up the prefix at this atom.
			set, ok := classFirstBytes(in)
			if !ok {
				break loop
			}
			recordFirst(set)
			break loop
		case compile.OpSplit:
			// A split is the leading construct only when no fixed byte has been
			// consumed yet (firstResolved is false). If a literal prefix already
			// preceded it (e.g. ab(c|d)), the first-byte set is already pinned by that
			// prefix, so simply end the analysable prefix here. Otherwise (e.g.
			// foo|bar, an a*-style optional, a leading group) collect the union of the
			// first bytes reachable from this split: if every alternative resolves to
			// a determinable byte set, the union is an exact first-byte oracle; if any
			// branch is non-reducible or can match empty (so a later atom's byte could
			// lead), the whole prefilter is given up.
			if firstResolved {
				break loop
			}
			var set byteSet
			if firstByteSet(insts, pc, &set, 0) {
				recordFirst(set)
			}
			break loop
		case compile.OpLoop:
			// A fused quantifier (e.g. a*b, [a-z]+, a{2,4}c) is the leading construct
			// only when no fixed byte has been consumed yet. If a literal prefix
			// already preceded it the first-byte set is pinned, so just end the
			// analysable prefix. Otherwise derive the loop's first-byte set
			// (firstByteSet handles the atom and, for a Min==0 loop that can match
			// zero reps, the union with the continuation past it); if it is fully
			// byte-determinable, record it. Either way the variable-length run breaks
			// the literal prefix, so the prefix ends here.
			if firstResolved {
				break loop
			}
			var set byteSet
			if firstByteSet(insts, pc, &set, 0) {
				recordFirst(set)
			}
			break loop
		default:
			// Any other instruction (split/alternation, dot, fold, property, look,
			// call, backref, anchors we do not model, …) ends the analysable prefix.
			break loop
		}
	}

	pf.prefix = lit.String()
	pf.required = requiredLiteral(insts)
	return pf
}

// requiredLiteral extracts the longest fixed byte string that provably must
// appear somewhere inside every match, walking only the program's MANDATORY
// SPINE — the linear sequence of instructions that every accepting run is forced
// to execute, in order, regardless of how it backtracks. It collects maximal runs
// of consecutive fixed bytes (OpChar) along that spine and returns the longest.
//
// The spine is what makes the result a *necessary* condition. Each step either:
//   - contributes a fixed byte to the current run (OpChar);
//   - is zero-width and unconditional, so it neither breaks byte contiguity nor
//     leaves the spine (OpSave, OpAtomicBegin/End): the run continues across it;
//   - consumes/asserts exactly one thing unconditionally but is not a fixed byte
//     (OpAny, OpClass, OpFoldChar, OpUniProp, OpBackref, the anchor asserts): the
//     run is broken (flushed as a candidate) but the spine continues, because a
//     later required literal is still mandatory;
//   - is a QUANTIFIER split (Quant, marked at compile time for *, +, ?, {m,n}):
//     its body is optional/repeatable and therefore NOT required, but its GuardTo
//     branch is the deterministic continuation past the whole quantifier, which
//     stays on the spine — so the current run is flushed and the walk jumps there;
//   - is anything else — an ALTERNATION split (a true fork: neither branch is
//     forced), a jump, a lookaround, a call/return, OpMatch — at which the spine
//     forks or ends, so the walk stops.
//
// Because the walk only ever follows forced steps, any byte it emits must be
// matched, in order, by every accepting run; hence the returned literal is a
// genuine necessary substring of every match. A run of length < 2 is not worth a
// whole-haystack scan (the first-byte/prefix filters already cover short anchored
// cases), so only runs of two or more bytes are considered.
func requiredLiteral(insts []compile.Inst) string {
	best := ""
	var run strings.Builder
	flush := func() {
		if run.Len() >= 2 && run.Len() > len(best) {
			best = run.String()
		}
		run.Reset()
	}
	// The walk strictly advances pc on every iteration that does not return: each
	// arm either does pc++ or jumps strictly forward (the quantifier GuardTo and
	// lookaround continuation guards reject a non-advancing target), so the loop is
	// acyclic and bounded by len(insts) with no separate step counter needed.
	pc := 0
	for pc >= 0 && pc < len(insts) {
		in := insts[pc]
		switch in.Op {
		case compile.OpChar:
			run.WriteByte(in.B)
			pc++
		case compile.OpSave, compile.OpAtomicBegin, compile.OpAtomicEnd, compile.OpReturn:
			// Zero-width, unconditional: a capture save, an atomic-group bracket, or a
			// group terminator (which, with no active \g<…> call frame — and the walk
			// never enters one, it stops at OpCall — simply falls through). Bytes on
			// either side stay contiguous, so the run is NOT flushed; the spine
			// continues to the next instruction. This keeps a literal that spans a
			// captured group, e.g. the "foobarbaz" of foo(bar)baz, as one run.
			pc++
		case compile.OpAny, compile.OpClass, compile.OpFoldChar, compile.OpUniProp,
			compile.OpBackref, compile.OpAssertBeginText, compile.OpAssertEndText,
			compile.OpAssertEndTextOptNL, compile.OpAssertBeginLine,
			compile.OpAssertEndLine, compile.OpAssertPrevMatch:
			// Unconditional but not a fixed byte (or a zero-width assertion that breaks
			// byte adjacency): end the current literal run, keep walking the spine.
			flush()
			pc++
		case compile.OpSplit:
			flush()
			if in.Quant {
				// A quantifier's body is optional/repeatable (not required); GuardTo is
				// the forced continuation past the whole quantifier. Follow it only when
				// it advances, so a degenerate GuardTo can never loop the walk.
				if in.GuardTo <= pc {
					return best
				}
				pc = in.GuardTo
				continue
			}
			// An alternation fork: neither branch is forced, so no later byte is
			// required; the spine ends here.
			return best
		case compile.OpLoop:
			// A fused quantifier over a single atom. Its required minimum (Min reps)
			// is forced and so part of the mandatory spine; the optional remainder is
			// not. Only an OpChar atom contributes FIXED bytes: Min copies of its byte
			// are required in order, so append them to the current run (this recovers
			// a{3} -> "aaa" and keeps a{3}b -> "aaab" contiguous when the count is
			// exact). When the count is not exact (Min < Max, or unbounded) the run
			// length past the forced minimum is variable, breaking byte adjacency, so
			// the run is flushed after the forced bytes. A non-OpChar atom (dot, class,
			// fold, property) contributes no fixed byte, and a Min==0 loop forces
			// nothing, so both just flush. The continuation past the loop (in.X) stays
			// on the spine. Follow only forward so a degenerate target cannot loop.
			if in.Sub == compile.OpChar && in.Min >= 1 {
				for i := 0; i < in.Min; i++ {
					run.WriteByte(in.B)
				}
				if in.Max != in.Min {
					flush()
				}
			} else {
				flush()
			}
			if in.X <= pc {
				// Degenerate (non-advancing) continuation: stop, but flush first so a
				// run still open across an exact-count loop (Min == Max) is not lost.
				flush()
				return best
			}
			pc = in.X
		case compile.OpLook:
			// A lookaround is a zero-width, unconditional assertion: its inline body
			// (which OpLook.X skips past) consumes none of the matched span, so its
			// bytes are not part of any literal run and break adjacency. The assertion
			// must hold for a match, and whatever follows it is still on the mandatory
			// spine, so flush and jump to the continuation. Follow only forward so a
			// degenerate target can never loop the walk.
			flush()
			if in.X <= pc {
				return best
			}
			pc = in.X
		default:
			// OpJmp, OpLookEnd (only reached as a stray, since OpLook jumps over its
			// body), OpCall/OpReturn, OpMatch: the spine forks, leaves the matched span,
			// or ends. Stop.
			flush()
			return best
		}
	}
	flush()
	return best
}

// classFirstBytes returns the set of bytes a byte-oriented OpClass can match,
// and whether the class is byte-reducible at all. A class carrying \p{…}
// members, explicit code-point ranges, or folded under /i is rune-aware and not
// reducible to a flat byte set here (its leading byte depends on UTF-8 encoding
// of code points), so ok is false and the caller gives up the prefilter for it.
func classFirstBytes(in compile.Inst) (byteSet, bool) {
	if len(in.Props) != 0 || len(in.RuneRanges) != 0 || in.Fold {
		return byteSet{}, false
	}
	var set byteSet
	for _, r := range in.Ranges {
		set.addRange(r.Lo, r.Hi)
	}
	if in.Negate {
		// The class matches every byte NOT in the ranges. A negated byte class can
		// still match high bytes, so the set is the complement.
		var neg byteSet
		for i := 0; i < 256; i++ {
			if !set.has(byte(i)) {
				neg.add(byte(i))
			}
		}
		set = neg
	}
	return set, true
}

// maxFirstByteDepth bounds the recursion of firstByteSet so an adversarial nest
// of leading splits cannot make analysis (run once at compile time) blow the
// stack. Beyond it the set is declared non-reducible and the prefilter is given
// up — never an incorrect skip, only a missed optimization.
const maxFirstByteDepth = 64

// firstByteSet walks the program from pc following only zero-width pass-through
// instructions and the branches of a split, adding to set the bytes every
// reachable leading atom can start with. It reports whether the leading byte is
// fully determinable: true means set is an exact union over all alternatives, so
// a match from here must begin with a byte in set; false means some reachable
// path is not byte-reducible (a rune-aware atom, the dot, a backref, a call, a
// lookaround, a loop back-edge, or a branch that can match empty so a later
// atom's byte could lead) and the caller must give up.
//
// It is the alternation generalization of the single-atom first-byte derivation:
// for foo|bar it unions {f} and {b}; for [ax]|[by] it unions the two classes; it
// refuses a|.b (dot branch) or (a|)b (an empty branch lets b lead).
func firstByteSet(insts []compile.Inst, pc int, set *byteSet, depth int) bool {
	if depth > maxFirstByteDepth || pc < 0 || pc >= len(insts) {
		return false
	}
	for {
		in := insts[pc]
		switch in.Op {
		case compile.OpSave, compile.OpAtomicBegin:
			// Zero-width, unconditional: a capture open/close or an atomic-group open
			// does not consume input or branch, so step over it to the next atom.
			pc++
			if pc >= len(insts) {
				return false
			}
		case compile.OpChar:
			set.add(in.B)
			return true
		case compile.OpClass:
			cs, ok := classFirstBytes(in)
			if !ok {
				return false
			}
			orInto(set, cs)
			return true
		case compile.OpSplit:
			// Both branches are reachable leading positions; the union of their first
			// bytes is the first-byte set of the alternation/optional. A bounded
			// recursion follows each; either being non-reducible fails the whole set.
			if !firstByteSet(insts, in.X, set, depth+1) {
				return false
			}
			return firstByteSet(insts, in.Y, set, depth+1)
		case compile.OpLoop:
			// A fused quantifier over a single atom. The atom's first byte must lead
			// every repetition, so its byte set contributes; only an OpChar/OpClass
			// atom is byte-reducible (the dot, a fold, or a property is not). When the
			// loop can match zero reps (Min == 0) the continuation past it (in.X) can
			// lead instead, so its first bytes must be unioned in too; a non-reducible
			// or empty-matching continuation then fails the whole set, exactly as a
			// possibly-empty optional does.
			if !loopAtomFirstBytes(in, set) {
				return false
			}
			if in.Min == 0 {
				return firstByteSet(insts, in.X, set, depth+1)
			}
			return true
		case compile.OpJmp:
			// Follow the unconditional jump (the tail of an alternation branch). A
			// back-edge (jmp to an earlier pc, the loop of a *-quantifier) would lead
			// to a position that can match empty and let a later atom's byte lead, so
			// refuse it rather than risk an unsound set.
			if in.X <= pc {
				return false
			}
			pc = in.X
		default:
			// Any other op as a leading atom — the dot, a fold/property/rune-aware
			// atom, a backref, a call, a lookaround, an anchor, OpMatch (an empty
			// alternative), … — is not reducible to a flat byte set here.
			return false
		}
	}
}

// loopAtomFirstBytes unions, into set, the bytes the repeated atom of a fused
// OpLoop can begin with, reporting whether that atom is byte-reducible at all.
// The loop carries its atom inline: in.Sub names the atom's real opcode and the
// atom's own fields (B for OpChar, the class fields for OpClass) live on the same
// Inst. An OpChar atom contributes its single byte; an OpClass atom its
// (byte-reducible) class set. A dot, a fold, or a property atom depends on UTF-8
// encoding of code points and is not reducible here, so ok is false and the
// caller gives up the prefilter for it.
func loopAtomFirstBytes(in compile.Inst, set *byteSet) bool {
	switch in.Sub {
	case compile.OpChar:
		set.add(in.B)
		return true
	case compile.OpClass:
		cs, ok := classFirstBytes(in)
		if !ok {
			return false
		}
		orInto(set, cs)
		return true
	}
	return false
}

// orInto unions src into dst.
func orInto(dst *byteSet, src byteSet) {
	for i := range dst {
		dst[i] |= src[i]
	}
}

// nextStart returns the smallest start position >= from at which a match could
// begin, using the prefilter, or -1 if no such position exists (the scan can
// stop). When the prefilter proves nothing useful it returns from unchanged, so
// the caller tries every position exactly as the unoptimized scan would.
//
// It is purely advisory: every position it returns must still be tried by the
// VM, and it must never skip a position that could match. Anchoring and a
// literal prefix are exact necessary conditions for a match to start at a given
// offset, so skipping the positions between them is sound.
func (pf prefilter) nextStart(input string, from int) int {
	if pf.anchored {
		// Only offset 0 can match \A. Any request to start past 0 means the whole
		// scan is exhausted.
		if from == 0 {
			return 0
		}
		return -1
	}
	if pf.prefix != "" {
		i := strings.Index(input[from:], pf.prefix)
		if i < 0 {
			return -1
		}
		return from + i
	}
	if pf.hasFirst {
		// A constrained first byte that is not a single fixed literal: scan forward
		// for the next byte in the set. A full set (every byte possible) is treated
		// as no constraint by the caller (usable() is false), so this loop only runs
		// when it can actually skip positions.
		for i := from; i < len(input); i++ {
			if pf.first.has(input[i]) {
				return i
			}
		}
		// No in-set byte remains. The empty match at len(input) is still possible
		// only if the pattern can match empty there, but a usable first-byte set
		// means the first atom consumes a byte, so end-of-input cannot start a
		// match; the scan is exhausted.
		return -1
	}
	return from
}

// usable reports whether the prefilter can actually skip any work. An anchored
// pattern always can (it collapses the scan to one position). A non-empty
// literal prefix always can. A first-byte set helps only when it is a proper
// subset of all 256 bytes; a full set would never skip a position, so it is
// reported unusable and the scan stays on its plain path.
func (pf prefilter) usable() bool {
	if pf.anchored || pf.prefix != "" {
		return true
	}
	if pf.hasFirst && pf.first.count() < 256 {
		return true
	}
	// A required interior literal cannot locate a start position, but it lets the
	// caller reject a haystack that lacks it; report it usable so the scan path is
	// taken (the whole-haystack gate then short-circuits a missing literal).
	if pf.required != "" {
		return true
	}
	return false
}
