package onigmo

import (
	"sync"
	"time"

	"github.com/go-ruby-regexp/regexp/internal/compile"
	"github.com/go-ruby-regexp/regexp/internal/syntax"
	"github.com/go-ruby-regexp/regexp/internal/vm"
)

// machine holds the heavy, lazily-built matcher state shared by a Regexp and
// every WithTimeout copy of it. Compile validates the pattern's syntax eagerly
// (so a malformed pattern is reported at Compile time, as Ruby's Regexp.new
// does) and stores the parse result here, but defers the expensive lowering —
// building the instruction program, the lazy-NFA/DFA accelerator, and the
// start-locating prefilter — until the first match. A Regexp that is compiled
// but never matched (or matched once) therefore pays only the parse cost, not
// the full machine build, which the compile-time microbenchmarks show is 5–76×
// the parse alone.
//
// build is guarded by sync.Once so the one-time lowering is race-free even when
// a freshly-compiled Regexp is shared across goroutines and matched
// concurrently: every caller observes a fully-built prog/dfa or blocks until the
// single builder finishes. The result is immutable thereafter, so subsequent
// concurrent matches read it without synchronisation.
type machine struct {
	once sync.Once
	res  syntax.Result // parsed AST + capture metadata, retained for the deferred lowering
	enc  compile.Encoding
	prog *compile.Program
	// dfa is the lazy-NFA accelerator for the matchable subset (no backreference,
	// call, lookaround, atomic group, or over-large bounded loop). It finds the
	// leftmost-first whole-match span in linear time, one step per input position,
	// replacing the backtracking VM's per-character dispatch for the search /
	// is-match case and for locating the bounds the VM is anchored to when
	// submatches are needed. It is nil when the program is outside the subset, in
	// which case every match runs on the backtracking VM. Built once on first use.
	dfa *vm.DFA
}

// build performs the deferred lowering exactly once. It lowers the retained
// parse result into the instruction program and builds the DFA accelerator
// (which may be nil for a program outside the DFA subset or one a literal
// prefilter serves better). Safe under concurrent callers via sync.Once.
func (m *machine) build() {
	m.once.Do(func() {
		m.prog = compile.CompileEnc(m.res, m.enc)
		m.dfa = vm.BuildDFA(m.prog)
	})
}

// Regexp is a compiled regular expression, safe for concurrent use by multiple
// goroutines. A Regexp is immutable once compiled; WithTimeout returns a copy
// carrying a wall-clock match limit rather than mutating the receiver, so a
// shared Regexp stays concurrency-safe. The heavy matcher state lives behind the
// shared *machine, built lazily on first match; the copy WithTimeout returns
// shares that machine, so a timeout variant never rebuilds it.
type Regexp struct {
	m      *machine
	source string
	// enc is stored here so Encoding() (and syntax-only introspection) can answer
	// without forcing the deferred machine build.
	enc compile.Encoding
	// timeout is the wall-clock limit for a single match (Ruby's Regexp.timeout
	// equivalent). Zero means no time limit. It is set only by WithTimeout, which
	// returns a copy, keeping a shared Regexp immutable.
	timeout time.Duration
}

// Encoding selects how the byte-oriented input-advancing atoms — the dot (`.`)
// and a byte-oriented character class — traverse the input, the way Ruby's
// Regexp#encoding governs matching on a UTF-8 vs a binary (ASCII-8BIT) string.
//
// In UTF8 (the default) the dot and byte-oriented classes advance by a whole
// UTF-8 code point, so `/./` matches a complete multi-byte character (it matches
// "é" as one unit, exactly as MRI does on a UTF-8 string) and `[^a]` consumes a
// whole character rather than a single byte. In ASCII8BIT (Ruby's /n binary
// encoding) every atom advances one byte, and Unicode case-folding (/i) and
// \p{…} properties operate per byte (ASCII-only). Match offsets are byte offsets
// in both modes.
type Encoding = compile.Encoding

const (
	// UTF8 is the default encoding: the dot and byte-oriented classes advance by
	// a whole UTF-8 code point.
	UTF8 = compile.UTF8
	// ASCII8BIT is Ruby's binary (/n) encoding: every atom advances one byte.
	ASCII8BIT = compile.ASCII8BIT
)

// Compile parses a pattern and returns a compiled Regexp in the default UTF-8
// encoding, or an error if the pattern is malformed.
func Compile(pattern string) (*Regexp, error) {
	return CompileEnc(pattern, UTF8)
}

// CompileEnc is Compile with an explicit input encoding (see Encoding). UTF8
// makes the dot and byte-oriented classes advance by a whole code point;
// ASCII8BIT makes every atom advance one byte, matching Ruby's /n.
func CompileEnc(pattern string, enc Encoding) (*Regexp, error) {
	// Parse eagerly: this validates the whole pattern (backreference bounds,
	// \g<…> call resolution, balanced groups, …) so a malformed pattern is
	// reported here — at Compile time, exactly as Ruby's Regexp.new raises
	// RegexpError rather than deferring the error to the first match. Only the
	// expensive machine build (instruction program + DFA + prefilter) is deferred
	// to first use; the parse result is retained so that build can lower it.
	res, err := syntax.ParseEnc(pattern, enc)
	if err != nil {
		return nil, err
	}
	return &Regexp{m: &machine{res: res, enc: enc}, source: pattern, enc: enc}, nil
}

// Encoding returns the input encoding the Regexp matches under (Ruby's
// Regexp#encoding equivalent): UTF8 by default, ASCII8BIT for a binary pattern.
// It is answered from the stored encoding and does not trigger the deferred
// machine build.
func (re *Regexp) Encoding() Encoding { return re.enc }

// String returns the source pattern the Regexp was compiled from.
func (re *Regexp) String() string { return re.source }

// Timeout returns the wall-clock limit applied to a single match, or zero if no
// limit is set.
func (re *Regexp) Timeout() time.Duration { return re.timeout }

// WithTimeout returns a copy of the Regexp that aborts any single match taking
// longer than d of wall-clock time (Ruby's Regexp.timeout equivalent), returning
// no match. A non-positive d clears the limit. The copy shares the compiled
// program with the receiver, which is left unchanged, so a Regexp can be shared
// across goroutines and given per-use timeouts without data races.
func (re *Regexp) WithTimeout(d time.Duration) *Regexp {
	cp := *re
	if d <= 0 {
		cp.timeout = 0
	} else {
		cp.timeout = d
	}
	return &cp
}

// deadline turns the configured timeout into an absolute instant for this match,
// or the zero time when there is no limit.
func (re *Regexp) deadline() time.Time {
	if re.timeout <= 0 {
		return time.Time{}
	}
	return time.Now().Add(re.timeout)
}

// Match searches s for the leftmost match and returns a *MatchData, or nil if
// there is no match. The search scans start positions left to right and, at the
// first position that matches, returns the greedy leftmost-first match (Ruby /
// Onigmo semantics).
//
// If the Regexp carries a timeout (see WithTimeout) and the search exceeds it,
// or the internal step budget is exhausted, Match returns nil — a pathological
// pattern is bounded rather than allowed to run unboundedly.
func (re *Regexp) Match(s string) *MatchData {
	m := re.m
	m.build()
	// Capture-free subset: the lazy NFA's leftmost-first span IS the whole answer
	// (there are no submatches to extract), so skip the backtracking VM entirely and
	// take the linear-time path. It is bounded by construction, so a configured
	// timeout never needs to fire on it.
	if m.dfa != nil && m.prog.NumCapture == 0 {
		b, e, ok := m.dfa.Search(s, m.prog.Enc, 0)
		if !ok {
			return nil
		}
		return spanMatch(s, b, e, m.prog.Names)
	}
	caps, ok, err := vm.MatchTimeout(m.prog, s, vm.DefaultBudget, re.deadline())
	if err != nil || !ok {
		return nil
	}
	return &MatchData{input: s, caps: caps, ngroups: m.prog.NumCapture, names: m.prog.Names}
}

// MatchAt attempts a match anchored exactly at byte offset pos in s, with \G
// bound to pos. It does not scan forward: it matches at pos or returns nil. The
// whole string s stays visible to the matcher, so the line/text anchors (^, \A)
// and lookbehind see the real prefix s[:pos] — exactly the semantics a
// StringScanner-style tokenizer needs (a Rouge RegexLexer scanning a buffer in
// place). Group offsets in the returned MatchData are absolute into s.
//
// This is the faithful primitive for cursor-anchored lexing: pattern ^ matches
// only at a genuine line start, not merely at pos. A timeout or exhausted step
// budget yields nil, as with Match.
func (re *Regexp) MatchAt(s string, pos int) *MatchData {
	if pos < 0 || pos > len(s) {
		return nil
	}
	m := re.m
	m.build()
	// Capture-free subset: the lazy NFA's anchored-at-pos span IS the whole answer
	// (there are no submatches to extract), so skip the backtracking VM entirely and
	// take the linear-time path with pooled, allocation-free per-call state. This is
	// the hot path for a tokenizing scan loop (Scan/Skip/match? re-matching from an
	// advancing cursor over capture-free class/quantifier patterns), which otherwise
	// paid a fresh backtracking machine + capture array + memo on every call.
	if m.dfa != nil && m.prog.NumCapture == 0 {
		b, e, ok := m.dfa.MatchAt(s, m.prog.Enc, pos)
		if !ok {
			return nil
		}
		return spanMatch(s, b, e, m.prog.Names)
	}
	caps, ok, err := vm.MatchAt(m.prog, s, pos, vm.DefaultBudget)
	if err != nil || !ok {
		return nil
	}
	return &MatchData{input: s, caps: caps, ngroups: m.prog.NumCapture, names: m.prog.Names}
}

// MatchBoundsAt is the allocation-free, bounds-only form of MatchAt: it reports
// the whole match's [begin, end) byte span for a match anchored exactly at pos
// (begin == pos on success), without building a MatchData or extracting
// submatches. It is the primitive for the cursor-anchored StringScanner ops that
// need only a length or a yes/no — skip(/…/), match?(/…/), an anchored scan whose
// captured text the caller ignores — mirroring Ruby's StringScanner#skip /
// #match?, which likewise return an integer rather than a MatchData. When the
// program is in the lazy-NFA subset (no backreference, call, lookaround, atomic
// group, or over-large bounded loop) the span is found by the linear-time NFA on
// pooled state, so a tokenizing loop over such a pattern allocates nothing per
// call; otherwise it falls to the backtracking VM. The span is identical to
// MatchAt(s, pos).Begin(0)/End(0). pos out of range yields ok == false.
func (re *Regexp) MatchBoundsAt(s string, pos int) (begin, end int, ok bool) {
	if pos < 0 || pos > len(s) {
		return 0, 0, false
	}
	m := re.m
	m.build()
	// The DFA reports the whole-match bounds for every program it accepts, including
	// one with capturing groups (whether a match exists and where it spans never
	// depends on which text the groups captured), so the bounds-only path uses it
	// whenever it was built — not just for the capture-free case.
	if m.dfa != nil {
		return m.dfa.MatchAt(s, m.prog.Enc, pos)
	}
	caps, ok, err := vm.MatchAt(m.prog, s, pos, vm.DefaultBudget)
	if err != nil || !ok {
		return 0, 0, false
	}
	return caps[0], caps[1], true
}

// MatchBounds is the allocation-free, bounds-only form of Match: it scans s left
// to right for the leftmost match and returns its whole-match [begin, end) byte
// span, without building a MatchData or extracting submatches. It is the
// primitive for a forward StringScanner scan_until / skip_until whose captured
// text the caller ignores. On the lazy-NFA subset the search is the linear-time
// NFA on pooled state (no per-call allocation); otherwise it falls to the
// backtracking VM (bounded by the step budget). The span is identical to
// Match(s).Begin(0)/End(0).
func (re *Regexp) MatchBounds(s string) (begin, end int, ok bool) {
	m := re.m
	m.build()
	if m.dfa != nil {
		return m.dfa.Search(s, m.prog.Enc, 0)
	}
	caps, ok, err := vm.MatchTimeout(m.prog, s, vm.DefaultBudget, re.deadline())
	if err != nil || !ok {
		return 0, 0, false
	}
	return caps[0], caps[1], true
}

// MatchString reports whether s contains a match of the regular expression. When
// the program is in the lazy-NFA subset (no backreference, call, lookaround,
// atomic group, or over-large bounded loop) the is-match question is answered by
// the linear-time NFA simulation rather than the backtracking VM — one step per
// input position — even for a pattern with capturing groups, since whether a match
// exists never depends on which text the groups captured. The backtracking VM is
// used only for programs outside the subset.
func (re *Regexp) MatchString(s string) bool {
	m := re.m
	m.build()
	if m.dfa != nil {
		_, _, ok := m.dfa.Search(s, m.prog.Enc, 0)
		return ok
	}
	return re.Match(s) != nil
}

// MatchData holds the result of a successful match: the byte spans of the whole
// match (group 0) and of each capturing group.
type MatchData struct {
	input string
	caps  []int
	// grp0 backs caps for the capture-free fast paths (Match / MatchAt / the DFA
	// is-match cases), so the whole-match span rides in the MatchData's own single
	// heap allocation instead of a separate []int{b,e}. spanMatch points caps at
	// grp0[:]; the backtracking VM path leaves grp0 zero and caps referencing the
	// VM's own slice. Accessors read caps uniformly either way, so results are
	// identical.
	grp0    [2]int
	ngroups int
	names   map[string]int
}

// spanMatch builds a MatchData for a capture-free result (group 0 only). It keeps
// the [begin,end) span inside the struct's own grp0 array and slices caps over it,
// so a successful match allocates just the one MatchData — no second []int — which
// matters on the tokenizing hot loop that produces one match per token.
func spanMatch(s string, b, e int, names map[string]int) *MatchData {
	md := &MatchData{input: s, ngroups: 0, names: names}
	md.grp0[0], md.grp0[1] = b, e
	md.caps = md.grp0[:]
	return md
}

// NGroups returns the number of capturing groups, not counting group 0 (the
// whole match).
func (m *MatchData) NGroups() int { return m.ngroups }

// IndexOfName returns the 1-based group index for a named capture, or -1 if no
// group has that name.
func (m *MatchData) IndexOfName(name string) int {
	if i, ok := m.names[name]; ok {
		return i
	}
	return -1
}

// StrName returns the substring captured by the named group, or "" if there is
// no such name or the group did not participate.
func (m *MatchData) StrName(name string) string {
	return m.Str(m.IndexOfName(name))
}

// Begin returns the byte offset of the start of group i, or -1 if the group did
// not participate in the match. Group 0 is the whole match. An out-of-range
// index returns -1.
func (m *MatchData) Begin(i int) int {
	if i < 0 || 2*i >= len(m.caps) {
		return -1
	}
	return m.caps[2*i]
}

// End returns the byte offset just past the end of group i, or -1 if the group
// did not participate. An out-of-range index returns -1.
func (m *MatchData) End(i int) int {
	if i < 0 || 2*i+1 >= len(m.caps) {
		return -1
	}
	return m.caps[2*i+1]
}

// Str returns the substring matched by group i, or the empty string if the
// group did not participate or the index is out of range.
func (m *MatchData) Str(i int) string {
	b, e := m.Begin(i), m.End(i)
	if b < 0 || e < 0 {
		return ""
	}
	return m.input[b:e]
}

// Pre returns the portion of the input before the whole match.
func (m *MatchData) Pre() string {
	return m.input[:m.Begin(0)]
}

// Post returns the portion of the input after the whole match.
func (m *MatchData) Post() string {
	return m.input[m.End(0):]
}
