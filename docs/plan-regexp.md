# Implementation plan — Onigmo in pure Go (`go-onigmo/regexp`)

> Goal: a **pure-Go (no cgo)** reimplementation of **Onigmo**, the regular
> expression engine used by Ruby, as a **standalone, reusable** module. It is the
> regexp backend for [go-embedded-ruby](https://github.com/go-embedded-ruby/ruby)
> but has no dependency on it.

## 1. Why not `regexp` (RE2)?

Go's standard `regexp` is RE2: linear-time, but **deliberately missing**
backreferences and lookbehind/lookahead, and its semantics (leftmost-longest by
default, character-class and Unicode behaviour) differ from Ruby's. Ruby programs
rely on Onigmo features RE2 cannot express:

- **Backreferences** (`\1`, `\k<name>`) and **named groups** (`(?<name>…)`).
- **Lookahead / lookbehind** (`(?=…)`, `(?!…)`, `(?<=…)`, `(?<!…)`).
- **Possessive quantifiers** (`a++`, `a*+`) and **atomic groups** (`(?>…)`).
- **Backtracking semantics** (leftmost-*first*, not leftmost-longest), so match
  results are byte-for-byte what Ruby produces.
- Ruby-specific syntax: `\A \z \Z \G \h \H \R`, `\p{…}` properties,
  `(?<name>…)` / `\k<name>` / `\g<name>` (subexpression calls), encodings.

So the engine is a **backtracking VM** (Onigmo's model), not an NFA/DFA simulator.

## 2. Threat model: ReDoS

Backtracking engines can blow up exponentially. Mitigations, matching Ruby ≥3.2:

- **Memoization** of (instruction, input-position) pairs to cut redundant
  backtracking where it is safe (no backreference dependence).
- **A timeout** (`Regexp.timeout` equivalent) and a configurable backtrack-step
  budget that aborts a pathological match deterministically.
- Optional static analysis to warn on obviously catastrophic patterns.

## 3. Architecture

```
pattern (string, encoding, flags)
   │  scanner / parser  → AST (Onigmo syntax)
   ▼
   │  compiler          → bytecode program (opcodes for the backtracking VM)
   ▼
   │  optimizer         → anchors, first-byte sets, literal prefixes, atomic cuts
   ▼
program  ──►  VM (backtracking, memoized, budgeted)  ──►  MatchData
```

Packages:

```
regexp/
  syntax/      scanner + parser → AST; Onigmo grammar & escapes
  compile/     AST → VM program (instructions + capture/group metadata)
  vm/          backtracking matcher: thread state, backtrack stack, memo, budget
  charset/     character classes, POSIX classes, \p{…} Unicode properties
  encoding/    byte/rune handling per encoding (UTF-8, ASCII-8BIT, …)
  regexp.go    public API (Compile, Match, MatchData, named captures, replace)
```

## 4. Public API (Ruby-shaped, Go-idiomatic)

```go
re, err := onigmo.Compile(`(?<year>\d{4})-(?<mon>\d{2})`, onigmo.None)
m := re.Match("2026-06")          // *MatchData or nil
m.Group("year")                   // "2026"
m.Begin(0); m.End(0)              // byte offsets
re.Replace(src, `\k<mon>/\k<year>`)
```

`MatchData` exposes whole-match and per-group spans (by index and by name),
pre/post match, and works in byte offsets so callers can map back to their own
string representation. A thin adapter in `go-embedded-ruby/ruby/internal/regexp`
maps Ruby's `Regexp`/`MatchData` onto this.

## 5. Compatibility & testing

- **Differential oracle against Onigmo/MRI** (dev only, not linked): run a corpus
  of `(pattern, input)` pairs through Ruby and through this engine; compare match
  span, captures, and named groups exactly.
- **Onigmo's own test corpus** ported as fixtures.
- **Property/fuzz tests**: random patterns + inputs vs the oracle; fuzz the
  parser for crashes; assert the budget/timeout always terminates.
- **100% coverage** target, enforced in CI (org convention).

## 6. Phasing

- **Phase 0** — scanner + parser for the common subset (literals, classes,
  `. * + ? {m,n}`, groups, alternation, anchors `^ $ \A \z`), compiler + a
  minimal backtracking VM. Exit: anchored/greedy matching with captures matches
  MRI on a starter corpus.
- **Phase 1** — named groups, non-greedy/possessive quantifiers, atomic groups,
  backreferences.
- **Phase 2** ✅ *done (except `\g<…>`)* — lookahead `(?=…)`/`(?!…)`,
  lookbehind `(?<=…)`/`(?<!…)`, and the `\G` anchor. Subexpression calls
  `\g<…>` remain deferred (tracked under a later phase).

  **Lookbehind limitation.** Matching Onigmo/Ruby, each *alternative* of a
  lookbehind body must have a **constant byte width**; different alternatives may
  differ (`(?<=ab|c)` is fine). Bodies whose width can vary — unbounded or
  `{m,n}` (m ≠ n) quantifiers, and backreferences — are rejected at parse time
  with a "variable-width lookbehind is not supported" syntax error, exactly as
  Ruby does. The VM evaluates a fixed/bounded-width lookbehind by trying each
  candidate start position `sp − w` (widest first, for greedy preference) and
  requiring the sub-pattern to consume exactly up to the current position.

  `\G` pins a match to the position where the overall scan began; for a single
  `Match` call that is offset 0 (so it behaves like `\A`). Iterative scanning
  (`scan`/`gsub`), which will advance the `\G` anchor on each step, arrives with
  the replacement/scan API in a later phase.
- **Phase 3** — Unicode properties `\p{…}`, POSIX classes, case-folding,
  multi-encoding.
- **Phase 4** — ReDoS hardening (memoization + timeout/budget), optimizer
  (first-byte sets, literal prefixes), benchmarks.
- **Phase 5** — full Ruby `Regexp`/`MatchData` surface via the go-embedded-ruby
  adapter; replacement DSL (`\1`, `\k<>`, `\&`, blocks).

## 7. Decisions

1. **Model** — backtracking VM (Onigmo-faithful), not RE2-style automata, because
   backreferences and the leftmost-first semantics require it. *Settled.*
2. **Standalone module** — usable by any Go program, not just the Ruby runtime.
   *Settled.*
3. **Encodings** — byte-oriented core with an encoding abstraction; UTF-8 and
   ASCII-8BIT first.
4. **ReDoS** — memoization + deterministic budget/timeout from Phase 4; never
   rely on the host watchdog alone.

BSD-3-Clause.
