<p align="center"><img src="https://raw.githubusercontent.com/go-ruby-regexp/brand/main/social/go-ruby-regexp-regexp.png" alt="go-ruby-regexp/regexp" width="720"></p>

# regexp — go-ruby-regexp

[![Docs](https://img.shields.io/badge/docs-mkdocs--material-DC2626)](https://go-ruby-regexp.github.io/docs/)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Phase](https://img.shields.io/badge/phase-0--4%20complete-1a7f37)](docs/plan-regexp.md)

**A pure-Go (no cgo) reimplementation of [Onigmo](https://github.com/k-takata/Onigmo)**,
the regular-expression engine used by Ruby — a faithful **backtracking VM** with
the features RE2 (Go's standard `regexp`) deliberately omits: **backreferences**,
**lookahead/lookbehind**, **possessive quantifiers**, **atomic groups**, named
groups, subexpression calls, and Ruby's leftmost-*first* match semantics.

It is the regexp backend for
[go-embedded-ruby](https://github.com/go-embedded-ruby/ruby), but is a
**standalone, reusable** module with no dependency on the Ruby runtime.

> **Status: Phases 0–4 complete** — a greedy backtracking VM with
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
> embedding inside character classes), the **hex-digit classes `\h` / `\H`**
> (`[0-9A-Fa-f]` and its complement), and the **linebreak escape `\R`** (a CRLF
> matched atomically, or any one of `\n \r \v \f` and the Unicode NEL/LS/PS) —
> all differential-tested against MRI,
> 100% coverage, CI green across 6 arches. Variable-width lookbehind is rejected,
> as in Onigmo/Ruby.
> **ReDoS memoization (Phase 4)** is in: a backreference-free pattern memoizes
> its `(instruction, position)` split states, so catastrophic patterns like
> `(a+)+$` run in polynomial rather than exponential time (with the step budget
> as the backstop), producing the identical leftmost-first match.
>
> **Start-position prefilter (Phase 4)** is in: the optimizer derives, where it
> can, a `\A` anchor, a required literal prefix, or a leading first-byte set from
> the compiled program — including the union over a leading alternation
> (`foo|bar` → `{f,b}`, `a*b` → `{a,b}`) — and uses it to skip start positions
> that provably cannot begin a match (a `strings.Index` / byte-set scan instead
> of running the VM at every offset). It is fully transparent — every candidate
> is still verified by the VM, so results are byte-identical — and gives ~200× on
> a literal-prefixed scan of a long non-matching haystack.
>
> **Required-interior-literal prefilter (Phase 4)** is in: even with no anchor and
> no leading literal, the optimizer extracts a fixed substring that must appear
> *somewhere inside* every match (the `foo` of `\d+foo\d+`, the `xyz` of
> `[ab]*xyz[cd]*`) by walking the program's mandatory spine across quantifiers,
> captured groups, and lookarounds but never across an alternation. A single
> `strings.Contains` then rejects a whole haystack lacking it before the VM runs
> at any offset, and the literal's last occurrence bounds the scan on the right
> (no match can begin past it). It stays transparent (the VM still verifies every
> survivor) and gives **~108×** on a 90 KB non-matching haystack the start-locating
> filters cannot exploit.
>
> **Wall-clock timeout (Phase 4)** is in: `re.WithTimeout(d)` returns a copy that
> aborts any single match exceeding `d` of real time (Ruby's `Regexp.timeout`
> equivalent), the real-time backstop to the deterministic step budget. The
> receiver is left unchanged, so a shared `*Regexp` stays concurrency-safe; the VM
> polls the clock only once every 4096 steps, so a search with no deadline pays
> nothing.
>
> **Benchmarks & transparent allocation reuse (Phase 4)** are in: a representative
> suite (`bench_test.go`) covers literal-prefix and alternation scanning, anchored
> matching, backtracking-heavy nested quantifiers under the ReDoS memo,
> subexpression-call recursion, multibyte/UTF-8 and binary scanning, and the
> prefilter fast paths against a forced-slow baseline. The start-position scan now
> reuses one capture buffer across offsets instead of reallocating at each (the VM
> never writes the base buffer in place, so this is behaviour-preserving), cutting
> the forced-slow whole-haystack baseline from ~270 k to ~180 k allocations. The
> prefilter fast paths run at ~15.6 µs versus ~3.37 ms for that baseline (**~210×**)
> on a 90 KB non-matching haystack, and an active `WithTimeout` adds ~2 % (polling
> noise). **The engine roadmap (Phases 0–4) is complete** — see the
> *Engine status: complete* section of
> [docs/plan-regexp.md](docs/plan-regexp.md) for the full supported-feature list
> and the documented out-of-scope boundaries.
>
> **Rune/byte boundary.** `\p{…}` and a folded (`/i`) literal or class are the
> **rune-aware** atoms: each decodes one UTF-8 code point and advances by its byte
> length (a `\p{…}` member, or `/i`, also makes the enclosing character class
> rune-aware). Folding is **simple (1:1)** only — full/special folding (`ß`→`ss`,
> Turkish dotless-i) is out of scope. Match offsets are **byte** offsets (MRI
> reports character offsets, so the engines agree on matched text but not on the
> numeric span on multi-byte input); a rune-aware atom never matches at a UTF-8
> continuation byte and is rejected inside a fixed-width lookbehind.
>
> **Multi-encoding (`Regexp#encoding`).** A `Regexp` carries a first-class
> encoding (`re.Encoding()`), the way Ruby's `Regexp#encoding` governs matching on
> a UTF-8 versus a binary string. In the default **`UTF8`** mode the dot `.` and a
> byte-oriented class advance by a **whole UTF-8 code point**, so `/./` matches a
> complete multi-byte character (`/./` on `"é"` consumes `"é"`, and `[^a]` consumes
> a whole character — exactly as MRI; a positive ASCII range like `[a-z]` still
> fails on a multi-byte character). In **`ASCII8BIT`** mode (Ruby's binary `/n`,
> via `CompileEnc(pattern, ASCII8BIT)`) every atom advances **one byte** and `/i`
> folding and `\p{…}` operate per byte, ASCII-only. A bare `.`/byte-class inside a
> fixed-width lookbehind has variable byte width (1..4) in `UTF8` mode and the
> candidate-position scan finds the character-aligned start, so `(?<=.)x` matches.
> A **literal multi-byte character-class member** is a whole code point in `UTF8`
> mode: `[é]` matches `"é"`, `[à-ï]` is a code-point range, `[αβγ]`/`[中文]` work,
> a mixed class such as `[a-zé]` combines an ASCII range with a multi-byte member,
> and a range may span ASCII into the multi-byte space (`[a-é]`). In `ASCII8BIT`
> mode such a member stays byte-oriented (`[é]` is its two raw bytes). Encodings
> beyond UTF-8 / ASCII-8BIT (UTF-16/32, EUC, Shift_JIS) are out of scope: a Go
> string is UTF-8 by convention, so legacy/wide text is transcoded to UTF-8 at
> the boundary before matching.
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
