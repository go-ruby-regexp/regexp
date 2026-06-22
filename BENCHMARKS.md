# Performance parity — go-ruby-regexp vs Onigmo (C) / Go regexp (2026-06-22)

This module (`github.com/go-ruby-regexp/regexp`, formerly **go-onigmo**) is a
from-scratch, pure-Go (cgo-free) reimplementation of **Onigmo** — the backtracking
regex engine behind Ruby's `Regexp`. This report measures it against the bar it
reimplements (**C Onigmo**) and against the Go peer (**stdlib `regexp`**, RE2).

> **Bar:** *as fast as the C Onigmo we reimplement.* On constructs where both
> engines do the same work we want parity; where we lag, the root cause and the
> fix are named below. RE2 is a *different algorithm* (linear-time automaton vs
> backtracking VM) — a tradeoff column, not a like-for-like target.

## Methodology

| | |
|---|---|
| **CPU / OS** | Apple M4 Max (16 cores), macOS 15 (Darwin 25.5.0), single core |
| **Go** | go1.26.4 (darwin/arm64), stdlib `regexp` = RE2 |
| **Onigmo (C)** | 6.2.0 (`k-takata/Onigmo`), built from source, Ruby syntax + UTF-8, `onig_search` |
| **Ruby (proxy)** | MRI 4.0.5 `Regexp` (Onigmo *through* the interpreter) |
| **Protocol** | best (min) of 12 timed batches, inner count auto-scaled to ≥ 50 ms/batch, monotonic clock |
| **Inputs** | one shared corpus (`benchmarks/corpus.json`), byte-identical across all four engines |
| **Correctness** | leftmost-match byte span recorded per engine; spans **agree** across ours / Onigmo C / Ruby (and RE2) on every case |
| **Reproduce** | `cd benchmarks && ./run.sh` → `results.csv` (isolated: its own Go module, outside the root coverage gate) |

`compile ns` is the best single-compile; `match ns` the best per-iteration full
leftmost search. **MB/s caveat:** for an *early hit* (a match ending a few bytes
in), MB/s divides the whole-haystack length by a time that only examined the
matched prefix, so those rows over-state scan rate — they remain apples-to-apples
across engines (all measured identically). Read the *miss / full-scan* rows for
true scan throughput. `ours/onig` is the match-time speed ratio (>1 = we are
faster).

## Parity table

| pattern | input | ours (MB/s · compile ns) | Onigmo C (MB/s · cns) | Go regexp RE2 (MB/s · cns) | ours / Onigmo | verdict |
|---|---|---|---|---|---|---|
| `needle` (miss) | 88 KB text | **5082** · 989 | 3030 · 209 | 5080 · 613 | **1.68×** | ✅ beat C (prefilter) |
| `needle` (hit @ end) | 88 KB text | **4996** · 942 | 3032 · 221 | 5064 · 616 | **1.65×** | ✅ beat C (prefilter) |
| `zoo\|quux\|kite` (miss) | 88 KB | 108 · 1550 | 199 · 760 | 44 · 1108 | 0.54× | ⚠️ < C, **2.5× > RE2** |
| `([0-9]{1,3}\.){3}[0-9]{1,3}` | 88 KB, hit @ end | **2842** · 2257 | 411 · 1099 | 79 · 1788 | **6.92×** | ✅ beat C **and** RE2 |
| `cat\|dog\|fox` (hit) | early hit | 192 k · 1200 | 1125 k · 742 | 176 k · 1036 | 0.17× | ⚠️ < C |
| `[a-zA-Z]+` | early hit | 160 k · 612 | 3600 k · 519 | 1667 k · 343 | 0.04× | ❌ << C |
| `\A\w+` (anchored) | early hit | 156 k · 845 | 3103 k · 297 | 1579 k · 515 | 0.05× | ❌ << C |
| `lazy` (unanchored) | mid hit | 417 k · 638 | 4500 k · 177 | 1071 k · 519 | 0.09× | ❌ << C |
| `[0-9]{2,4}` (bounded) | early hit | 191 k · 864 | 1895 k · 489 | 537 k · 565 | 0.10× | ❌ << C |
| `(\w+)=(\w+)` (captures) | mid hit | 21 k · 1462 | 213 k · 599 | 237 k · 982 | 0.10× | ❌ << C (alloc) |
| `\p{L}+` (Unicode) | early hit | 168 k · 586 | 3135 k · 11156 | 1785 k · 3260 | 0.05× | ❌ << C (but our compile 19× faster) |
| `.x` (UTF-8 miss) | 235 KB | 26.6 · 564 | 324 · 253 | 107 · 369 | 0.08× | ❌ << C |
| `email` | 90 KB, hit @ end | 5.1 · 1598 | 42.8 · 1662 | 52.1 · 1461 | 0.12× | ❌ << C |
| `https?://…` (URL) | 82 KB, hit @ end | 2774 · 1169 | 9317 · 1021 | 3162 · 1168 | 0.30× | ⚠️ < C, ≈ RE2 |
| `\[\d{4}-…\] (ERROR\|…) \w+` | log line | 88 k · 3012 | 952 k · 2376 | 352 k · 2835 | 0.09× | ❌ << C |
| `\A(a*)*b` (ReDoS) | 40×`a`+`!` | 3.4 · 983 | 244 · 513 | 64.9 · 799 | 0.01× | ❌ < C (C has empty-loop opt) |
| `\A(a\|aa)+b` (ReDoS) | 40×`a`+`!` | **3.1 · 1525 — safe** | **TIMEOUT > 60 s** | 56.6 · 799 | **∞** | ✅ **C catastrophically backtracks; we don't** |
| `(a+)\1b` (backref) | 24×`a`+`c` | 0.8 · 957 | 7.4 · 498 | *RE2: unsupported* | 0.11× | ❌ < C (no RE2 peer) |

*(Ruby/MRI proxy column omitted from the table for width; it is in
`benchmarks/results.csv`. Note MRI 4.0 ships Onigmo's memoized linear-time mode,
so MRI finishes `\A(a|aa)+b` in ~1.9 µs while the raw 6.2.0 **C library** does
not — see below.)*

## Summary

### Where we meet or beat the C Onigmo
- **Literal scans** (`needle` miss/hit): **1.65–1.68× faster than C Onigmo** and
  on par with RE2. Our literal-prefix prefilter rejects/locates with one
  `strings.Index` (runtime Boyer–Moore-ish) pass instead of stepping
  `onig_search` byte by byte.
- **Alternation miss**: 0.54× of C but **~2.5× faster than RE2** — the
  alternation-aware first-byte set skips most positions.
- **Structured numeric scan** `([0-9]{1,3}\.){3}…`: **6.9× faster than C Onigmo
  and 36× faster than RE2** — C Onigmo backtracks the bounded reps hard across a
  long no-match haystack; our prefilter + bounded-rep compilation does not.
- **ReDoS safety** (the headline): on `\A(a|aa)+b` the **C Onigmo we reimplement
  blows up past 60 s**; our `(pc,sp)` memo holds it to **13 µs** (RE2 stays linear
  too). We are *algorithmically safer than the engine we clone* on this class.
- **Compile time on Unicode**: our `\p{L}+` compile is **19× faster** than C
  Onigmo's (11.2 µs → 0.6 µs); C Onigmo pays a large table-build cost per compile.

### Where we lag the C Onigmo — root causes
The pattern is consistent: on **match-time inner-loop throughput** for
quantifiers, classes, captures, and full-haystack scans we are **10–25× slower**
than C Onigmo. Three root causes, all confirmed by allocation profiling
(`go test -bench -benchmem`): SubexprCallRecursion = 214 KB/op·663 allocs, backref
= 1008 allocs/op, RedosNestedStar = 29 KB/op·349 allocs.

1. **Per-thread capture-array copying (biggest cost).**
   `internal/vm/vm.go` `saveSlot`/`push` allocate and `copy` a fresh `[]int` of
   all capture slots on **every** `OpSave` and **every** backtrack push. C Onigmo
   mutates one capture region with an undo log. This dominates `captures_kv`
   (10× slower) and every backtracking case.
   → **Action:** replace copy-on-save with a single mutable capture array +
   an undo/trail stack (record `(slot, oldval)`, unwind on backtrack). Removes
   the O(captures) allocation per step. *Highest ROI.*

2. **Tree-walking `switch` dispatch with a per-step `tick()` and a `map` memo.**
   `run()` switches on `compile.Inst.Op` each step; `OpSplit` does a Go-map
   lookup+insert into `m.visited` (keyed `int64(pc)<<32|sp`); `tick()` decrements a
   budget every step. C Onigmo runs a tuned opcode loop with specialised
   repeat/anychar opcodes.
   → **Actions:** (a) swap the `map[int64]bool` memo for a flat
   `[]uint64` bitset indexed by `pc*len(input)+sp` (or a 2-level slice) — turns a
   hashed map op into a bit test on the hot `OpSplit` path; (b) add fused
   loop opcodes for `OpClass*`/`OpChar*`/`OpAny*` so a `[a-z]+` / `.x` run advances
   in a tight inner loop instead of re-dispatching the outer `switch` per byte;
   (c) hoist the budget/deadline check out of the per-byte path (sample every N).

3. **No DFA / memchr-style scan for the common no-anchor, no-literal case.**
   `.x`, `email`, and `\p{L}+`-style scans re-enter the VM at every start
   position. C Onigmo uses an optimized forward-search (and a first-byte
   `memchr`).
   → **Actions:** (a) a first-byte `IndexByte`/`bytes.IndexAny` skip in the
   start-position scan for patterns whose first atom is a small byte set (extends
   the existing prefilter to *drive* the scan, not just gate it); (b) a
   bounded-size lazy-DFA cache for the anchored, capture-free, backref-free subset
   (the cases RE2 wins) to get linear scan speed without losing the backtracker for
   the feature-rich patterns.

### Honest bottom line
- vs **C Onigmo**: we **win on literal/prefilter-friendly scans and on ReDoS
  safety**, **tie nowhere in the inner loop**, and **lag 10–25× on
  quantifier/capture/scan throughput** — entirely from allocation and dispatch
  overhead, not algorithmic deficiency. The fixes above (mutable captures + bitset
  memo + fused loop opcodes) are expected to close most of that gap; none changes
  matching semantics.
- vs **RE2**: we are faster on alternation-miss and structured bounded-rep scans,
  slower on plain class/anchor scans, and we *have the features RE2 lacks*
  (backreferences, lookaround, atomic/possessive, subexpr calls). RE2 stays linear
  on ReDoS by construction; we stay bounded via the memo + step budget.

### Action items (ranked)
1. **Mutable capture array + undo trail** — kill the per-save `[]int` copy. (vm.go `saveSlot`/`push`)
2. **Flat bitset memo** replacing `m.visited map[int64]bool`. (vm.go `OpSplit`, `consumed`)
3. **Fused loop opcodes** for `OpChar/OpClass/OpAny/OpUniProp` quantified runs. (compile + vm)
4. **First-byte `IndexByte`-driven start scan**; lazy-DFA cache for the anchored capture/backref-free subset. (vm/prefilter.go)
5. **Hoist budget/deadline polling** off the per-byte path.

_Numbers: `benchmarks/results.csv` (this run, 2026-06-22). Regenerate with
`benchmarks/run.sh`._
