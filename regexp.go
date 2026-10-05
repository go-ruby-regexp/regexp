// Package onigmo is the Ruby-flavoured face of the pure-Go Onigmo regular
// expression engine. It is a thin wrapper over github.com/go-regexp/engine — the
// idiomatic-Go engine extracted so that both Go consumers and this Ruby binding
// share a single matcher core — presenting the Ruby/Onigmo surface that rbgo
// expects: Regexp.new-style eager compilation, a MatchData with 1-based group
// numbering and pre/post-match text, and Regexp#encoding / Regexp.timeout.
package onigmo

import (
	"time"

	engine "github.com/go-regexp/engine"
)

// Regexp is a compiled regular expression, safe for concurrent use by multiple
// goroutines. It is immutable once compiled; WithTimeout returns a copy carrying
// a wall-clock match limit rather than mutating the receiver, so a shared Regexp
// stays concurrency-safe. The heavy matcher state (instruction program, lazy
// NFA/DFA accelerator, prefilter) lives in the underlying engine, built lazily on
// first match; the copy WithTimeout returns shares that state.
type Regexp struct {
	re *engine.Regexp
	// names maps a capture name to its 1-based group index (Ruby numbering),
	// precomputed once at compile so MatchData lookups need no engine call.
	names map[string]int
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
type Encoding = engine.Encoding

const (
	// UTF8 is the default encoding: the dot and byte-oriented classes advance by
	// a whole UTF-8 code point.
	UTF8 = engine.UTF8
	// ASCII8BIT is Ruby's binary (/n) encoding: every atom advances one byte.
	ASCII8BIT = engine.ASCII8BIT
)

// ErrTimeout and ErrBudget are the two reasons a match can be abandoned rather
// than decided: ErrTimeout when the wall-clock limit set by WithTimeout elapsed,
// ErrBudget when the engine's deterministic backtrack-step budget was exhausted.
// They are reported only by the …Err match methods (MatchErr, MatchAtErr,
// MatchStringErr, MatchBoundsErr, MatchBoundsAtErr); every other match method
// folds them into "no match".
//
// A caller using a Regexp as a validator, a denylist or any other guard MUST use
// an …Err method and treat a non-nil error as a refusal. Folded into no-match, an
// abandoned search on a crafted subject reads as "the guard did not fire", which
// is fail-open — this is what Ruby's Regexp::TimeoutError exists to prevent.
// Note that ErrBudget needs no timeout to be configured: it is reached on a
// catastrophically backtracking pattern under the engine's default budget.
// Compare with errors.Is.
var (
	ErrTimeout = engine.ErrTimeout
	ErrBudget  = engine.ErrBudget
)

// Compile parses a pattern and returns a compiled Regexp in the default UTF-8
// encoding, or an error if the pattern is malformed. As with Ruby's Regexp.new,
// a syntax error is reported here at compile time, not deferred to the first
// match.
func Compile(pattern string) (*Regexp, error) {
	return CompileEnc(pattern, UTF8)
}

// CompileEnc is Compile with an explicit input encoding (see Encoding). UTF8
// makes the dot and byte-oriented classes advance by a whole code point;
// ASCII8BIT makes every atom advance one byte, matching Ruby's /n.
func CompileEnc(pattern string, enc Encoding) (*Regexp, error) {
	re, err := engine.CompileEnc(pattern, enc)
	if err != nil {
		return nil, err
	}
	return wrap(re), nil
}

// wrap adapts an engine.Regexp into the Ruby-flavoured Regexp, precomputing the
// name→1-based-index map used by MatchData's named-group accessors.
func wrap(re *engine.Regexp) *Regexp {
	names := map[string]int{}
	for i, nm := range re.SubexpNames() {
		if nm != "" {
			names[nm] = i
		}
	}
	return &Regexp{re: re, names: names}
}

// Encoding returns the input encoding the Regexp matches under (Ruby's
// Regexp#encoding equivalent): UTF8 by default, ASCII8BIT for a binary pattern.
func (re *Regexp) Encoding() Encoding { return re.re.Encoding() }

// String returns the source pattern the Regexp was compiled from.
func (re *Regexp) String() string { return re.re.String() }

// Timeout returns the wall-clock limit applied to a single match, or zero if no
// limit is set.
func (re *Regexp) Timeout() time.Duration { return re.re.Timeout() }

// WithTimeout returns a copy of the Regexp that aborts any single match taking
// longer than d of wall-clock time (Ruby's Regexp.timeout equivalent), reporting
// ErrTimeout from the …Err match methods and no match from every other one. A
// non-positive d clears the limit. The copy shares the compiled
// program with the receiver, which is left unchanged, so a Regexp can be shared
// across goroutines and given per-use timeouts without data races.
func (re *Regexp) WithTimeout(d time.Duration) *Regexp {
	return &Regexp{re: re.re.WithTimeout(d), names: re.names}
}

// Match searches s for the leftmost match and returns a *MatchData, or nil if
// there is no match. The search scans start positions left to right and, at the
// first position that matches, returns the greedy leftmost-first match (Ruby /
// Onigmo semantics). If the Regexp carries a timeout (see WithTimeout) and the
// search exceeds it, or the internal step budget is exhausted, Match returns nil
// — indistinguishable from a genuine non-match. Use MatchErr to tell them apart.
func (re *Regexp) Match(s string) *MatchData {
	md, _ := re.MatchErr(s)
	return md
}

// MatchErr is Match that reports why there is no match: err is ErrTimeout or
// ErrBudget when the search was abandoned with the answer still unknown, and nil
// when a nil *MatchData means a real non-match. It is the form a validator,
// denylist or any other guard must use — see ErrTimeout.
func (re *Regexp) MatchErr(s string) (*MatchData, error) {
	caps, err := re.re.FindStringSubmatchIndexErr(s)
	if err != nil {
		return nil, err
	}
	if caps == nil {
		return nil, nil
	}
	return re.matchData(s, caps), nil
}

// MatchAt attempts a match anchored exactly at byte offset pos in s, with \G
// bound to pos. It does not scan forward: it matches at pos or returns nil. The
// whole string s stays visible to the matcher, so the line/text anchors (^, \A)
// and lookbehind see the real prefix s[:pos] — exactly the semantics a
// StringScanner-style tokenizer needs. Group offsets in the returned MatchData
// are absolute into s. pos out of range yields nil, as does a search abandoned at
// a limit (see ErrTimeout); use MatchAtErr to tell those apart.
func (re *Regexp) MatchAt(s string, pos int) *MatchData {
	md, _ := re.MatchAtErr(s, pos)
	return md
}

// MatchAtErr is MatchAt that reports why there is no match: err is ErrTimeout or
// ErrBudget when the search was abandoned with the answer still unknown, and nil
// otherwise. See ErrTimeout for why a guard must use this form.
func (re *Regexp) MatchAtErr(s string, pos int) (*MatchData, error) {
	caps, err := re.re.FindStringSubmatchIndexAtErr(s, pos)
	if err != nil {
		return nil, err
	}
	if caps == nil {
		return nil, nil
	}
	return re.matchData(s, caps), nil
}

// MatchBoundsAt is the allocation-free, bounds-only form of MatchAt: it reports
// the whole match's [begin, end) byte span for a match anchored exactly at pos
// (begin == pos on success), without building a MatchData or extracting
// submatches. It is the primitive for the cursor-anchored StringScanner ops that
// need only a length or a yes/no — skip(/…/), match?(/…/). The span is identical
// to MatchAt(s, pos).Begin(0)/End(0). pos out of range yields ok == false, as
// does a search abandoned at a limit (see ErrTimeout); use MatchBoundsAtErr to
// tell them apart.
func (re *Regexp) MatchBoundsAt(s string, pos int) (begin, end int, ok bool) {
	return re.re.MatchBoundsAt(s, pos)
}

// MatchBoundsAtErr is MatchBoundsAt that reports why there is no match: err is
// ErrTimeout or ErrBudget when the search was abandoned with the answer still
// unknown, and nil otherwise. See ErrTimeout for why a guard must use this form.
func (re *Regexp) MatchBoundsAtErr(s string, pos int) (begin, end int, ok bool, err error) {
	return re.re.MatchBoundsAtErr(s, pos)
}

// MatchBounds is the allocation-free, bounds-only form of Match: it scans s left
// to right for the leftmost match and returns its whole-match [begin, end) byte
// span, without building a MatchData or extracting submatches. The span is
// identical to Match(s).Begin(0)/End(0). A search abandoned at a limit (see
// ErrTimeout) yields ok == false; use MatchBoundsErr to tell them apart.
func (re *Regexp) MatchBounds(s string) (begin, end int, ok bool) {
	return re.re.MatchBounds(s)
}

// MatchBoundsErr is MatchBounds that reports why there is no match: err is
// ErrTimeout or ErrBudget when the search was abandoned with the answer still
// unknown, and nil otherwise. See ErrTimeout for why a guard must use this form.
func (re *Regexp) MatchBoundsErr(s string) (begin, end int, ok bool, err error) {
	return re.re.MatchBoundsErr(s)
}

// MatchString reports whether s contains a match of the regular expression. A
// search abandoned at a limit (see ErrTimeout) reports false, indistinguishable
// from a genuine non-match — which for a denylist means the subject is let
// through. Use MatchStringErr to tell them apart.
func (re *Regexp) MatchString(s string) bool {
	return re.re.MatchString(s)
}

// MatchStringErr is MatchString that reports why the answer is false: err is
// ErrTimeout or ErrBudget when the search was abandoned with the answer still
// unknown, and nil when false means a real non-match. It is the form a validator,
// denylist or any other guard must use — see ErrTimeout.
func (re *Regexp) MatchStringErr(s string) (bool, error) {
	_, _, ok, err := re.re.MatchBoundsErr(s)
	return ok, err
}

// matchData builds a MatchData from an engine capture slice: caps holds the
// [begin,end) pairs for group 0 (the whole match) and each capturing group, with
// -1/-1 for a group that did not participate.
func (re *Regexp) matchData(s string, caps []int) *MatchData {
	return &MatchData{input: s, caps: caps, ngroups: len(caps)/2 - 1, names: re.names}
}

// MatchData holds the result of a successful match: the byte spans of the whole
// match (group 0) and of each capturing group.
type MatchData struct {
	input   string
	caps    []int
	ngroups int
	names   map[string]int
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
