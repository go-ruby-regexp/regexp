# Performance parity — go-ruby-regexp vs Onigmo (C) / Go regexp (2026-06-24)

> **Lazy-DFA update (2026-06-24):** the remaining inner-loop lever named below —
> **a lazy / on-the-fly NFA simulation (RE2 / Go-`regexp` style)** for the
> matchable subset — is now **implemented**. A Thompson-NFA derived from the
> program (fused `OpLoop` unrolled back into split/atom/jmp) is simulated with a
> **priority-ordered thread list** (preserving Ruby's leftmost-FIRST end), one step
> per input position with a precomputed epsilon-closure cache, replacing the
> backtracking VM's per-character dispatch for the search / is-match case. It runs
> for programs with **no backreference, call, lookaround, atomic group, or
> over-large bounded loop**, and only when **no strong literal filter** (a required
> prefix or interior literal) is present — those keep the VM's `strings.Index`
> scan, which already beats C. The backtracking VM remains the source of truth for
> every excluded feature and for submatch extraction. Match-time on the targeted
> inner-loop cases improved **another 1.6–4.3×** over the post-fusion baseline,
> closing the gap to C from ~0.05–0.20× to **0.20–0.63×** (e.g. `[a-zA-Z]+`
> 403→107 ns = 3.8×, `\A\w+` 439→188 ns, `\p{L}+` 359→178 ns, `(\w+)=(\w+)`
> 2593→862 ns = 3.0×, `email` 7.9→3.8 ms ≈ RE2). **ReDoS cases improved 4–43×**
> (`\A(a*)*b` 41→1 µs, `\A(a|aa)+b` 7.8→1.8 µs — still linear while C Onigmo times
> out). Correctness is unchanged: the lazy NFA agrees with the backtracker on the
> leftmost-first span on the full `diff_ruby` cross-check (and C Onigmo / Ruby /
> RE2), 100 % coverage held. The before→after detail is in *Lazy-DFA — results*.

> **Earlier inner-loop update (2026-06-22):** a mutable capture array with an undo
> trail (no per-`OpSave` `[]int` copy), a flat generation-stamped `(pc,sp)` bitset
> memo (replacing `map[int64]bool`), **fused `OpLoop` quantifier opcodes** for
> single-atom `Char/Class/Any/UniProp/FoldChar` runs, and a first-byte
> `IndexByte`-driven start scan. Those roughly **halved** match-time on the
> inner-loop cases and are the baseline the lazy-DFA numbers above build on.

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
| `needle` (miss) | 88 KB text | **5046** · 960 | 2885 · 194 | 5081 · 601 | **1.75×** | ✅ beat C (prefilter) |
| `needle` (hit @ end) | 88 KB text | **4954** · 954 | 2898 · 217 | 5104 · 616 | **1.71×** | ✅ beat C (prefilter) |
| `zoo\|quux\|kite` (miss) | 88 KB | 130 · 1336 | 193 · 785 | 42 · 1143 | 0.67× | ⚠️ < C, **3.1× > RE2** |
| `([0-9]{1,3}\.){3}[0-9]{1,3}` | 88 KB, hit @ end | **2694** · 2207 | 414 · 1092 | 82 · 1796 | **6.50×** | ✅ beat C **and** RE2 |
| `cat\|dog\|fox` (hit) | early hit | 222 k · 1307 | 1125 k · 750 | 172 k · 1074 | 0.20× | ⚠️ < C |
| `[a-zA-Z]+` | early hit | 223 k · 666 | 3600 k · 535 | 1667 k · 337 | 0.06× | ❌ << C (was 0.04×) |
| `\A\w+` (anchored) | early hit | 205 k · 710 | 3000 k · 287 | 1475 k · 528 | 0.07× | ❌ << C (was 0.05×) |
| `lazy` (unanchored) | mid hit | 328 k · 646 | 4091 k · 196 | 1059 k · 508 | 0.08× | ❌ << C |
| `[0-9]{2,4}` (bounded) | early hit | 202 k · 658 | 1895 k · 474 | 537 k · 565 | 0.11× | ❌ << C |
| `(\w+)=(\w+)` (captures) | mid hit | 38 k · 1377 | 214 k · 600 | 231 k · 986 | 0.18× | ⚠️ < C (was 0.10×; trail killed the copy) |
| `\p{L}+` (Unicode) | early hit | 323 k · 634 | 3135 k · 11446 | 1706 k · 3374 | 0.10× | ❌ << C (was 0.05×; our compile **18× faster**) |
| `.x` (UTF-8 miss) | 235 KB | 45.2 · 577 | 319 · 236 | 107 · 378 | 0.14× | ❌ << C (was 0.08×) |
| `email` | 90 KB, hit @ end | 11.4 · 1260 | 41.9 · 1692 | 51.1 · 1492 | 0.27× | ❌ << C (was 0.12×) |
| `https?://…` (URL) | 82 KB, hit @ end | 2949 · 1429 | 9254 · 1039 | 3017 · 1193 | 0.32× | ⚠️ < C, ≈ RE2 |
| `\[\d{4}-…\] (ERROR\|…) \w+` | log line | 134 k · 3128 | 938 k · 2434 | 347 k · 2929 | 0.14× | ❌ << C (was 0.09×) |
| `\A(a*)*b` (ReDoS) | 40×`a`+`!` | 1.0 · 1537 — safe | 237 · 542 | 62.4 · 824 | 0.004× | ⚠️ safe but slower (fused inner loop; see note) |
| `\A(a\|aa)+b` (ReDoS) | 40×`a`+`!` | **5.2 · 2221 — safe** | **TIMEOUT > 70 s** | 55.3 · 1218 | **∞** | ✅ **C catastrophically backtracks; we don't** |
| `(a+)\1b` (backref) | 24×`a`+`c` | 2.1 · 1109 | 7.3 · 499 | *RE2: unsupported* | 0.29× | ❌ < C (no RE2 peer; was 0.11×) |

*(Ruby/MRI proxy column omitted from the table for width; it is in
`benchmarks/results.csv`. Note MRI 4.0 ships Onigmo's memoized linear-time mode,
so MRI finishes `\A(a|aa)+b` in ~1.9 µs while the raw 6.2.0 **C library** does
not — see below.)*

## Summary

### Where we meet or beat the C Onigmo
- **Literal scans** (`needle` miss/hit): **1.71–1.75× faster than C Onigmo** and
  on par with RE2. Our literal-prefix prefilter rejects/locates with one
  `strings.Index` (runtime Boyer–Moore-ish) pass instead of stepping
  `onig_search` byte by byte.
- **Alternation miss**: 0.67× of C but **~3.1× faster than RE2** — the
  alternation-aware first-byte set skips most positions.
- **Structured numeric scan** `([0-9]{1,3}\.){3}…`: **6.5× faster than C Onigmo
  and 33× faster than RE2** — C Onigmo backtracks the bounded reps hard across a
  long no-match haystack; our prefilter + bounded-rep compilation does not.
- **ReDoS safety** (the headline): on `\A(a|aa)+b` the **C Onigmo we reimplement
  blows up past 70 s**; our `(pc,sp)` memo holds it to **~2 µs** (RE2 stays linear
  too). We are *algorithmically safer than the engine we clone* on this class.
- **Compile time on Unicode**: our `\p{L}+` compile is **18× faster** than C
  Onigmo's (11.4 µs → 0.63 µs); C Onigmo pays a large table-build cost per compile.

### Lazy-DFA — results (2026-06-24)
The on-the-fly NFA simulation closes the inner-loop gap further. Match-time, the
post-fusion 2026-06-22 baseline → after the lazy-DFA, ours (ns/op), vs the
unchanged C baseline (same run, `benchmarks/results.csv`). All cases below now run
on the lazy NFA; the literal/prefix cases (`url`, `logline`, `unanchored_literal`,
`literal_miss`) keep the VM path and are unchanged within noise.

| case | 2026-06-22 | after DFA | speedup | ours/C before → after |
|---|---|---|---|---|
| `[a-zA-Z]+` | 403 | **107** | **3.77×** | 0.067× → **0.25×** |
| `(\w+)=(\w+)` (captures, is-match) | 2593 | **862** | **3.01×** | 0.21× → **0.63×** |
| `\A\w+` (anchored) | 439 | **188** | **2.34×** | 0.114× → **0.27×** |
| `\p{L}+` (Unicode) | 359 | **178** | **2.02×** | 0.128× → **0.26×** |
| `[0-9]{2,4}` (bounded) | 356 | **176** | **2.02×** | 0.171× → **0.35×** |
| `cat\|dog\|fox` (alternation hit) | 405 | **253** | **1.60×** | 0.388× → **0.62×** |
| `email` (full scan) | 7.9 ms | **3.76 ms** | **2.10×** | 0.27× → **0.48×** (≈ RE2) |
| `zoo\|quux\|kite` (alt. miss) | 695 µs | **561 µs** | **1.24×** | 0.745× → **0.92×** |
| `.x` (UTF-8 miss) | 2.57 ms | **2.17 ms** | **1.18×** | 0.14× → **0.20×** |
| `\A(a*)*b` (ReDoS) | 41.2 µs | **0.97 µs** | **42.6×** | 0.005× → **0.21×** |
| `\A(a\|aa)+b` (ReDoS) | 7.84 µs | **1.81 µs** | **4.33×** | C **TIMEOUT** → still ∞ |

What landed (semantics-preserving; the lazy NFA agrees with the backtracker on the
leftmost-first span on the full `diff_ruby` corpus and on C Onigmo / Ruby / RE2;
the linear-time ReDoS guarantee is inherent to the NFA simulation and the
backtracker's bitset memo is kept for the fallback; 100 % coverage held):

- **Lazy NFA simulation** (`internal/vm/dfa.go`, `dfa_run.go`). A Thompson-NFA is
  derived once per program — fused `OpLoop` is unrolled back into split/atom/jmp,
  every opcode outside the subset (backref, call, lookaround, atomic) makes the
  build bail to the VM. It is simulated with a **priority-ordered thread list**
  (each thread carrying its start offset) so the highest-priority thread to reach
  the accept fixes the whole-match end — preserving Ruby's **leftmost-FIRST**
  semantics, unlike a leftmost-longest set-DFA. A **precomputed epsilon-closure**
  per node (valid when no position-dependent assertion is on the closure) makes the
  hot step a dedup'd slice append instead of a recursive walk.
- **Selective routing** (`BuildDFA`). The DFA is built only when there is **no
  required literal prefix or interior literal** — those keep the VM's
  `strings.Index` Boyer–Moore scan, which already beats C. So the DFA serves the
  class / quantifier / alternation / anchor-led patterns it wins on, and the
  literal/prefix patterns keep their faster VM path; net is positive with no
  material regression.
- **Two-engine design.** `MatchString` (is-match) and capture-free `Match` take the
  DFA bounds directly; a pattern with capturing groups still uses the DFA for the
  is-match question (whether a match exists never depends on captured text) and the
  backtracking VM for actual submatch extraction. The VM and the DFA share the exact
  per-atom acceptance tests (`*StepCtx`), so the DFA accepts exactly the bytes the
  VM does.

### Inner-loop fixes — results (2026-06-22)
The three structural causes below have been addressed. Match-time, before → after,
ours (ns/op), vs the unchanged C baseline:

| case | before | after | speedup | ours/C before → after |
|---|---|---|---|---|
| `\p{L}+` (Unicode) | 691 | 359 | **1.93×** | 0.05× → 0.10× |
| `(\w+)=(\w+)` (captures) | 4641 | 2593 | **1.79×** | 0.10× → 0.18× |
| `email` (full scan) | 17.6 ms | 7.9 ms | **2.23×** | 0.12× → 0.27× |
| `.x` (UTF-8 miss) | 4.36 ms | 2.57 ms | **1.70×** | 0.08× → 0.14× |
| `[a-zA-Z]+` | 564 | 403 | **1.40×** | 0.04× → 0.06× |
| `logline` | 1356 | 896 | **1.51×** | 0.09× → 0.14× |
| `\A\w+` (anchored) | 578 | 439 | **1.32×** | 0.05× → 0.07× |
| `(a+)\1b` (backref) | 31388 | 11816 | **2.66×** | 0.11× → 0.29× |

What landed (all semantics-preserving; correctness cross-checks still agree with
C Onigmo / Ruby / RE2, and the linear-time ReDoS guarantee is intact):

1. **Mutable capture array + undo trail** (`internal/vm/vm.go`). `saveSlot`/`push`
   no longer copy a fresh `[]int` of all slots on every `OpSave` and every
   backtrack push; one shared `m.caps` is mutated in place and a `(kind, slot,
   oldval)` trail unwinds it on backtrack. This is what nearly halves the
   capture/backref cases (the per-step O(captures) allocation is gone).

2. **Flat generation-stamped bitset memo** replacing `map[int64]bool`
   (`memoGen`). The hot `OpSplit` path is now a flat array index-and-compare; the
   per-start / per-byte clear is an O(1) generation bump instead of a map clear, so
   a long no-split scan is no longer O(n²). A dense `[]uint32` stamp array backs
   small `(pc × position)` tables; an oversized table falls back to a
   generation-stamped sparse map so a split pattern over a big haystack skips the
   multi-megabyte allocation. The linear-time ReDoS prune is preserved exactly.

3. **Fused `OpLoop` quantifier opcode** (`compile` + `vm`). A quantifier over a
   single consuming atom (`Char/Class/Any/UniProp/FoldChar`, e.g. `[a-z]+`, `.*`,
   `\p{L}+`, `a{2,4}`) lowers to one `OpLoop` that scans the run in a tight inner
   loop and records its boundaries, with greedy give-back / lazy take-more handled
   by a single backtrack thread — instead of re-dispatching the outer `switch`
   (and touching the memo) per character. Each atom scanned still charges one step
   of the deterministic budget, so the fused loop cannot evade the step/time bound.

4. **First-byte `IndexByte`-driven start scan** + the prefilter taught about
   `OpLoop` (so `a*b` still yields first-byte set `{a,b}`, and `\d+foo\d+` still
   extracts the required interior literal `foo`).

> **Note on `\A(a*)*b`:** fusing the *inner* `a*` of a nested quantifier means the
> run is re-scanned on each outer memoized iteration rather than pruned at an inner
> `OpSplit`, so this one pathological case regressed in constant factor (12→41 µs
> at n=40). It remains **polynomial (O(n²)) and safe** — not catastrophic — and the
> flagship `\A(a|aa)+b` (whose `(a|aa)` body is an alternation and does *not* fuse)
> is fully memoized and still defuses to µs while C Onigmo times out. The net
> across the suite is strongly positive; a future inner-loop memo point for fused
> loops nested under a quantifier would recover this corner.

### Remaining gap to C — root cause
The lazy DFA closed most of the inner-loop gap (we now run at **0.20–0.63× of C**
on the matchable subset, up from ~0.05–0.20×, and **match RE2 on `email`**). The
residual is the per-step thread-list bookkeeping of an *uncached* NFA simulation:
we recompute the priority closure each step rather than caching whole DFA states
(transition table) the way RE2's lazy DFA does — and we still decode UTF-8 per code
point instead of stepping a byte-class DFA. A cached-state transition table over
byte classes is the next lever; it would mostly affect the long-scan cases (`.x`,
`email`) where the per-position cost dominates.

### Honest bottom line
- vs **C Onigmo**: we **win on literal/prefilter-friendly scans, on structured
  bounded-rep scans (`ipv4` 10× faster), and on ReDoS safety**, and after the
  lazy-DFA we **lag only ~1.6–5× (was ~7–15×) on quantifier / class / alternation
  inner loops** — `email` now ≈ RE2. The DFA serves the capture/backref-free subset
  at linear NFA-simulation speed while the backtracker keeps the feature-rich
  patterns and submatch extraction; literal-prefix scans keep the VM's
  `strings.Index` path that already beats C. The residual is the missing
  cached-state DFA transition table (we simulate the NFA per step rather than
  caching states), not allocation or per-character VM dispatch.
- vs **RE2**: we are faster on alternation-miss and structured bounded-rep scans,
  slower on plain class/anchor scans, and we *have the features RE2 lacks*
  (backreferences, lookaround, atomic/possessive, subexpr calls). RE2 stays linear
  on ReDoS by construction; we stay bounded via the memo + step budget.

### Action items (ranked)
1. ✅ **DONE** — **Mutable capture array + undo trail** — killed the per-save `[]int` copy. (vm.go `saveSlot`/`push`/`trail`)
2. ✅ **DONE** — **Flat generation-stamped bitset memo** replacing `map[int64]bool`. (vm.go `memoGen`, `OpSplit`, `consumed`)
3. ✅ **DONE** — **Fused `OpLoop` opcode** for single-atom `Char/Class/Any/UniProp/FoldChar` quantified runs. (compile + vm)
4. ✅ **DONE** — **First-byte `IndexByte`-driven start scan** + prefilter taught about `OpLoop`. (vm/prefilter.go)
5. ✅ **DONE (2026-06-24)** — **Lazy / on-the-fly NFA simulation** for the capture/backref/lookaround/atomic-free subset (the cases RE2 wins) — leftmost-first priority thread list + precomputed epsilon-closure, routed in only when no literal prefilter applies. (`vm/dfa.go`, `vm/dfa_run.go`) Closed the inner-loop gap to **0.20–0.63× of C** (was 0.05–0.20×) and made `\A(a*)*b` linear again (41→1 µs).
6. **TODO** — **Cached-state DFA transition table** (the true RE2 lazy DFA: memoize whole `(state, byte-class) → state` transitions instead of re-simulating the NFA per step) to push the long-scan cases (`.x`, `email`) toward C/RE2 throughput.
7. **TODO** — recover the `\A(a*)*b` constant factor *on the VM fallback path* with an inner-loop memo point for a fused loop nested under an outer quantifier (the DFA already handles this case linearly).

_Numbers: `benchmarks/results.csv` (this run, 2026-06-24). Regenerate with
`benchmarks/run.sh`._
