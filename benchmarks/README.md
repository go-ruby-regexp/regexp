# benchmarks — cross-engine performance parity

A reproducible harness comparing this engine (`github.com/go-ruby-regexp/regexp`,
the from-scratch pure-Go reimplementation of **Onigmo**) against:

- **Onigmo (C) 6.2.0** — the engine we reimplement (and the engine behind Ruby's
  `Regexp`). This is the authoritative bar: *as fast as the C Onigmo we
  reimplement?*
- **Go stdlib `regexp`** (RE2) — the Go peer. Different algorithm (linear-time
  automaton vs backtracking VM), so it is a tradeoff comparison, not a like-for-like
  one: RE2 cannot do backreferences/lookaround, and on a few constructs its
  leftmost-longest scan differs from Onigmo's leftmost-first.
- **Ruby (MRI) `Regexp`** — Onigmo *through* the interpreter. Easy to reproduce,
  but it carries MRI method-dispatch overhead, so treat it as an upper bound on
  "Onigmo via Ruby", not raw C speed.

## Isolation from the coverage gate

This directory is a **separate Go module** (`benchmarks/go.mod`). The root CI runs
`go test ./...` and the 100 % coverage gate over `go list ./...`, neither of which
descends into a nested module, so the harness never affects the gate. The C and
Ruby harnesses are plain source files in `onig/` and `ruby/`.

## Layout

| file | role |
|------|------|
| `corpus.json`      | the single shared corpus (patterns + haystack recipes) every engine runs |
| `corpus.go`        | loads `corpus.json`, materialises haystacks |
| `main.go`          | Go harness: benchmarks **ours** + **RE2**; `bench dump` emits the corpus as TSV |
| `onig/onig_bench.c`| C harness: benchmarks **Onigmo (C)** over the same TSV |
| `ruby/ruby_bench.rb`| Ruby harness: benchmarks **MRI `Regexp`** over the same TSV |
| `run.sh`           | builds Onigmo from source, builds all harnesses, runs them, merges `results.csv` |
| `results.csv`      | last committed run (backs `../BENCHMARKS.md`) |

## Method

Every engine uses the **same** protocol: best (minimum) of 12 timed batches, with
the inner iteration count auto-scaled so a batch lasts ≥ 50 ms, on a single core,
monotonic clock. Compile time is the best single-compile; match time is the best
per-iteration full leftmost search over the haystack. `run.sh` caps each case at a
wall-clock timeout so a catastrophic-backtracking case in the C/Ruby backtrackers
(e.g. `\A(a|aa)+b`) records a `TIMEOUT`/DNF instead of wedging the run.

Correctness is cross-checked: the harness records the leftmost-match byte span
(`begin`,`end`) for every engine; the spans agree across ours / Onigmo C / Ruby on
all matching cases (RE2 agrees here too, since these patterns have no
leftmost-first/longest divergence).

## Run it

```sh
./run.sh                  # full run -> results.csv (+ console table)
PER_CASE_TIMEOUT=120 ./run.sh
```

Needs a C compiler, Go, and (optionally) Ruby. Building Onigmo needs
autoconf/automake/libtool; `run.sh` falls back to `pkgx` to supply them if they
are not already on `PATH`.

A `match_ns_per_op`/`mb_per_s` caveat: for an **early hit** (a match that ends a
few bytes in) MB/s divides the *whole* haystack length by a time that only examined
the matched prefix, so MB/s is inflated for those rows — it is still an apples-to-apples
comparison across engines (all measured identically), just not a literal scan rate.
Read miss/full-scan rows for true scan throughput.
