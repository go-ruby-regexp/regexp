<p align="center"><img src="https://raw.githubusercontent.com/go-onigmo/brand/main/social/go-onigmo-regexp.png" alt="go-onigmo/regexp" width="720"></p>

# regexp — go-onigmo

[![Docs](https://img.shields.io/badge/docs-mkdocs--material-9B1C2E)](https://go-onigmo.github.io/docs/)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Phase](https://img.shields.io/badge/phase-0--3%20done%2C%204%20started-1a7f37)](docs/plan-regexp.md)

**A pure-Go (no cgo) reimplementation of [Onigmo](https://github.com/k-takata/Onigmo)**,
the regular-expression engine used by Ruby — a faithful **backtracking VM** with
the features RE2 (Go's standard `regexp`) deliberately omits: **backreferences**,
**lookahead/lookbehind**, **possessive quantifiers**, **atomic groups**, named
groups, subexpression calls, and Ruby's leftmost-*first* match semantics.

It is the regexp backend for
[go-embedded-ruby](https://github.com/go-embedded-ruby/ruby), but is a
**standalone, reusable** module with no dependency on the Ruby runtime.

> **Status: Phases 0–3 implemented, Phase 4 started** — a greedy backtracking VM with
> leftmost-first semantics covering literals/escapes, `.`, character classes,
> anchors (`\A \z \Z ^ $`), greedy, **non-greedy/lazy**, and **possessive**
> quantifiers (`* + ? {m,n}`, `*? +? ?? {m,n}?`, and `*+ ++ ?+`) plus
> **atomic groups `(?>…)`**, capturing and
> non-capturing groups, alternation, named groups `(?<name>…)` and
> backreferences `\1` / `\k<name>`, plus **lookahead `(?=…)` / `(?!…)`,
> fixed-width lookbehind `(?<=…)` / `(?<!…)`, the `\G` anchor, and
> subexpression calls `\g<…>`** (named/numbered/relative/`\g<0>`, recursive), and from
> Phase 3 **POSIX bracket classes `[[:alpha:]]` … `[[:^digit:]]`**
> (the 14 standard classes, positive and negated) inside character classes, and
> the **inline options `(?imx)` / `(?imx:…)`** (with `(?-…)` to turn them off) —
> `i` case-insensitive matching (**rune-level** folding of literals and classes
> via `unicode.SimpleFold`, so `/É/i` matches `é`, `(?i)[α-ω]` an uppercase Greek
> letter, and `/k/i` even the Kelvin sign; backreferences fold ASCII-only), `m`
> dot-all (the dot also matches a newline), and `x` extended/free-spacing
> (unescaped whitespace and `#` comments ignored, except in a class) — and
> **Unicode property classes `\p{…}` / `\P{…}`** (general
> categories `L N P S Z C` + `Lu Ll Lt Lm Lo Nd`, plus the Onigmo aliases
> `Alpha Alnum Digit Space Upper Lower Word`, with `\p{^…}` negation and
> embedding inside character classes) — all differential-tested against MRI,
> 100% coverage, CI green across 6 arches. Variable-width lookbehind is rejected,
> as in Onigmo/Ruby.
> **ReDoS memoization (Phase 4)** is in: a backreference-free pattern memoizes
> its `(instruction, position)` split states, so catastrophic patterns like
> `(a+)+$` run in polynomial rather than exponential time (with the step budget
> as the backstop), producing the identical leftmost-first match.
>
> **Rune/byte boundary.** `\p{…}` and a folded (`/i`) literal or class are the
> **rune-aware** atoms: each decodes one UTF-8 code point and advances by its byte
> length (a `\p{…}` member, or `/i`, also makes the enclosing character class
> rune-aware). Folding is **simple (1:1)** only — full/special folding (`ß`→`ss`,
> Turkish dotless-i) is out of scope. Everything else is **byte-oriented** and
> byte-exact, and match offsets are **byte** offsets (MRI reports character
> offsets, so the engines agree on matched text but not on the numeric span on
> multi-byte input); a rune-aware atom never matches at a UTF-8 continuation byte
> and is rejected inside a fixed-width lookbehind.
>
> **Subexpression calls (`\g<…>`).** `\g<name>`, `\g<n>`, relative `\g<+n>` /
> `\g<-n>`, and `\g<0>` (whole-pattern recursion) **re-run and re-capture** the
> referenced group, with last-execution-wins captures except that a self-recursive
> group keeps its **outermost** binding — exactly as Onigmo/Ruby. Forward
> references resolve post-parse; recursive and mutually recursive grammars (e.g.
> balanced parentheses `\A(?<bal>\((?:[^()]|\g<bal>)*\))\z`) work. A per-search
> **call/return stack** drives this, with a hard **recursion-depth cap** plus the
> step budget so a non-terminating grammar fails deterministically. A call has
> data-dependent width, so it is rejected inside a fixed-width lookbehind. See
> **[docs/plan-regexp.md](docs/plan-regexp.md)** for the architecture and roadmap.

## Why not the standard library?

Go's `regexp` is RE2: linear-time but without backreferences or lookaround, and
with different (leftmost-longest) semantics. Ruby code routinely depends on
Onigmo features RE2 cannot express, so a byte-compatible Ruby regexp needs a
backtracking engine. This module provides one, hardened against catastrophic
backtracking with memoization and a deterministic time/step budget.

## License

BSD-3-Clause. See [LICENSE](LICENSE).
