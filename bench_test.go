package onigmo_test

import (
	"strings"
	"testing"
	"time"

	onigmo "github.com/go-onigmo/regexp"
)

// This file is a representative benchmark suite exercising the engine through its
// public API (compile once, match many) over the workloads a real caller hits:
// literal-prefix scanning, alternation, anchored matching, backtracking-heavy
// patterns held in check by the ReDoS guard, subexpression-call recursion,
// multibyte/UTF-8 scanning, and the prefilter fast paths measured against a
// forced-slow baseline on the same haystack. Benchmarks are additive: they do not
// change engine behaviour and are excluded from the coverage gate (they live in an
// external _test package and assert results so a regression still fails loudly).

// hay is a long natural-language haystack reused across the scanning benchmarks. It
// contains no "needle"/"zzz" so the miss benchmarks exercise the worst case: every
// start position (or every byte the prefilter cannot skip) is rejected.
var hay = strings.Repeat("the quick brown fox jumps over the lazy dog. ", 2000)

// benchCompile compiles a pattern for a benchmark, failing it on a parse error.
func benchCompile(b *testing.B, pat string) *onigmo.Regexp {
	b.Helper()
	re, err := onigmo.Compile(pat)
	if err != nil {
		b.Fatalf("compile %q: %v", pat, err)
	}
	return re
}

// benchCompileEnc is benchCompile with an explicit encoding (for the binary-mode
// scanning benchmark).
func benchCompileEnc(b *testing.B, pat string, enc onigmo.Encoding) *onigmo.Regexp {
	b.Helper()
	re, err := onigmo.CompileEnc(pat, enc)
	if err != nil {
		b.Fatalf("compile %q: %v", pat, err)
	}
	return re
}

// --- Literal-prefix scanning: the prefilter locates the literal start. ---

// BenchmarkLiteralScanMiss scans a long haystack for a literal that never occurs.
// The literal-prefix prefilter rejects the whole haystack with one strings.Index
// pass instead of invoking the VM at every offset.
func BenchmarkLiteralScanMiss(b *testing.B) {
	re := benchCompile(b, "needle")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(hay) {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkLiteralScanHit scans to a literal appended at the very end, so the
// prefilter jumps straight there.
func BenchmarkLiteralScanHit(b *testing.B) {
	h := hay + "needle"
	re := benchCompile(b, "needle")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !re.MatchString(h) {
			b.Fatal("expected match")
		}
	}
}

// --- Alternation: a leading alternation with no shared prefix. ---

// BenchmarkAlternationMiss exercises the alternation-aware first-byte set: every
// branch starts with a distinct byte, so the scan skips positions whose byte is in
// none of {z,q,k} (here only the "q" of "quick" survives, then the VM rejects it).
func BenchmarkAlternationMiss(b *testing.B) {
	re := benchCompile(b, "zoo|quux|kite")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(hay) {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkAlternationHit matches one of several alternatives present in the
// haystack, exercising branch selection on a hit.
func BenchmarkAlternationHit(b *testing.B) {
	re := benchCompile(b, "cat|dog|fox")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !re.MatchString(hay) {
			b.Fatal("expected match")
		}
	}
}

// --- Anchored: \A collapses the scan to offset 0. ---

// BenchmarkAnchoredMatch matches an anchored word-run at the front; the prefilter
// collapses the start scan to a single position.
func BenchmarkAnchoredMatch(b *testing.B) {
	re := benchCompile(b, `\A\w+`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !re.MatchString(hay) {
			b.Fatal("expected match")
		}
	}
}

// BenchmarkAnchoredMiss anchors a literal that does not start the haystack, so the
// anchor rejects after a single VM attempt at offset 0.
func BenchmarkAnchoredMiss(b *testing.B) {
	re := benchCompile(b, `\Aneedle`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(hay) {
			b.Fatal("unexpected match")
		}
	}
}

// --- Backtracking-heavy: nested quantifiers the ReDoS memo holds to polynomial. ---

// redosInput is a run of 'a's with a trailing '!' that defeats the final $/b, the
// classic input that makes a naive backtracker blow up exponentially. The memo
// (no backreference in these patterns) collapses it to polynomial work.
var redosInput = strings.Repeat("a", 40) + "!"

// BenchmarkRedosNestedStar runs \A(a*)*b — catastrophic without memoization (the
// nested star explores every partition of the 'a' run). The trailing 'b' is absent
// from redosInput so it never matches; with the (pc,sp) memo the search still
// terminates in polynomial time.
func BenchmarkRedosNestedStar(b *testing.B) {
	re := benchCompile(b, `\A(a*)*b`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(redosInput) {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkRedosAltPlus runs \A(a|aa)+b — another canonical catastrophic pattern
// (the alternation doubles the partitions of the 'a' run), held to polynomial work
// by the memo. The trailing 'b' is absent, so it never matches.
func BenchmarkRedosAltPlus(b *testing.B) {
	re := benchCompile(b, `\A(a|aa)+b`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(redosInput) {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkBacktrackBackref exercises a backreference pattern: the memo is disabled
// (a backref makes the future depend on captured text), so the step budget is the
// backstop. The input is short enough to stay well under the budget while still
// forcing real backtracking.
func BenchmarkBacktrackBackref(b *testing.B) {
	re := benchCompile(b, `(a+)\1b`)
	in := strings.Repeat("a", 24) + "c"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(in) {
			b.Fatal("unexpected match")
		}
	}
}

// --- Subexpression-call recursion: balanced parentheses via \g<…>. ---

// BenchmarkSubexprCallRecursion matches deeply nested balanced parentheses with the
// recursive \g<bal> idiom, exercising the VM's call/return stack and the open-group
// save/restore on each recursion.
func BenchmarkSubexprCallRecursion(b *testing.B) {
	re := benchCompile(b, `\A(?<bal>\((?:[^()]|\g<bal>)*\))\z`)
	in := strings.Repeat("(", 30) + strings.Repeat(")", 30)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !re.MatchString(in) {
			b.Fatal("expected match")
		}
	}
}

// --- Multibyte / UTF-8 scanning: the char-advancing dot and class over UTF-8. ---

// utf8Hay is a long multi-byte haystack: each repetition mixes ASCII with Greek,
// accented Latin, and CJK so the char-advancing dot and byte-oriented class must
// decode whole code points as they scan.
var utf8Hay = strings.Repeat("héllo κόσμε 世界 — ", 4000)

// BenchmarkUTF8DotScan runs `.x` over the UTF-8 haystack: the dot advances a whole
// code point at each position and the trailing literal (absent) forces a full scan.
func BenchmarkUTF8DotScan(b *testing.B) {
	re := benchCompile(b, `.x`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(utf8Hay) {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkUTF8PropScan runs \p{Word}+ over the UTF-8 haystack: a rune-aware
// property atom decodes and classifies each code point (the Word alias spans the
// ASCII, Greek, accented-Latin and CJK letters of the haystack).
func BenchmarkUTF8PropScan(b *testing.B) {
	re := benchCompile(b, `\p{Word}+`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !re.MatchString(utf8Hay) {
			b.Fatal("expected match")
		}
	}
}

// BenchmarkBinaryByteScan runs the same kind of scan in ASCII8BIT (binary) mode,
// where every atom advances a single byte — the contrast to the UTF-8 cursor.
func BenchmarkBinaryByteScan(b *testing.B) {
	re := benchCompileEnc(b, `.x`, onigmo.ASCII8BIT)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(utf8Hay) {
			b.Fatal("unexpected match")
		}
	}
}

// --- Prefilter fast path vs forced-slow baseline on the same haystack. ---

// BenchmarkInteriorLiteralMiss exercises the required-interior-literal prefilter: a
// pattern with no anchor and no leading literal but a mandatory interior "needle"
// lets a single strings.Contains reject the whole haystack.
func BenchmarkInteriorLiteralMiss(b *testing.B) {
	re := benchCompile(b, `\d+needle\d+`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(hay) {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkForcedSlowMiss is the baseline: a leading dot followed by a digit
// defeats every prefilter (no anchor, no leading literal, no usable first byte, and
// the lone trailing atom is below the interior-literal threshold), so the VM runs at
// every start position of the same haystack. It bounds the speedup the prefilters
// buy — compare it against the literal/alternation/interior miss benchmarks above.
func BenchmarkForcedSlowMiss(b *testing.B) {
	re := benchCompile(b, `.\d`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(hay) {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkCompile measures one-shot compilation of a representative pattern (the
// recursive balanced-parens grammar), the cost a caller pays once per Regexp.
func BenchmarkCompile(b *testing.B) {
	const pat = `\A(?<bal>\((?:[^()]|\g<bal>)*\))\z`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := onigmo.Compile(pat); err != nil {
			b.Fatalf("compile: %v", err)
		}
	}
}

// BenchmarkTimeoutGuard measures the per-step overhead of an active wall-clock
// deadline (WithTimeout) on a non-pathological scan: the clock is polled only once
// every few thousand steps, so the cost should be negligible against the plain
// scan. A generous timeout never fires here, isolating the polling overhead.
func BenchmarkTimeoutGuard(b *testing.B) {
	re := benchCompile(b, `.\d`).WithTimeout(time.Hour)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if re.MatchString(hay) {
			b.Fatal("unexpected match")
		}
	}
}
