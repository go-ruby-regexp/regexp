package onigmo

import (
	"github.com/go-onigmo/regexp/internal/compile"
	"github.com/go-onigmo/regexp/internal/syntax"
	"github.com/go-onigmo/regexp/internal/vm"
)

// Regexp is a compiled regular expression, safe for concurrent use by multiple
// goroutines.
type Regexp struct {
	prog   *compile.Program
	source string
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

// Match searches s for the leftmost match and returns a *MatchData, or nil if
// there is no match. The search scans start positions left to right and, at the
// first position that matches, returns the greedy leftmost-first match (Ruby /
// Onigmo semantics).
//
// Match panics only if the internal step budget is exceeded, which cannot occur
// for the Phase 0 instruction set on any input; that path is reserved for the
// ReDoS hardening of later phases and is surfaced as a non-match here.
func (re *Regexp) Match(s string) *MatchData {
	caps, ok, err := vm.Match(re.prog, s, vm.DefaultBudget)
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
