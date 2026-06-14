<p align="center"><img src="https://raw.githubusercontent.com/go-onigmo/brand/main/social/go-onigmo-regexp.png" alt="go-onigmo/regexp" width="720"></p>

# regexp — go-onigmo

[![Docs](https://img.shields.io/badge/docs-mkdocs--material-9B1C2E)](https://go-onigmo.github.io/docs/)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Phase](https://img.shields.io/badge/phase-0%2B1%2B2%20done-1a7f37)](docs/plan-regexp.md)

**A pure-Go (no cgo) reimplementation of [Onigmo](https://github.com/k-takata/Onigmo)**,
the regular-expression engine used by Ruby — a faithful **backtracking VM** with
the features RE2 (Go's standard `regexp`) deliberately omits: **backreferences**,
**lookahead/lookbehind**, **possessive quantifiers**, **atomic groups**, named
groups, subexpression calls, and Ruby's leftmost-*first* match semantics.

It is the regexp backend for
[go-embedded-ruby](https://github.com/go-embedded-ruby/ruby), but is a
**standalone, reusable** module with no dependency on the Ruby runtime.

> **Status: Phases 0, 1 and 2 implemented** — a greedy backtracking VM with
> leftmost-first semantics covering literals/escapes, `.`, character classes,
> anchors (`\A \z \Z ^ $`), greedy quantifiers (`* + ? {m,n}`), capturing and
> non-capturing groups, alternation, named groups `(?<name>…)` and
> backreferences `\1` / `\k<name>`, plus **lookahead `(?=…)` / `(?!…)`,
> fixed-width lookbehind `(?<=…)` / `(?<!…)`, and the `\G` anchor** —
> differential-tested against MRI, 100% coverage, CI green across 6 arches.
> Variable-width lookbehind is rejected, as in Onigmo/Ruby. Subexpression calls
> `\g<…>`, Unicode `\p{}`, case-folding and ReDoS memoization are next. See
> **[docs/plan-regexp.md](docs/plan-regexp.md)** for the architecture and roadmap.

## Why not the standard library?

Go's `regexp` is RE2: linear-time but without backreferences or lookaround, and
with different (leftmost-longest) semantics. Ruby code routinely depends on
Onigmo features RE2 cannot express, so a byte-compatible Ruby regexp needs a
backtracking engine. This module provides one, hardened against catastrophic
backtracking with memoization and a deterministic time/step budget.

## License

BSD-3-Clause. See [LICENSE](LICENSE).
