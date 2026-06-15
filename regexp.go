package onigmo

import (
	"time"

	"github.com/go-onigmo/regexp/internal/compile"
	"github.com/go-onigmo/regexp/internal/syntax"
	"github.com/go-onigmo/regexp/internal/vm"
)

// Regexp is a compiled regular expression, safe for concurrent use by multiple
// goroutines. A Regexp is immutable once compiled; WithTimeout returns a copy
// carrying a wall-clock match limit rather than mutating the receiver, so a
// shared Regexp stays concurrency-safe.
type Regexp struct {
	prog   *compile.Program
	source string
	// timeout is the wall-clock limit for a single match (Ruby's Regexp.timeout
	// equivalent). Zero means no time limit. It is set only by WithTimeout, which
	// returns a copy, keeping a shared Regexp immutable.
	timeout time.Duration
}

// Compile parses a pattern and returns a compiled Regexp, or an error if the
// pattern is malformed.
func Compile(pattern string) (*Regexp, error) {
	res, err := syntax.Parse(pattern)
	if err != nil {
		return nil, err
	}
	return &Regexp{prog: compile.Compile(res), source: pattern}, nil
}

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
	caps, ok, err := vm.MatchTimeout(re.prog, s, vm.DefaultBudget, re.deadline())
	if err != nil || !ok {
		return nil
	}
	return &MatchData{input: s, caps: caps, ngroups: re.prog.NumCapture, names: re.prog.Names}
}

// MatchString reports whether s contains a match of the regular expression.
func (re *Regexp) MatchString(s string) bool {
	return re.Match(s) != nil
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
