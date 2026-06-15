// Package onigmo is a pure-Go (cgo-free) reimplementation of Onigmo, the
// regular-expression engine used by Ruby.
//
// Unlike Go's standard library regexp (RE2), it is a backtracking VM and so can
// support the Onigmo features RE2 omits — backreferences, lookahead/lookbehind,
// possessive quantifiers, atomic groups, named groups and subexpression calls —
// with Ruby's leftmost-first match semantics, hardened against catastrophic
// backtracking (ReDoS) by memoization and a deterministic time/step budget.
//
// Phase 0 is implemented: a greedy backtracking VM with leftmost-first match
// semantics covering literals and escapes, the dot metacharacter, character
// classes (ranges, negation, and the \d \D \w \W \s \S escapes), the anchors
// \A \z \Z ^ $, the greedy quantifiers * + ? {m} {m,} {m,n}, capturing and
// non-capturing groups, and alternation. Phase 1 adds named groups
// (?<name>...) and backreferences (\1..\9 and \k<name>). Phase 2 adds the
// lookaround assertions — positive and negative lookahead (?=...) (?!...) and
// lookbehind (?<=...) (?<!...) — and the \G anchor (which pins a match to the
// scan start). Lookbehind bodies must be of constant width per alternative, as
// in Onigmo/Ruby; variable-width lookbehind is rejected. Phase 3 begins with
// POSIX bracket classes [[:name:]] (and negated [[:^name:]]) inside character
// classes, for the 14 standard names alpha, digit, alnum, upper, lower, space,
// blank, cntrl, graph, print, punct, xdigit, and word; and the inline options
// (?flags) / (?flags:...) (with the (?-flags) turn-off form) for the letters i
// (ASCII case-insensitive matching, folding ASCII letters in literals, character
// classes, and backreferences), m (dot-all: the dot also matches a newline), and
// x (extended/free-spacing: unescaped whitespace and # comments in the pattern
// are ignored, except inside a character class). Folding is byte-oriented and
// ASCII-only; Unicode property classes and Unicode case-folding arrive with the
// later rune-level work.
//
// ReDoS hardening (Phase 4) is in: for a pattern without a backreference the VM
// memoizes the (instruction, position) split states it has explored and never
// re-explores one, so catastrophic patterns such as (a+)+$ run in polynomial
// rather than exponential time while producing the identical leftmost-first
// match. A deterministic step budget remains as the backstop. See
// docs/plan-regexp.md for the full roadmap.
package onigmo
