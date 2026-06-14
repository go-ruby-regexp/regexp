# regexp — go-onigmo

[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Status](https://img.shields.io/badge/status-planning-9a6700)](docs/plan-regexp.md)

**A pure-Go (no cgo) reimplementation of [Onigmo](https://github.com/k-takata/Onigmo)**,
the regular-expression engine used by Ruby — a faithful **backtracking VM** with
the features RE2 (Go's standard `regexp`) deliberately omits: **backreferences**,
**lookahead/lookbehind**, **possessive quantifiers**, **atomic groups**, named
groups, subexpression calls, and Ruby's leftmost-*first* match semantics.

It is the regexp backend for
[go-embedded-ruby](https://github.com/go-embedded-ruby/ruby), but is a
**standalone, reusable** module with no dependency on the Ruby runtime.

> ⚠️ **Status: planning.** See **[docs/plan-regexp.md](docs/plan-regexp.md)** for
> the architecture, ReDoS strategy (memoization + timeout, as Ruby ≥3.2), and the
> phased roadmap.

## Why not the standard library?

Go's `regexp` is RE2: linear-time but without backreferences or lookaround, and
with different (leftmost-longest) semantics. Ruby code routinely depends on
Onigmo features RE2 cannot express, so a byte-compatible Ruby regexp needs a
backtracking engine. This module provides one, hardened against catastrophic
backtracking with memoization and a deterministic time/step budget.

## License

BSD-3-Clause. See [LICENSE](LICENSE).
