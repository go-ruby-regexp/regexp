package onigmo_test

import (
	"testing"

	onigmo "github.com/go-ruby-regexp/regexp"
)

// These tests pin the \b (word-boundary) and \B (non-word-boundary) zero-width
// assertions added to the engine. They are oracle-independent — the expected
// spans are written out directly (and were cross-checked against MRI 4.0.5 in the
// differential corpus) — so they hold on every CI target including the qemu /
// Windows runs where ruby is absent. Both the lazy-NFA (capture-free) and the
// backtracking-VM (capture-bearing) paths are exercised, in both UTF8 and
// ASCII8BIT encodings, including \b inside a lookaround and \b == backspace
// inside a character class.

// wbSpan compiles p under enc and returns the whole-match [begin,end) span of the
// first match of s as a "b,e" string, or "nil" when there is no match.
func wbSpan(t *testing.T, p string, enc onigmo.Encoding, s string) string {
	t.Helper()
	re, err := onigmo.CompileEnc(p, enc)
	if err != nil {
		t.Fatalf("compile /%s/ enc=%d: %v", p, enc, err)
	}
	m := re.Match(s)
	if m == nil {
		return "nil"
	}
	return itoa(m.Begin(0)) + "," + itoa(m.End(0))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TestWordBoundaryASCII covers the ASCII semantics of \b / \B in the default UTF8
// mode (where the word-char set on ASCII input is [0-9A-Za-z_]).
func TestWordBoundaryASCII(t *testing.T) {
	cases := []struct {
		pat, in, want string
	}{
		{`\bfoo\b`, "foo", "0,3"},
		{`\bfoo\b`, "afoo", "nil"},
		{`\bfoo\b`, "foo!", "0,3"},
		{`\bfoo\b`, "a foo b", "2,5"},
		{`\bfoo\b`, "_foo_", "nil"}, // _ is a word char, so no boundary
		{`\bfoo`, "foo", "0,3"},
		{`foo\b`, "foo", "0,3"},
		{`\bcat\b`, "the cat sat", "4,7"},
		{`\b\w+\b`, "  word  ", "2,6"},
		{`\w+\b`, "hello world", "0,5"},
		{`\w+\b?`, "hello", "0,5"}, // the Puppet-lexer optional-boundary pattern
		{`\b123\b`, "x123x", "nil"},
		{`\b_\b`, "_", "0,1"},
		{`\Bfoo`, "afoo", "1,4"},
		{`\Bfoo`, "foo", "nil"},
		{`\Bing\b`, "running", "4,7"},
		{`a\bb`, "ab", "nil"},
		{`a\Bb`, "ab", "0,2"},
		{`\b`, "a", "0,0"},
		{`\b`, "!", "nil"},
		{`\b`, "", "nil"},
		{`\b`, "a b", "0,0"},
		{`\B`, "a", "nil"},
		{`\B`, "!", "0,0"},
		{`\B`, "", "0,0"},
		{`\B`, "ab", "1,1"},
		{`x*\b`, "xxx yyy", "0,3"},
		{`foo\b?bar`, "foobar", "0,6"},
		{`(?:\bfoo\b|\bbar\b)`, "x bar y", "2,5"},
	}
	for _, c := range cases {
		if got := wbSpan(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("/%s/ on %q: got %s want %s", c.pat, c.in, got, c.want)
		}
	}
}

// TestWordBoundaryCaptureVMPath drives the backtracking VM (a capturing group
// keeps the program off the capture-free lazy-NFA fast path) so the VM's own
// \b / \B handling is exercised independently of the NFA simulation.
func TestWordBoundaryCaptureVMPath(t *testing.T) {
	cases := []struct {
		pat, in, want string
	}{
		{`(\bfoo\b)`, "a foo b", "2,5"},
		{`(\w+)\b`, "hi!", "0,2"},
		{`(\Bar)`, "bar", "1,3"},
		{`(x)\bz`, "x z", "nil"}, // boundary holds after x, but z does not follow a space
	}
	for _, c := range cases {
		if got := wbSpan(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("/%s/ on %q: got %s want %s", c.pat, c.in, got, c.want)
		}
	}
}

// TestWordBoundaryUnicode covers UTF8 mode where \b is Unicode-aware: a word char
// is \p{Word} (letter | mark | decimal number | connector), so an accented or
// non-Latin letter counts as a word char and a run of them is one word — even
// though \w stays ASCII-only. Spans are byte offsets.
func TestWordBoundaryUnicode(t *testing.T) {
	cases := []struct {
		pat, in, want string
	}{
		{`\bcafé\b`, "le café est", "3,8"}, // "café" = bytes 3..8 (é is 2 bytes)
		{`\bé`, "aé", "nil"},               // "aé" is one word run: no inner boundary
		{`\Bé`, "café", "3,5"},             // é is mid-word: a non-boundary precedes it
		{`α\b`, "βα γ", "2,4"},             // Greek letters are word chars
		{`\bнет\b`, "да нет да", "5,11"},   // Cyrillic word run
		// An ellipsis is punctuation (non-word), so position 1 (word 'a' | non-word
		// '…') is a real boundary, not a \B; and no other position yields \B…, so the
		// whole pattern fails. This confirms \b's Unicode word notion classes '…' as
		// non-word.
		{`\B…`, "a…b", "nil"},
	}
	for _, c := range cases {
		if got := wbSpan(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("/%s/ on %q: got %s want %s", c.pat, c.in, got, c.want)
		}
	}
}

// TestWordBoundaryASCII8BIT covers /n (binary) mode, where \b is byte-oriented:
// the word-char set is the ASCII [0-9A-Za-z_] bytes and any high byte (a UTF-8
// continuation or lead byte) is a non-word byte. This is the path through
// asciiWordByte and the ASCII8BIT branch of wordCharBefore / wordCharAfter.
func TestWordBoundaryASCII8BIT(t *testing.T) {
	cases := []struct {
		pat, in, want string
	}{
		{`\b`, "a\xc3\xa9", "0,0"},  // "aé" bytes: boundary at start (a is word)
		{`a\b`, "a\xc3\xa9", "0,1"}, // a is word, next byte 0xc3 is non-word → boundary at 1
		{`\B`, "a\xc3\xa9", "2,2"},  // non-boundary inside the é byte pair
		{`\bfoo\b`, "foo", "0,3"},
		{`\b_\b`, "_", "0,1"},
		// A lone high byte 0xff is a non-word byte; both sides of position 0 are
		// non-word (the empty prefix and the 0xff byte), so position 0 is NOT a
		// boundary → \B holds there.
		{`\B`, "\xff", "0,0"},
	}
	for _, c := range cases {
		if got := wbSpan(t, c.pat, onigmo.ASCII8BIT, c.in); got != c.want {
			t.Errorf("/%s/ on %q (n): got %s want %s", c.pat, c.in, got, c.want)
		}
	}
}

// TestWordBoundaryInLookaround embeds \b / \B inside a lookahead and a lookbehind.
// A lookaround keeps the program out of the lazy-NFA subset, so the assertion is
// evaluated by the backtracking VM's lookaround sub-executor (execLook).
func TestWordBoundaryInLookaround(t *testing.T) {
	cases := []struct {
		pat, in, want string
	}{
		{`foo(?=\b)`, "foo bar", "0,3"},   // boundary right after foo
		{`foo(?=\b)`, "foobar", "nil"},    // no boundary inside foobar
		{`foo(?!\b)`, "foobar", "0,3"},    // negative: NOT a boundary after foo
		{`foo(?=\Bbar)`, "foobar", "0,3"}, // \B before bar (mid-word)
		{`(?<=\b)foo`, "a foo", "2,5"},    // boundary before foo
		{`(?<=\bfoo)bar`, "foobar", "3,6"},
	}
	for _, c := range cases {
		if got := wbSpan(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("/%s/ on %q: got %s want %s", c.pat, c.in, got, c.want)
		}
	}
}

// TestBackspaceInClass pins that \b inside a character class is the BACKSPACE byte
// 0x08, NOT a word boundary, and that this is independent of the new \b assertion.
func TestBackspaceInClass(t *testing.T) {
	cases := []struct {
		pat, in, want string
	}{
		{`[\b]`, "a\bc", "1,2"},
		{`a[\b]c`, "a\bc", "0,3"},
		{`[\ba]+`, "aa\bbb", "0,3"}, // class {backspace,a}: matches "aa\b"
		{`[^\b]+`, "ab\bcd", "0,2"}, // negated: stops at the backspace
		{`[\b-\r]`, "\n", "0,1"},    // backspace as a range low bound (0x08..0x0d, so \n matches)
	}
	for _, c := range cases {
		if got := wbSpan(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("/%s/ on %q: got %s want %s", c.pat, c.in, got, c.want)
		}
	}
}

// TestWordBoundaryInBoundedLoopDFA exercises the cached-DFA fallback path: a \b
// inside a bounded quantifier stays in the DFA subset, so the cached transition
// table is built, but every assertion-crossing position falls back to the
// per-step simulation. The result must still match the backtracker.
func TestWordBoundaryDFAFallback(t *testing.T) {
	// A class run with an interior \b keeps the program capture-free (lazy-NFA /
	// cached-DFA path) yet forces the per-position assertion fallback.
	cases := []struct {
		pat, in, want string
	}{
		{`[a-z]+\b`, "  hello  ", "2,7"},
		{`\b[a-z]+`, "  hello  ", "2,7"},
		{`[a-z]*\b[a-z]*`, "ab cd", "0,2"},
		{`(?:\w\b\W)+`, "a b c ", "0,6"},
	}
	for _, c := range cases {
		if got := wbSpan(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("/%s/ on %q: got %s want %s", c.pat, c.in, got, c.want)
		}
	}
}
