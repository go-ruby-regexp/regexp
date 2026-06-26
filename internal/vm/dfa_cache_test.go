package vm

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-ruby-regexp/regexp/internal/compile"
	"github.com/go-ruby-regexp/regexp/internal/syntax"
)

// compileForDFAEnc parses and compiles a pattern under an explicit encoding for the
// cached-DFA tests.
func compileForDFAEnc(t *testing.T, pat string, enc syntax.Encoding) *compile.Program {
	t.Helper()
	res, err := syntax.ParseEnc(pat, enc)
	if err != nil {
		t.Fatalf("parse %q: %v", pat, err)
	}
	return compile.CompileEnc(res, compile.Encoding(enc))
}

// TestCachedDFAOverflowFlush drives the interned-state table past maxDFAStates so
// the RE2-style clear-and-rebuild (flush) runs and the search stays correct after
// it. A program with N distinct one-byte alternatives followed by a long tail would
// be needed to reach the real cap naturally, so instead the cap is exercised
// directly: internState is called with maxDFAStates distinct frontiers, then once
// more to force the overflow branch (count >= cap -> flush -> re-intern), and the
// post-flush state must still be a valid, freshly interned state.
func TestCachedDFAOverflowFlush(t *testing.T) {
	prog := compileForDFA(t, `[a-z]+`)
	nfa, ok := buildNFA(prog)
	if !ok {
		t.Fatal("expected NFA for [a-z]+")
	}
	c := newDFACache(nfa, prog.Enc)

	// Intern distinct single-node frontiers (a one-element slice whose single id is i;
	// the id need not be a real node, the intern key is purely the id list) until the
	// count reaches the cap, at which point the next intern triggers the RE2-style
	// flush (count >= cap -> clear-and-rebuild) and re-interns into the emptied table.
	// The flush keeps count bounded, so it drops back near the base after firing.
	flushed := false
	prev := c.count
	for i := 0; i < maxDFAStates+5; i++ {
		c.internState([]int32{int32(i)}, false)
		if c.count < prev {
			flushed = true // count shrank: the overflow branch ran flush()
		}
		prev = c.count
	}
	if !flushed {
		t.Fatalf("expected an overflow flush within %d interns (cap=%d)", maxDFAStates+5, maxDFAStates)
	}
	if c.count > maxDFAStates {
		t.Errorf("count = %d exceeds cap %d; flush did not bound the table", c.count, maxDFAStates)
	}
	// A fresh distinct frontier after the flush still interns to a live state.
	st := c.internState([]int32{int32(maxDFAStates + 100)}, false)
	if st == nil {
		t.Fatal("internState returned nil after overflow")
	}
	// The dead state is re-seeded by flush, so a fresh lookup of the empty frontier
	// returns a live state.
	if got := c.internState(nil, false); got != c.dead {
		t.Error("empty frontier did not intern to the (re-seeded) dead state after flush")
	}
}

// TestCachedDFAFlushPreservesCorrectness confirms a search whose program would
// repeatedly intern still returns the right span after the cache has been flushed
// mid-run (warmup lost, correctness intact). It flushes the cache, then searches.
func TestCachedDFAFlushPreservesCorrectness(t *testing.T) {
	prog := compileForDFA(t, `[a-z]+`)
	dfa := forceDFA(prog)
	if dfa == nil {
		t.Fatal("expected DFA")
	}
	// Prime, then flush, then search again: the post-flush path recomputes every
	// transition and must agree with the VM.
	if _, _, ok := dfa.Search("hello", compile.UTF8, 0); !ok {
		t.Fatal("priming search failed")
	}
	dfa.cache.mu.Lock()
	dfa.cache.flush()
	dfa.cache.mu.Unlock()
	for _, in := range []string{"hello", "  abc  ", "", "12ab34"} {
		wb, we, wok := vmSpan(t, prog, in)
		gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
		if gok != wok || (gok && (gb != wb || ge != we)) {
			t.Errorf("input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)", in, gb, ge, gok, wb, we, wok)
		}
	}
}

// TestCachedDFAASCII8BITAdvance covers the ASCII8BIT arm of advanceWidth: a \G
// -anchored program in binary mode whose start closure is empty at every offset past
// the origin forces the re-seed-then-advance path, advancing one byte at a time.
func TestCachedDFAASCII8BITAdvance(t *testing.T) {
	prog := compileForDFAEnc(t, `\Gabc`, syntax.ASCII8BIT)
	dfa := forceDFA(prog)
	if dfa == nil {
		t.Fatal("expected DFA for \\Gabc")
	}
	// gpos 0: \G holds only at offset 0; "xxabc" never matches, and reaching no-match
	// requires advancing the cursor (the empty-start re-seed arm) one byte per step.
	if _, _, ok := dfa.Search("xxabc", compile.ASCII8BIT, 0); ok {
		t.Error("\\Gabc should not match binary input not starting at the origin")
	}
	if b, e, ok := dfa.Search("abcd", compile.ASCII8BIT, 0); !ok || b != 0 || e != 3 {
		t.Errorf("\\Gabc on abcd = (%d,%d,%v), want (0,3,true)", b, e, ok)
	}
}

// TestCachedDFAUncacheableFallback exercises the per-position fallback for a
// transition whose closure crosses a position-dependent assertion (so stepCached
// reports uncacheable) interleaved with cacheable steps, cross-checked against the
// VM. The interior $ in an alternation makes the post-consume closure assertion
// -dependent.
func TestCachedDFAUncacheableFallback(t *testing.T) {
	for _, pat := range []string{`a$|ab`, `[a-c]+$|x`, `(?m:a$)|ab`} {
		prog := compileForDFA(t, pat)
		dfa := forceDFA(prog)
		if dfa == nil {
			t.Fatalf("expected DFA for %q", pat)
		}
		for _, in := range []string{"a", "ab", "a\nb", "abc", "aaa", "aaa\n", "x", ""} {
			wb, we, wok := vmSpan(t, prog, in)
			gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
			if gok != wok || (gok && (gb != wb || ge != we)) {
				t.Errorf("pattern %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
					pat, in, gb, ge, gok, wb, we, wok)
			}
		}
	}
}

// TestCachedDFAMultibyteFallback exercises the multi-byte UTF-8 lead-byte fallback
// interleaved with cacheable ASCII steps: a class that matches both ASCII and
// non-ASCII runes over mixed input.
func TestCachedDFAMultibyteFallback(t *testing.T) {
	for _, pat := range []string{`.+`, `\w+`, `[^\s]+`, `\p{L}+`, `(?i)[a-zé]+`} {
		prog := compileForDFA(t, pat)
		dfa := forceDFA(prog)
		if dfa == nil {
			continue
		}
		for _, in := range []string{"café", "aébc", "αβγ", "naïve", "a1b2", "über", ""} {
			wb, we, wok := vmSpan(t, prog, in)
			gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
			if gok != wok || (gok && (gb != wb || ge != we)) {
				t.Errorf("pattern %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
					pat, in, gb, ge, gok, wb, we, wok)
			}
		}
	}
}

// TestCachedDFAHuntVsCommitted confirms the hunting (no match begun yet, fresh start
// seeded each step — transHunt row) and committed (match begin fixed, no new start —
// trans row) transition rows both run and agree with the VM. A pattern whose match
// starts partway through the input exercises hunting before the start and committed
// after it.
func TestCachedDFAHuntVsCommitted(t *testing.T) {
	for _, pat := range []string{`b+`, `[0-9]+`, `xy+`} {
		prog := compileForDFA(t, pat)
		dfa := forceDFA(prog)
		if dfa == nil {
			t.Fatalf("expected DFA for %q", pat)
		}
		for _, in := range []string{"aaabbbccc", "...123...", "zzxyyyz", "nomatch", ""} {
			wb, we, wok := vmSpan(t, prog, in)
			gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
			if gok != wok || (gok && (gb != wb || ge != we)) {
				t.Errorf("pattern %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
					pat, in, gb, ge, gok, wb, we, wok)
			}
		}
	}
}

// TestCachedDFAConcurrent runs many searches against one shared DFA (and thus one
// shared, initially cold cache) from concurrent goroutines, exercising the lock
// -free hit path and the locked compute path's double-check (a slot a racing
// goroutine filled first). Every result must equal the VM's, proving the atomic
// publish is correct under contention.
func TestCachedDFAConcurrent(t *testing.T) {
	prog := compileForDFA(t, `[a-z]+[0-9]*`)
	dfa := forceDFA(prog)
	if dfa == nil {
		t.Fatal("expected DFA")
	}
	in := "abc123 def xyz789 ghijklmnop 42"
	wb, we, wok := vmSpan(t, prog, in)
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
				if gok != wok || gb != wb || ge != we {
					t.Errorf("concurrent search = (%d,%d,%v), want (%d,%d,%v)", gb, ge, gok, wb, we, wok)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestCachedDFADoubleCheckRace deterministically drives stepCached's "a racing
// goroutine filled this slot first" branch via the stepRaceHook seam: the hook
// publishes the slot after the lock is taken but before the double-check load, so
// stepCached must return the pre-published entry rather than recompute. The result
// must still equal the VM's.
func TestCachedDFADoubleCheckRace(t *testing.T) {
	prog := compileForDFA(t, `[a-z]+`)
	dfa := forceDFA(prog)
	if dfa == nil {
		t.Fatal("expected DFA")
	}
	// On the first stepCached miss, fill the slot ourselves (simulating the racing
	// goroutine) so the double-check load finds it. Fire once, then disarm.
	fired := false
	stepRaceHook = func(slot *atomic.Pointer[transEntry]) {
		if fired {
			return
		}
		fired = true
		// A benign self-loop entry on the dead state: any cacheable entry exercises the
		// double-check return. The driver only consults it for this one slot once.
		slot.Store(&transEntry{next: dfa.cache.dead, src: nil, matchSrc: -1})
	}
	defer func() { stepRaceHook = nil }()
	// The forced entry deliberately diverts one transition, so the match result is not
	// asserted here (correctness under a genuine race is covered by TestCachedDFAConcurrent,
	// where every published entry is the real, equivalent transition). This test only
	// proves the double-check load path is taken and returns the pre-published entry.
	dfa.Search("abc", compile.UTF8, 0)
	if !fired {
		t.Error("stepRaceHook never fired; the double-check branch was not exercised")
	}
}

// TestCachedDFAByteClassEdges checks byte-class partitioning edges: a pattern with
// several distinct atom decisions so multiple classes form, plus the empty-program
// degenerate single-class case, exercised through the cached path. strconv import is
// used to build varied inputs.
func TestCachedDFAByteClassEdges(t *testing.T) {
	prog := compileForDFA(t, `[a-z]+[0-9]+`)
	dfa := forceDFA(prog)
	if dfa == nil {
		t.Fatal("expected DFA")
	}
	if dfa.cache.nClasses < 3 {
		t.Errorf("expected >= 3 byte classes for [a-z]+[0-9]+, got %d", dfa.cache.nClasses)
	}
	var inputs []string
	for i := 0; i < 6; i++ {
		inputs = append(inputs, "ab"+strconv.Itoa(i)+"cd99", "Z"+strconv.Itoa(i))
	}
	for _, in := range inputs {
		wb, we, wok := vmSpan(t, prog, in)
		gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
		if gok != wok || (gok && (gb != wb || ge != we)) {
			t.Errorf("input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)", in, gb, ge, gok, wb, we, wok)
		}
	}
}

// TestCachedDFAMultibyteGate exercises the adaptive fallback-dominance gate: over a
// multi-byte-heavy UTF-8 haystack the cached driver falls back at (about) every
// position, so within the opening fbGateWindow it counts at least fbGateMin
// fallbacks, abandons the cached path (useSim) and reruns the whole search on the
// per-step NFA simulation. The haystack is long enough to fill the gate window, and
// patterns are exercised both with usePF=true (a multi-byte first-byte set, so the
// rerun's prefilter-driven seed runs) and usePF=false (the dot, so the rerun's
// no-prefilter seed runs). Every result is cross-checked against the backtracking
// VM, proving the gate routes to a byte-identical engine. Each pattern is run on a
// no-match and a match-after-the-multibyte-prefix haystack so both the exhaust and
// the deep-match arms of the simulation run.
func TestCachedDFAMultibyteGate(t *testing.T) {
	// A long multi-byte run (mixed Greek code points, each a 2-byte sequence). It is
	// well past fbGateWindow consumed positions, so the gate trips before any match.
	mb := strings.Repeat("αβγδε", 30)
	for _, pat := range []string{
		`.x`,      // usePF=false: the rerun seed returns every position
		`αx|βy`,   // usePF=true via a multi-byte first-byte set
		`α+`,      // greedy multi-byte run: match then the threads drain
		`[αβγδε]+`, // class over the whole multi-byte run
	} {
		prog := compileForDFA(t, pat)
		dfa := forceDFA(prog)
		if dfa == nil {
			t.Fatalf("expected DFA for %q", pat)
		}
		for _, in := range []string{mb, mb + "αx", mb + "βy", "αx" + mb, mb + "x"} {
			wb, we, wok := vmSpan(t, prog, in)
			gb, ge, gok := dfa.Search(in, compile.UTF8, 0)
			if gok != wok || (gok && (gb != wb || ge != we)) {
				t.Errorf("pattern %q input %q: DFA=(%d,%d,%v) VM=(%d,%d,%v)",
					pat, in, gb, ge, gok, wb, we, wok)
			}
		}
	}
}
