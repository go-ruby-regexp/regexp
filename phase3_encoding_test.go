package onigmo_test

import (
	"testing"

	onigmo "github.com/go-ruby-regexp/regexp"
)

// Phase 3 multi-encoding: the first slice gives the Regexp a first-class
// encoding mode. In the default UTF-8 mode the dot and a byte-oriented class
// advance by a whole code point (so `/./` matches a complete multi-byte
// character, resolving the engine's original "dot matches one byte" limitation);
// in ASCII8BIT (binary, Ruby /n) mode every atom advances a single byte. These
// tests are oracle-independent — they encode the behaviour directly rather than
// shelling out to MRI — and cover both modes plus the lookbehind width and
// per-byte folding/property interactions.

// matchStr compiles p under enc and returns the whole match for s, or a sentinel
// "<nil>" when there is no match.
func matchStr(t *testing.T, p string, enc onigmo.Encoding, s string) string {
	t.Helper()
	re, err := onigmo.CompileEnc(p, enc)
	if err != nil {
		t.Fatalf("compile /%s/ enc=%d: %v", p, enc, err)
	}
	m := re.Match(s)
	if m == nil {
		return "<nil>"
	}
	return m.Str(0)
}

func TestEncodingDefaultIsUTF8(t *testing.T) {
	re, err := onigmo.Compile(`.`)
	if err != nil {
		t.Fatal(err)
	}
	if re.Encoding() != onigmo.UTF8 {
		t.Errorf("default encoding = %d, want UTF8", re.Encoding())
	}
	re2, _ := onigmo.CompileEnc(`.`, onigmo.ASCII8BIT)
	if re2.Encoding() != onigmo.ASCII8BIT {
		t.Errorf("CompileEnc ASCII8BIT encoding = %d, want ASCII8BIT", re2.Encoding())
	}
}

func TestDotAdvancesCodePointUTF8(t *testing.T) {
	cases := []struct {
		pat, in, want string
	}{
		{`.`, "é", "é"},      // 2-byte char, one dot
		{`.`, "中", "中"},     // 3-byte char
		{`.`, "😀", "😀"},     // 4-byte char
		{`.+`, "héllo", "héllo"},
		{`a.c`, "aéc", "aéc"},
		{`a.c`, "a中c", "a中c"},
		{`.{2}`, "éé", "éé"},
		{`(.)(.)`, "éx", "éx"},
		{`.`, "ab", "a"}, // ASCII unchanged: one byte = one char
		{`.`, "", "<nil>"},
	}
	for _, c := range cases {
		if got := matchStr(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("UTF8 /%s/ on %q = %q, want %q", c.pat, c.in, got, c.want)
		}
	}
}

func TestDotAdvancesOneByteASCII8BIT(t *testing.T) {
	// Binary mode: the dot consumes exactly one byte of a multi-byte sequence —
	// the engine's original byte-oriented behaviour, equivalent to Ruby's /n.
	if got := matchStr(t, `.`, onigmo.ASCII8BIT, "é"); got != "\xc3" {
		t.Errorf("ASCII8BIT /./ on é = %q, want first byte 0xc3", got)
	}
	if got := matchStr(t, `..`, onigmo.ASCII8BIT, "é"); got != "é" {
		t.Errorf("ASCII8BIT /../ on é = %q, want both bytes", got)
	}
	if got := matchStr(t, `.{3}`, onigmo.ASCII8BIT, "中"); got != "中" {
		t.Errorf("ASCII8BIT /.{3}/ on 中 = %q, want all three bytes", got)
	}
}

func TestDotNewlineExclusion(t *testing.T) {
	// The dot excludes a bare '\n' (one byte) unless dot-all /m is set, in both
	// encodings — the exclusion tests the leading byte, identical either way.
	for _, enc := range []onigmo.Encoding{onigmo.UTF8, onigmo.ASCII8BIT} {
		if got := matchStr(t, `.`, enc, "\n"); got != "<nil>" {
			t.Errorf("enc=%d /./ on \\n = %q, want no match", enc, got)
		}
		if got := matchStr(t, `(?m).`, enc, "\n"); got != "\n" {
			t.Errorf("enc=%d /(?m)./ on \\n = %q, want \\n", enc, got)
		}
		if got := matchStr(t, `.`, enc, "a\nb"); got != "a" {
			t.Errorf("enc=%d /./ on a\\nb = %q, want a", enc, got)
		}
	}
}

func TestByteClassAdvancesCodePointUTF8(t *testing.T) {
	cases := []struct {
		pat, in, want string
	}{
		{`[^a]`, "éx", "é"},        // negated class consumes the whole char
		{`[^a]+`, "héllo", "héllo"},
		{`[^x]`, "中x", "中"},
		{`[a-z]`, "é", "<nil>"},     // positive ASCII range fails on é
		{`[a-z]+`, "héllo", "h"},    // h matches; é breaks the run
		{`[\d]`, "中5", "5"},        // digit class skips past the multi-byte char
		{`[abc]`, "中a", "a"},
	}
	for _, c := range cases {
		if got := matchStr(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("UTF8 /%s/ on %q = %q, want %q", c.pat, c.in, got, c.want)
		}
	}
}

func TestByteClassAdvancesOneByteASCII8BIT(t *testing.T) {
	// Binary mode: a negated class consumes one byte, so [^a] matches the first
	// byte of é; a positive ASCII range still fails on a high byte.
	if got := matchStr(t, `[^a]`, onigmo.ASCII8BIT, "é"); got != "\xc3" {
		t.Errorf("ASCII8BIT /[^a]/ on é = %q, want first byte", got)
	}
	if got := matchStr(t, `[^a]+`, onigmo.ASCII8BIT, "é"); got != "é" {
		t.Errorf("ASCII8BIT /[^a]+/ on é = %q, want both bytes", got)
	}
	if got := matchStr(t, `[a-z]`, onigmo.ASCII8BIT, "é"); got != "<nil>" {
		t.Errorf("ASCII8BIT /[a-z]/ on é = %q, want no match", got)
	}
}

func TestLookbehindCharWidthUTF8(t *testing.T) {
	// In UTF-8 mode a dot/byte-class inside a fixed-width lookbehind has a
	// variable byte width (1..4); the candidate-position scan finds the
	// character-aligned start, so a lookbehind over a multi-byte char matches.
	cases := []struct {
		pat, in, want string
	}{
		{`(?<=.)x`, "éx", "x"},
		{`(?<=.)x`, "x", "<nil>"}, // nothing precedes x
		{`(?<=a.)c`, "aéc", "c"},
		{`(?<=[^q])z`, "中z", "z"},
		{`(?<=é)x`, "éx", "x"}, // multi-byte literal lookbehind (already byte-exact)
	}
	for _, c := range cases {
		if got := matchStr(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("UTF8 /%s/ on %q = %q, want %q", c.pat, c.in, got, c.want)
		}
	}
	// Binary mode: the dot is one byte, so the lookbehind is byte-exact.
	if got := matchStr(t, `(?<=..)x`, onigmo.ASCII8BIT, "éx"); got != "x" {
		t.Errorf("ASCII8BIT /(?<=..)x/ on éx = %q, want x", got)
	}
}

func TestFoldAndPropPerByteASCII8BIT(t *testing.T) {
	// Binary mode: case-folding (/i) and \p{…} properties operate per byte and
	// ASCII-only. An ASCII fold still works on a single byte; a multi-byte case
	// partner (the Kelvin sign U+212A, /k/i's UTF-8 orbit member) does not fold —
	// its bytes are read one at a time.
	if got := matchStr(t, `(?i)k`, onigmo.ASCII8BIT, "K"); got != "K" {
		t.Errorf("ASCII8BIT /(?i)k/ on ASCII K = %q, want K", got)
	}
	if got := matchStr(t, `(?i)k`, onigmo.ASCII8BIT, "K"); got != "<nil>" {
		t.Errorf("ASCII8BIT /(?i)k/ on Kelvin sign = %q, want no match (per-byte)", got)
	}
	// \p{Word} on an ASCII letter byte matches; on a high byte (a lone non-ASCII
	// byte) it does not, advancing one byte.
	if got := matchStr(t, `\p{Word}`, onigmo.ASCII8BIT, "a"); got != "a" {
		t.Errorf("ASCII8BIT /\\p{Word}/ on a = %q, want a", got)
	}
	if got := matchStr(t, `\p{Word}`, onigmo.ASCII8BIT, "é"); got != "<nil>" {
		t.Errorf("ASCII8BIT /\\p{Word}/ on é = %q, want no match per byte", got)
	}
	// A rune-aware class member (a \p{…} inside a class) likewise cannot match a
	// single byte in binary mode; only the byte ranges of the class apply.
	if got := matchStr(t, `[\p{L}0-9]`, onigmo.ASCII8BIT, "é5"); got != "5" {
		t.Errorf("ASCII8BIT /[\\p{L}0-9]/ on é5 = %q, want 5 (byte range only)", got)
	}
}

func TestFoldAndPropCodePointUTF8(t *testing.T) {
	// UTF-8 mode is unchanged for the rune-aware atoms: a Kelvin sign folds to k,
	// \p{Word} matches a multi-byte letter, and a rune-aware class spans a char.
	if got := matchStr(t, `(?i)k`, onigmo.UTF8, "K"); got != "K" {
		t.Errorf("UTF8 /(?i)k/ on Kelvin sign = %q, want the Kelvin sign", got)
	}
	if got := matchStr(t, `\p{L}`, onigmo.UTF8, "中"); got != "中" {
		t.Errorf("UTF8 /\\p{L}/ on 中 = %q, want 中", got)
	}
	if got := matchStr(t, `[\p{L}0-9]+`, onigmo.UTF8, "a中9"); got != "a中9" {
		t.Errorf("UTF8 /[\\p{L}0-9]+/ on a中9 = %q, want a中9", got)
	}
}

func TestInvalidUTF8LeadByteAdvancesOneByteUTF8(t *testing.T) {
	// MRI raises on invalid UTF-8; this engine is lenient — an invalid lead byte
	// decodes as the replacement rune with width 1, so the dot advances one byte
	// rather than stalling. A documented divergence, asserted directly here.
	if got := matchStr(t, `.`, onigmo.UTF8, "\xff"); got != "\xff" {
		t.Errorf("UTF8 /./ on lone 0xff = %q, want the single byte", got)
	}
	if got := matchStr(t, `[^q]`, onigmo.UTF8, "\xff"); got != "\xff" {
		t.Errorf("UTF8 /[^q]/ on lone 0xff = %q, want the single byte", got)
	}
}

func TestDotClassTriedAtContinuationByteFailsUTF8(t *testing.T) {
	// Force the scan to attempt the dot and a byte-oriented class at a UTF-8
	// continuation byte: a leading dot/class defeats the start-position prefilter,
	// so every offset is tried, and a non-matching tail makes the earlier offsets
	// fail and the cursor reach the interior continuation byte — where the atom
	// must fail (never matching off a code-point boundary). On "éx" with no 'z',
	// offset 0 matches the lead but the trailing 'z' fails, offset 1 is the
	// continuation byte of é, and offset 2 (the 'x') also lacks a following 'z'.
	if got := matchStr(t, `.z`, onigmo.UTF8, "éx"); got != "<nil>" {
		t.Errorf("UTF8 /.z/ on éx = %q, want no match", got)
	}
	// The 'z' is present (so the interior-literal prefilter does not reject the
	// whole input) but never preceded by a single character at every offset, so
	// the class is genuinely tried at the continuation byte of é and fails.
	if got := matchStr(t, `[^x]z`, onigmo.UTF8, "éxz"); got != "<nil>" {
		t.Errorf("UTF8 /[^x]z/ on éxz = %q, want no match", got)
	}
	// A rune-aware folded literal (/i) is likewise tried at the continuation byte
	// and must fail there. The 'z' is present so the scan runs the VM at every
	// offset, including the interior continuation byte of é.
	if got := matchStr(t, `(?i)éz`, onigmo.UTF8, "éxz"); got != "<nil>" {
		t.Errorf("UTF8 /(?i)éz/ on éxz = %q, want no match", got)
	}
	// The same patterns DO match when the tail is present, proving the dot/class
	// still advance a whole code point at a valid boundary.
	if got := matchStr(t, `.z`, onigmo.UTF8, "éz"); got != "éz" {
		t.Errorf("UTF8 /.z/ on éz = %q, want éz", got)
	}
	if got := matchStr(t, `[^q]z`, onigmo.UTF8, "éz"); got != "éz" {
		t.Errorf("UTF8 /[^q]z/ on éz = %q, want éz", got)
	}
}

func TestBinaryByteClassNoMatch(t *testing.T) {
	// Binary mode, byte-oriented class that fails on every byte. A leading dot
	// defeats the start-position prefilter so the VM actually runs and tests the
	// class against each byte: é's two high bytes and the '!' are all outside
	// [a-z], so there is no match and the per-byte no-match branch is exercised.
	if got := matchStr(t, `.[a-z]`, onigmo.ASCII8BIT, "é!"); got != "<nil>" {
		t.Errorf("ASCII8BIT /.[a-z]/ on é! = %q, want no match", got)
	}
}

func TestContinuationByteNeverStartsMatchUTF8(t *testing.T) {
	// Like MRI, which positions only at character boundaries, the dot and a
	// byte-oriented class never begin a match on a UTF-8 continuation byte, so the
	// only match offset is code-point-aligned. Anchoring after the lead byte still
	// finds the character at its boundary.
	re, err := onigmo.Compile(`.`)
	if err != nil {
		t.Fatal(err)
	}
	m := re.Match("é") // é = 0xc3 0xa9; offset 1 is the continuation byte
	if m == nil || m.Begin(0) != 0 || m.End(0) != 2 {
		t.Errorf("/./ on é: begin/end = %d/%d, want 0/2", m.Begin(0), m.End(0))
	}
}
