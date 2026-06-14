// Package onigmo is a pure-Go (cgo-free) reimplementation of Onigmo, the
// regular-expression engine used by Ruby.
//
// Unlike Go's standard library regexp (RE2), it is a backtracking VM and so can
// support the Onigmo features RE2 omits — backreferences, lookahead/lookbehind,
// possessive quantifiers, atomic groups, named groups and subexpression calls —
// with Ruby's leftmost-first match semantics, hardened against catastrophic
// backtracking (ReDoS) by memoization and a deterministic time/step budget.
//
// The package is in the planning stage; see docs/plan-regexp.md for the
// architecture and roadmap.
package onigmo
