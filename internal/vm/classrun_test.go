package vm

import (
	"strings"
	"testing"

	"github.com/go-ruby-regexp/regexp/internal/compile"
	"github.com/go-ruby-regexp/regexp/internal/syntax"
)

// classRunPats are the patterns detectClassRun recognises: a whole program that is
// one anchored repeat of a byte-decidable atom (positive / negated byte class,
// dot, single ASCII literal; greedy / lazy; unbounded / bounded / star).
var classRunPats = []string{
	`\s+`, `\S+`, `\w+`, `\W+`, `\d+`, `\D+`,
	`[0-9]+`, `[a-zA-Z0-9_]+`, `[^ ]+`, `[abc]+`, `[^abc]+`,
	`.+`, `.*`, `a+`, `\s*`, `\s+?`, `.{2,5}?`, `\s{2,4}`, `x{0,3}`, `\s{3,}`,
}

// classRunInputs cover the semantic corners: empty, all-match, no-match, a run
// interrupted mid-string, leading / trailing runs, multi-byte UTF-8 (the ASCII
// fast-path fallback), newlines (the dot's exclusion), and tabs/spaces.
var classRunInputs = []string{
	"", " ", "   ", "abc", "aaa", "aaab", "baaa", "   abc   ",
	"foo123 + bar456 - baz789 * qux000 / quux ; ",
	"αβγ δεζ", "  αβ  ", "aαb", "a\nb\n", "\t \t x", "0123456789", "___id_9",
	"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", strings.Repeat("z", 300),
}

// dfaMatchAtGeneral runs MatchAt with the fast anchored consumer disabled, i.e. on
// the general per-step simulation, so a differential test can hold the fast path to
// the exact span the general engine produces on the identical DFA.
func dfaMatchAtGeneral(d *DFA, in string, enc compile.Encoding, pos int) (int, int, bool) {
	cr := d.classRun
	d.classRun = nil
	b, e, ok := d.MatchAt(in, enc, pos)
	d.classRun = cr
	return b, e, ok
}

// TestClassRunVsGeneral is the core differential proof: for every recognised
// class-run pattern and input, at every position, the fast anchored consumer must
// return the byte-identical span the general engine returns on the same DFA — and
// the same span the backtracking VM reports. It exercises the ASCII fast loop, the
// non-ASCII fallback, greedy vs lazy, bounded caps, and the empty / all / no-match
// corners.
func TestClassRunVsGeneral(t *testing.T) {
	for _, pat := range classRunPats {
		prog, err := compileProg(pat)
		if err != nil {
			t.Fatalf("compile %q: %v", pat, err)
		}
		dfa := forceDFA(prog)
		if dfa == nil {
			t.Fatalf("pat=%q: expected a DFA", pat)
		}
		if dfa.classRun == nil {
			t.Fatalf("pat=%q: expected detectClassRun to recognise it", pat)
		}
		for _, in := range classRunInputs {
			for pos := 0; pos <= len(in); pos++ {
				fb, fe, fok := dfa.MatchAt(in, prog.Enc, pos)
				gb, ge, gok := dfaMatchAtGeneral(dfa, in, prog.Enc, pos)
				if fok != gok || (fok && (fb != gb || fe != ge)) {
					t.Fatalf("pat=%q in=%q pos=%d: fast=(%d,%d,%v) general=(%d,%d,%v)",
						pat, in, pos, fb, fe, fok, gb, ge, gok)
				}
				vb, ve, vok := vmMatchAtBounds(prog, in, pos)
				if fok != vok || (fok && (fb != vb || fe != ve)) {
					t.Fatalf("pat=%q in=%q pos=%d: fast=(%d,%d,%v) VM=(%d,%d,%v)",
						pat, in, pos, fb, fe, fok, vb, ve, vok)
				}
			}
		}
	}
}

// TestClassRunASCII8BIT checks the binary-encoding path: under ASCII8BIT every byte
// is a one-byte code point, so a high byte is decided by the class bitset (no
// fallback) and a negated class such as \S consumes it. The fast consumer must
// still agree with the general engine byte-for-byte.
func TestClassRunASCII8BIT(t *testing.T) {
	pats := []string{`\S+`, `[^ ]+`, `.+`, "[\x80-\xff]+"}
	inputs := []string{"a\x80\xffb", "\x80\x80", "  \xff  ", "abc", ""}
	for _, pat := range pats {
		res, err := syntax.ParseEnc(pat, syntax.ASCII8BIT)
		if err != nil {
			t.Fatalf("compile %q: %v", pat, err)
		}
		prog := compile.CompileEnc(res, compile.ASCII8BIT)
		dfa := forceDFA(prog)
		if dfa == nil || dfa.classRun == nil {
			t.Fatalf("pat=%q: expected a class-run DFA", pat)
		}
		for _, in := range inputs {
			for pos := 0; pos <= len(in); pos++ {
				fb, fe, fok := dfa.MatchAt(in, prog.Enc, pos)
				gb, ge, gok := dfaMatchAtGeneral(dfa, in, prog.Enc, pos)
				if fok != gok || (fok && (fb != gb || fe != ge)) {
					t.Fatalf("pat=%q in=%q pos=%d: fast=(%d,%d,%v) general=(%d,%d,%v)",
						pat, in, pos, fb, fe, fok, gb, ge, gok)
				}
			}
		}
	}
}

// TestDetectClassRunRejects checks that programs outside the recognised shape get
// no classRun and fall to the general engine: a capture, a leading anchor, a
// rune-aware class (code-point range, \p{…}, /i fold), a possessive/atomic wrapper,
// a multi-atom body, a high-byte literal repeat, and a plain single atom.
func TestDetectClassRunRejects(t *testing.T) {
	rejects := []string{
		`(\s+)`,      // capturing group
		`\A\s+`,      // leading anchor
		`[α-ω]+`,     // rune-aware code-point range
		`\p{L}+`,     // \p{…} member
		`(?i)[a-z]+`, // /i fold
		`\s++`,       // possessive -> atomic wrapper
		`ab+`,        // multi-atom body (the loop is only over b, program has an OpChar before it)
		`[a-z]`,      // single atom, not a repeat
		`\s+x`,       // repeat followed by more
		`foo`,        // literal
	}
	for _, pat := range rejects {
		prog, err := compileProg(pat)
		if err != nil {
			t.Fatalf("compile %q: %v", pat, err)
		}
		if cr := detectClassRun(prog); cr != nil {
			t.Fatalf("pat=%q: expected detectClassRun to reject, got %+v", pat, cr)
		}
	}
	// A high-byte single-literal repeat: recognised shape, but its literal is >= 0x80,
	// which the ASCII fast loop can never reach, so detectClassRun declines it. Built
	// under ASCII8BIT, where a lone 0x80 byte is a valid one-byte literal.
	res, err := syntax.ParseEnc("\x80+", syntax.ASCII8BIT)
	if err != nil {
		t.Fatalf("compile high-byte literal: %v", err)
	}
	if cr := detectClassRun(compile.CompileEnc(res, compile.ASCII8BIT)); cr != nil {
		t.Fatalf("high-byte literal repeat: expected reject, got %+v", cr)
	}
}

// TestClassRunMatchBranches drives match() through each definite outcome directly:
// a greedy run to end-of-input, a greedy run stopped by a rejecting ASCII byte, a
// bounded cap, a min-not-met failure, and a lazy stop at the minimum.
func TestClassRunMatchBranches(t *testing.T) {
	mk := func(pat string) *classRun {
		prog, err := compileProg(pat)
		if err != nil {
			t.Fatalf("compile %q: %v", pat, err)
		}
		cr := detectClassRun(prog)
		if cr == nil {
			t.Fatalf("pat=%q: expected a class-run", pat)
		}
		return cr
	}
	type want struct {
		end          int
		ok, definite bool
	}
	cases := []struct {
		pat, in string
		pos     int
		w       want
	}{
		{`\s+`, "   x", 0, want{3, true, true}},       // greedy, stopped by rejecting byte
		{`\s+`, "   ", 0, want{3, true, true}},        // greedy, stopped by end-of-input
		{`\s+`, "x", 0, want{0, false, true}},         // min not met (no whitespace)
		{`\s{2,4}`, "      ", 0, want{4, true, true}}, // greedy capped at max
		{`\s{2,4}`, " x", 0, want{0, false, true}},    // fewer than min available
		{`\s+?`, "    ", 0, want{1, true, true}},      // lazy stops at minimum
		{`.{2,5}?`, "abcdef", 0, want{2, true, true}}, // lazy bounded stops at min
		{`\s*`, "x", 0, want{0, true, true}},          // star matches empty
		{`\S+`, "aé", 0, want{0, false, false}},       // non-ASCII -> fall back (indefinite)
	}
	for _, c := range cases {
		cr := mk(c.pat)
		end, ok, definite := cr.match(c.in, compile.UTF8, c.pos)
		if definite != c.w.definite || ok != c.w.ok || (definite && ok && end != c.w.end) {
			t.Fatalf("pat=%q in=%q pos=%d: got (end=%d,ok=%v,def=%v) want (end=%d,ok=%v,def=%v)",
				c.pat, c.in, c.pos, end, ok, definite, c.w.end, c.w.ok, c.w.definite)
		}
	}
}
