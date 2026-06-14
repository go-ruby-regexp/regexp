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
// non-capturing groups, and alternation. Backreferences, named groups,
// lookaround, Unicode properties, and case-folding arrive in later phases; see
// docs/plan-regexp.md for the full roadmap.
package onigmo
