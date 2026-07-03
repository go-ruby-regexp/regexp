package onigmo

import "testing"

// Corpus mirrors the go-ruby-strscan parity harness: a lexer-shaped input.
var scanCorpus = func() string {
	s := ""
	for i := 0; i < 64; i++ {
		s += "foo123 + bar456 - baz789 * qux000 / quux ; "
	}
	return s
}()

var scanPats = []string{`[a-zA-Z_][a-zA-Z0-9_]*`, `[0-9]+`, `\s+`, `[-+*/;]`}

func compileAll(t testing.TB, pats []string) []*Regexp {
	res := make([]*Regexp, len(pats))
	for i, p := range pats {
		re, err := Compile(p)
		if err != nil {
			t.Fatalf("compile %q: %v", p, err)
		}
		res[i] = re
	}
	return res
}

// BenchmarkScanTokenize is the classic lexer loop: anchored MatchAt per token.
func BenchmarkScanTokenize(b *testing.B) {
	res := compileAll(b, scanPats)
	// warm the lazy build
	for _, re := range res {
		re.MatchAt(scanCorpus, 0)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for pos < len(scanCorpus) {
			matched := false
			for _, re := range res {
				if md := re.MatchAt(scanCorpus, pos); md != nil && md.End(0) > pos {
					pos = md.End(0)
					matched = true
					break
				}
			}
			if !matched {
				pos++
			}
		}
	}
}

// BenchmarkMatchQ mirrors strscan match?: anchored MatchAt at every position.
func BenchmarkMatchQ(b *testing.B) {
	re := compileAll(b, []string{`[A-Za-z0-9_]+`})[0]
	re.MatchAt(scanCorpus, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for pos := 0; pos < len(scanCorpus); pos++ {
			re.MatchAt(scanCorpus, pos)
		}
	}
}

// BenchmarkScanUntil mirrors strscan scan_until: forward Match on the tail.
func BenchmarkScanUntil(b *testing.B) {
	re := compileAll(b, []string{`[-+*/;]`})[0]
	re.Match(scanCorpus)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for pos < len(scanCorpus) {
			md := re.Match(scanCorpus[pos:])
			if md == nil {
				break
			}
			pos += md.End(0)
		}
	}
}

// BenchmarkSkip mirrors strscan skip: MatchAt alternating \s+ / \S+ runs.
func BenchmarkSkip(b *testing.B) {
	res := compileAll(b, []string{`\s+`, `\S+`})
	for _, re := range res {
		re.MatchAt(scanCorpus, 0)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for pos < len(scanCorpus) {
			adv := false
			for _, re := range res {
				if md := re.MatchAt(scanCorpus, pos); md != nil && md.End(0) > pos {
					pos = md.End(0)
					adv = true
					break
				}
			}
			if !adv {
				pos++
			}
		}
	}
}

// --- bounds-only variants: the allocation-free path mirroring Ruby's
// StringScanner#skip / #match? / scan (which return an Integer / String, not a
// MatchData). These are what a bounds-only strscan binding uses. ---

func BenchmarkScanTokenizeBounds(b *testing.B) {
	res := compileAll(b, scanPats)
	for _, re := range res {
		re.MatchBoundsAt(scanCorpus, 0)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for pos < len(scanCorpus) {
			matched := false
			for _, re := range res {
				if _, e, ok := re.MatchBoundsAt(scanCorpus, pos); ok && e > pos {
					pos = e
					matched = true
					break
				}
			}
			if !matched {
				pos++
			}
		}
	}
}

func BenchmarkMatchQBounds(b *testing.B) {
	re := compileAll(b, []string{`[A-Za-z0-9_]+`})[0]
	re.MatchBoundsAt(scanCorpus, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for pos := 0; pos < len(scanCorpus); pos++ {
			re.MatchBoundsAt(scanCorpus, pos)
		}
	}
}

func BenchmarkScanUntilBounds(b *testing.B) {
	re := compileAll(b, []string{`[-+*/;]`})[0]
	re.MatchBounds(scanCorpus)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for pos < len(scanCorpus) {
			_, e, ok := re.MatchBounds(scanCorpus[pos:])
			if !ok {
				break
			}
			pos += e
		}
	}
}

func BenchmarkSkipBounds(b *testing.B) {
	res := compileAll(b, []string{`\s+`, `\S+`})
	for _, re := range res {
		re.MatchBoundsAt(scanCorpus, 0)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for pos < len(scanCorpus) {
			adv := false
			for _, re := range res {
				if _, e, ok := re.MatchBoundsAt(scanCorpus, pos); ok && e > pos {
					pos = e
					adv = true
					break
				}
			}
			if !adv {
				pos++
			}
		}
	}
}
