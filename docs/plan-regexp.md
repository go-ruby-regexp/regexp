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
- **Phase 1** — named groups, backreferences, and quantifier modes.

  **Non-greedy (lazy) quantifiers** ✅ *done* — `*?`, `+?`, `??`, and `{m,n}?`
  match the *fewest* repetitions first and take more only when the rest of the
  pattern forces it, so `<.+?>` on `<a><b>` matches `<a>` and `a{2,4}?` on `aaaa`
  matches `aa`. They are compiled by swapping the greedy split's preferred/
  give-back branches (the loop body becomes the give-back branch and the exit the
  preferred one); the zero-width-loop guard follows a per-split `GuardTo` exit so
  an empty lazy loop terminates regardless of branch order. Possessive
  quantifiers (`*+`, `++`, `?+`, `{m,n}+`) and atomic groups `(?>…)` — which share
  an atomic-cut VM mechanism — are the next increment.
- **Phase 2** ✅ *done* — lookahead `(?=…)`/`(?!…)`, lookbehind
  `(?<=…)`/`(?<!…)`, the `\G` anchor, and subexpression calls `\g<…>`.

  **Subexpression calls (`\g<…>`)** ✅ *done* — `\g<name>`, `\g<n>` (absolute
  group number), the relative forms `\g<+n>` / `\g<-n>`, and `\g<0>` (recurse
  the whole pattern); the quote-delimited spelling `\g'…'` is accepted too. A
  call is a **true re-execution** of the referenced group's sub-pattern at the
  current position and **re-captures** into that group's slot, so the most recent
  execution wins (`(\d+)-\g<1>` on `12-34` leaves group 1 = `34`) — *except* that
  a group recursing into itself keeps its **outermost** binding, exactly as
  Onigmo/Ruby does. The VM implements this with a per-search **call/return
  stack**: an `OpCall` records a return frame (saving the captures of every
  currently-open group) and jumps to the callee's entry; the callee's own
  `OpReturn` pops the frame and restores those open-group captures, while a nested
  group's `OpReturn` reached on the *linear* path is skipped (each return is
  tagged with its group index). Forward references — a call to a group defined
  later in the pattern — resolve in a **post-parse pass**. Calls may be recursive
  and **mutually recursive**, so the balanced-parentheses idiom
  `\A(?<bal>\((?:[^()]|\g<bal>)*\))\z` and recursive grammars (arithmetic
  expressions, nested brackets) work and capture exactly as MRI does.

  **Recursion budget.** Recursion is bounded by a hard depth cap
  (`vm.MaxCallDepth`, 4096 — one frame per nesting level, generous for any
  realistic input) integrated with the existing step budget: a call that would
  exceed the cap is a local failure (the engine backtracks), so a non-terminating
  grammar such as `\A\g<0>\z` (which Onigmo *rejects statically* with "never
  ending recursion" — a divergence: this engine accepts it and simply fails to
  match) terminates deterministically instead of exhausting the Go stack or
  hanging. A subexpression call, like a backreference, makes the future depend on
  captured/recursive state, so the persistent `(pc, sp)` ReDoS memo is disabled
  for call-bearing programs (the depth and step budgets are the backstop).

  **Lookbehind boundary.** A call's matched width is data/recursion-dependent and
  in general undecidable, so — like a backreference — `\g<…>` is **rejected inside
  a fixed-width lookbehind** with the same "variable-width lookbehind is not
  supported" error. (MRI accepts the simple one-character case; this is a
  documented divergence.) A call is otherwise allowed inside lookahead and
  lookbehind bodies that do not themselves need a fixed width.

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
- **Phase 3** *(in progress)* — POSIX bracket classes, Unicode properties
  `\p{…}`, case-folding, multi-encoding.

  **POSIX bracket classes** ✅ *done* — inside a character class, `[[:name:]]`
  (and the negated `[[:^name:]]`) expand to the byte ranges Onigmo uses for the
  ASCII byte space. The 14 standard classes are supported: `alpha`, `digit`,
  `alnum`, `upper`, `lower`, `space`, `blank`, `cntrl`, `graph`, `print`,
  `punct`, `xdigit`, and `word`. A `[` inside a class that is not followed by
  `:` is a literal `[`; an unknown class name, or a `[:` that is not closed by
  `:]`, is a parse error (matching Ruby). Negation complements the positive set
  over the full `0..255` byte range, so e.g. `[[:^alpha:]]` matches any
  non-ASCII-letter byte — the byte-oriented behaviour MRI exhibits on
  ASCII-8BIT strings.

  **Case-folding (`/i`)** ✅ *done (literals + classes are rune-level; backrefs
  ASCII)* — case-insensitive matching via the inline options `(?i)` (a set
  directive that applies to the rest of the enclosing group), `(?i:…)` (a scoped
  non-capturing group), and `(?-i)` / `(?i-i:…)` (turning folding back off).
  Scoping follows Onigmo/Ruby exactly, including the subtle rule that a `(?i)`
  forming the *leading prefix* of an alternation branch propagates to later
  branches (`(?i)a|b` folds `b`) whereas one set after a consuming atom does not
  (`a(?i)|b` does not).

  **Rune-level folding (literals and classes).** Under `/i`, a literal character
  and a character class fold **rune-aware** using Go's `unicode.SimpleFold` —
  *simple (1:1)* Unicode case folding. The parser lowers a folded character that
  has a case partner (every ASCII letter, and many non-ASCII letters) to a
  rune-aware `FoldLiteral`/`OpFoldChar`, which decodes one UTF-8 code point and
  accepts it when it is in the same simple-case-folding orbit as the pattern code
  point. So `/É/i` matches `é`, `/Σ/i` matches `σ` and the final-sigma `ς`,
  Cyrillic and Greek case pairs fold, and even an ASCII `/k/i` matches the Kelvin
  sign U+212A and `/s/i` the long s ſ — exactly as MRI. A folded **class**
  becomes rune-aware: a decoded input code point is in the class when it, or any
  rune in its fold orbit, lies in a range or satisfies a `\p{…}` member, so
  `(?i)[a-z]` matches `A` and the Kelvin sign, `(?i)[α-ω]` matches an uppercase
  Greek letter, multi-byte members and ranges work (`(?i)[é]`, `(?i)[Α-Ω]`,
  `(?i)[a-é]`), and negation is applied last (`(?i)[^é]` excludes `É`). A folded
  rune atom obeys the same rune/byte boundary as `\p{…}`: it refuses to match at a
  UTF-8 continuation byte, match **offsets stay byte offsets**, and — because its
  byte width varies (e.g. `k` vs the 3-byte Kelvin sign) — it is rejected inside a
  fixed-width lookbehind, like a property atom.

  **Folding boundary.** Only *simple (1:1)* case folding is done. **Full/special
  case folding is deliberately out of scope**: multi-character expansions such as
  `ß`→`ss` and locale-specific rules such as Turkish dotless-`ı`/dotted-`İ` are
  not implemented (Onigmo/Ruby do apply some of these; this engine does not).
  **Backreference folding remains ASCII-only** by design — a backref under `/i`
  compares its captured bytes case-insensitively over ASCII letters, not via the
  rune-level orbit; a multi-byte case partner in a backref is not folded.

  **Inline flags `m` and `x`** ✅ *done* — the same inline-option machinery now
  also carries `m` (dot-all: the dot `.` matches a newline too, Ruby's `/m`) and
  `x` (extended/free-spacing). All three letters share the `(?flags)` set
  directive, the `(?flags:…)` scoped group, and the `(?-flags)` / `(?f-f:…)`
  turn-off forms, with the same alternation-prefix propagation rule as `i`. For
  `m`, the dot's newline exclusion is dropped (`(?m).` matches `\n`); note that
  `^`/`$` are *always* per-line in Ruby and need no flag. For `x`, the parser
  skips the insignificant whitespace bytes Onigmo ignores — space, tab, newline,
  form feed and carriage return (not the vertical tab) — and `#` comments running
  to end of line, both at atom boundaries and between an atom and a following
  quantifier (`(?x)a *` applies `*` to `a`); inside a character class those bytes
  are literal, and `\ ` / `\#` (and the other escaped whitespace bytes) are
  literal everywhere. One Onigmo idiosyncrasy is *not* reproduced: a `#` comment
  glued directly to an atom and immediately followed by a quantifier (e.g.
  `/(?x)a#c\n+/`) is a syntax error in Onigmo but is accepted here as `a+` after
  a comment; any whitespace around the comment makes Onigmo accept it too, so the
  divergence is confined to that one shape.

  **Unicode property classes (`\p{…}`)** ✅ *done (a deliberate slice)* — the
  `\p{name}`, `\P{name}` and in-brace `\p{^name}` forms are supported as the
  engine's first **rune-aware** atom. A new `internal/charset` package classifies
  one code point with Go's `unicode` package (pure Go, no cgo). The supported
  names are the general categories `L N P S Z C`, the letter/number
  subcategories `Lu Ll Lt Lm Lo Nd`, and the Onigmo POSIX-style aliases `Alpha`,
  `Alnum`, `Digit`, `Space`, `Upper`, `Lower`, `Word` — the aliases follow
  Ruby's definitions, so `Space` is the Unicode `White_Space` property (broader
  than the `Z` category: it includes the vertical tab) and `Word` is
  letter | mark | decimal-number | connector-punctuation (so `_` and combining
  marks are in). Onigmo's one-letter `\pL` form is *not* accepted, matching
  Onigmo (which warns and rejects it). A `\p{…}` may also be a member of a
  character class (`[\p{L}\d]`), which makes that whole class rune-aware.

  **The rune/byte boundary.** Only `\p{…}` (and a character class that contains
  one) is rune-aware: the VM op `OpUniProp` decodes one UTF-8 code point at the
  cursor and advances by its byte length, and a rune-aware class tests a decoded
  code point against both its members and its byte ranges (whose bounds, produced
  only from byte syntax, are all ASCII and so are interpreted as code-point
  ranges). **Everything else stays byte-oriented and byte-exact** — literals, the
  dot, byte character classes, anchors, quantifiers, groups, lookaround, `/i`,
  `(?m)`/`(?x)`, backreferences, and the ReDoS memo. To keep this boundary sound,
  a rune-aware atom **refuses to match at a UTF-8 continuation byte**, so the
  byte-by-byte scan and backtracking never test a code point mid-character — the
  same effect MRI gets by positioning only at character boundaries. That is what
  makes a negated property such as `\P{L}` skip past a multi-byte letter rather
  than match one of its interior bytes. A variable-byte-width rune atom (a
  standalone `\p{…}` or a rune-aware class) is rejected inside a fixed-width
  lookbehind. Match **offsets remain byte offsets**, whereas MRI reports
  *character* offsets; the two therefore agree on the matched text but not on the
  numeric span on multi-byte input, so the differential tests compare matched
  substrings for the UTF-8 corpus and exact spans for the ASCII corpus.

  Still to come in Phase 3: multi-encoding support (the rune-level `/i`
  case-folding above is now done for literals and classes).
- **Phase 4** *(in progress)* — ReDoS hardening, optimizer (first-byte sets,
  literal prefixes), benchmarks.

  **Memoization** ✅ *done* — the backtracking VM memoizes the
  (instruction, input-position) split states it reaches and never re-explores
  one. This is sound exactly when the program has no backreference: captures are
  then write-only and never affect whether a match can succeed, so two arrivals
  at the same `(pc, sp)` split have identical futures and the later one is
  pruned. The compiler records this as `Program.HasBackref`; for a backref-free
  program the `(pc, sp)` set persists across consumed input and becomes the
  memo, collapsing the canonical catastrophic patterns — `(a+)+$`, `(a|aa)+$`,
  `(.*)*$` — from exponential to polynomial work while producing byte-identical
  leftmost-first results. A program with a backreference keeps the previous
  per-advance reset of that set (its only role there is the zero-width-loop
  guard), and the lookaround sub-VM is unchanged. The deterministic step budget
  remains as the backstop for the residual cases (notably backref-bearing
  patterns) that memoization cannot prune. Still to come in Phase 4: a wall-clock
  timeout, the optimizer, and benchmarks.
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
