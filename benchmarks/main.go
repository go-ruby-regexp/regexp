// Command bench runs the cross-engine performance-parity harness for the Go
// side: our from-scratch Onigmo engine (github.com/go-ruby-regexp/regexp) and
// the Go standard library regexp (RE2). It uses the same best-of-N protocol the
// C and Ruby harnesses use so the three result sets are directly comparable.
//
// Output is CSV on stdout:
//
//	engine,case,compile_ns,match_ns_per_op,mb_per_s,matched,begin,end
//
// where compile_ns is the best (minimum) single-compile time, match_ns_per_op is
// the best per-iteration match time over N inner iterations, and mb_per_s is the
// haystack length divided by that time. matched/begin/end record the leftmost
// match span (for cross-engine correctness verification by verify.sh).
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"runtime/debug"
	"time"

	onigmo "github.com/go-ruby-regexp/regexp"
)

const (
	// outerReps is the best-of-N: we take the minimum over this many timed runs
	// to reject scheduler / GC noise (same protocol as the C harness).
	outerReps = 12
	// minInner is the floor on inner iterations; cheap cases get auto-scaled up
	// until a timed batch lasts at least minBatch.
	minInner = 50
	minBatch = 50 * time.Millisecond
)

func main() {
	// Pin to one OS thread and disable the GC's effect on timing as much as
	// possible: single-core, steady-state measurement.
	runtime.GOMAXPROCS(1)
	debug.SetGCPercent(400)

	cases, err := loadCorpus("corpus.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "load corpus:", err)
		os.Exit(1)
	}

	// `bench dump` writes the materialised corpus as TSV so the C and Ruby
	// harnesses match byte-identical haystacks: name<TAB>re2<TAB>pattern_b64<TAB>haystack_b64.
	if len(os.Args) > 1 && os.Args[1] == "dump" {
		for _, c := range cases {
			fmt.Printf("%s\t%v\t%s\t%s\n",
				c.Name, c.RE2,
				base64.StdEncoding.EncodeToString([]byte(c.Pattern)),
				base64.StdEncoding.EncodeToString([]byte(c.Haystack())))
		}
		return
	}

	fmt.Println("engine,case,compile_ns,match_ns_per_op,mb_per_s,matched,begin,end")
	for _, c := range cases {
		hay := c.Haystack()
		benchOurs(c, hay)
		if c.RE2 {
			benchRE2(c, hay)
		}
	}
}

// autoInner picks an inner-iteration count so a single timed batch lasts at
// least minBatch, then returns (count, per-op duration) from the best batch.
func bestNs(do func(n int)) (perOp time.Duration) {
	// Calibrate inner count.
	n := minInner
	for {
		start := time.Now()
		do(n)
		el := time.Since(start)
		if el >= minBatch || n >= 1<<22 {
			break
		}
		// Scale up toward minBatch with headroom.
		if el <= 0 {
			n *= 8
			continue
		}
		factor := int(minBatch/el) + 1
		if factor < 2 {
			factor = 2
		}
		n *= factor
	}
	best := time.Duration(1<<62 - 1)
	for r := 0; r < outerReps; r++ {
		start := time.Now()
		do(n)
		el := time.Since(start)
		if el < best {
			best = el
		}
	}
	return best / time.Duration(n)
}

func mbps(hayLen int, perOp time.Duration) float64 {
	if perOp <= 0 {
		return 0
	}
	return (float64(hayLen) / 1e6) / perOp.Seconds()
}

func benchOurs(c Case, hay string) {
	// Compile (best-of-N single compiles).
	var compiled *onigmo.Regexp
	compNs := bestNs(func(n int) {
		for i := 0; i < n; i++ {
			re, err := onigmo.Compile(c.Pattern)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ours compile %q: %v\n", c.Pattern, err)
				os.Exit(1)
			}
			compiled = re
		}
	})

	// Determine the match span once (verification).
	matched, begin, end := 0, -1, -1
	if md := compiled.Match(hay); md != nil {
		matched, begin, end = 1, md.Begin(0), md.End(0)
	}

	// Match throughput (best-of-N).
	matchNs := bestNs(func(n int) {
		for i := 0; i < n; i++ {
			_ = compiled.MatchString(hay)
		}
	})

	fmt.Printf("ours,%s,%d,%d,%.1f,%d,%d,%d\n",
		c.Name, compNs.Nanoseconds(), matchNs.Nanoseconds(), mbps(len(hay), matchNs),
		matched, begin, end)
}

func benchRE2(c Case, hay string) {
	// RE2 cannot compile some patterns (e.g. backreferences); emit a sentinel.
	if _, err := regexp.Compile(c.Pattern); err != nil {
		fmt.Printf("re2,%s,-1,-1,0.0,-1,-1,-1\n", c.Name)
		return
	}
	var compiled *regexp.Regexp
	compNs := bestNs(func(n int) {
		for i := 0; i < n; i++ {
			re, _ := regexp.Compile(c.Pattern)
			compiled = re
		}
	})

	matched, begin, end := 0, -1, -1
	if loc := compiled.FindStringIndex(hay); loc != nil {
		matched, begin, end = 1, loc[0], loc[1]
	}

	matchNs := bestNs(func(n int) {
		for i := 0; i < n; i++ {
			_ = compiled.MatchString(hay)
		}
	})

	fmt.Printf("re2,%s,%d,%d,%.1f,%d,%d,%d\n",
		c.Name, compNs.Nanoseconds(), matchNs.Nanoseconds(), mbps(len(hay), matchNs),
		matched, begin, end)
}
