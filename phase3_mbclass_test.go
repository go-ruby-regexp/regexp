package onigmo_test

import (
	"testing"

	onigmo "github.com/go-ruby-regexp/regexp"
)

// Phase 3 multi-encoding (literal multi-byte character-class members): in the
// default UTF-8 mode a non-ASCII literal inside a class is a whole code point and
// a non-ASCII range is a code-point range, so [é] matches the character "é",
// [à-ï] matches that code-point range, and a mixed class such as [a-zé] works.
// In ASCII8BIT (binary, /n) mode the class stays byte-oriented and a high byte is
// a single-byte member. These tests are oracle-independent — they encode the
// behaviour directly rather than shelling out to MRI.

// mbMatch compiles p under enc and returns the whole match for s, or "<nil>" when
// there is no match.
func mbMatch(t *testing.T, p string, enc onigmo.Encoding, s string) string {
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

// TestMBClassSingleMember covers a single multi-byte member in UTF-8 mode: the
// member is the whole code point, not its raw bytes.
func TestMBClassSingleMember(t *testing.T) {
	cases := []struct{ pat, in, want string }{
		{`[é]`, "é", "é"},
		{`[é]`, "héllo", "é"},
		{`[é]`, "e", "<nil>"},  // ASCII 'e' is not the code point é
		{`[é]`, "naïve", "<nil>"}, // ï is not é
		{`[αβγ]`, "βx", "β"},
		{`[αβγ]`, "δ", "<nil>"},
		{`[中文]`, "文字", "文"},
		{`[中文]`, "字", "<nil>"},
	}
	for _, c := range cases {
		if got := mbMatch(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("UTF8 /%s/ on %q = %q, want %q", c.pat, c.in, got, c.want)
		}
	}
}

// TestMBClassNegated covers negation of a multi-byte member: [^é] rejects é and
// consumes any other whole code point.
func TestMBClassNegated(t *testing.T) {
	cases := []struct{ pat, in, want string }{
		{`[^é]`, "x", "x"},
		{`[^é]`, "中", "中"}, // a whole 3-byte code point is consumed
		{`[^é]`, "é", "<nil>"},
		{`[^é]+`, "héllo", "h"}, // stops at é; matches the leading h only
	}
	for _, c := range cases {
		if got := mbMatch(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("UTF8 /%s/ on %q = %q, want %q", c.pat, c.in, got, c.want)
		}
	}
}

// TestMBClassRange covers a multi-byte code-point range whose both bounds are
// non-ASCII.
func TestMBClassRange(t *testing.T) {
	cases := []struct{ pat, in, want string }{
		{`[à-ï]`, "ç", "ç"}, // ç (U+00E7) is inside à..ï (U+00E0..U+00EF)
		{`[à-ï]`, "à", "à"}, // low bound inclusive
		{`[à-ï]`, "ï", "ï"}, // high bound inclusive
		{`[à-ï]`, "ø", "<nil>"}, // ø (U+00F8) is past ï
		{`[à-ï]`, "z", "<nil>"}, // ASCII below the range
		{`[α-ω]`, "λ", "λ"},
		{`[α-ω]`, "Α", "<nil>"}, // uppercase Greek Α is below α
	}
	for _, c := range cases {
		if got := mbMatch(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("UTF8 /%s/ on %q = %q, want %q", c.pat, c.in, got, c.want)
		}
	}
}

// TestMBClassAsciiLowMultibyteHigh covers a range whose low bound is ASCII and
// whose high bound is a multi-byte code point ([a-é]): it is a code-point range
// spanning ASCII into the multi-byte space.
func TestMBClassAsciiLowMultibyteHigh(t *testing.T) {
	cases := []struct{ pat, in, want string }{
		{`[a-é]`, "c", "c"},  // ASCII inside the range
		{`[a-é]`, "z", "z"},  // ASCII z (U+007A) is below é (U+00E9)
		{`[a-é]`, "à", "à"},  // à (U+00E0) is inside a..é
		{`[a-é]`, "é", "é"},  // high bound inclusive
		{`[a-é]`, "ÿ", "<nil>"}, // ÿ (U+00FF) is past é
		{`[a-é]`, "A", "<nil>"}, // uppercase A (U+0041) is below the low bound a
	}
	for _, c := range cases {
		if got := mbMatch(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("UTF8 /%s/ on %q = %q, want %q", c.pat, c.in, got, c.want)
		}
	}
}

// TestMBClassMixed covers a class mixing an ASCII byte range with a multi-byte
// member, plus combinations with class escapes and properties. The class is
// rune-aware (a multi-byte member forces it), but the ASCII byte range still
// matches ASCII.
func TestMBClassMixed(t *testing.T) {
	cases := []struct{ pat, in, want string }{
		{`[a-zé]`, "é", "é"},
		{`[a-zé]`, "m", "m"},
		{`[a-zé]`, "Z", "<nil>"}, // uppercase not in a-z and not é
		{`[a-zé]+`, "abcéZ", "abcé"},
		{`[-é]`, "-", "-"},  // leading dash is literal
		{`[-é]`, "é", "é"},
		{`[é-]`, "-", "-"}, // trailing dash is literal
		{`[é-]`, "é", "é"},
		{`[é\d]`, "5", "5"},
		{`[é\d]`, "é", "é"},
		{`[é\d]`, "x", "<nil>"},
		{`[é\p{L}]`, "Z", "Z"},
		{`[é\p{L}]`, "é", "é"},
		{`[é\p{L}]`, "9", "<nil>"},
	}
	for _, c := range cases {
		if got := mbMatch(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("UTF8 /%s/ on %q = %q, want %q", c.pat, c.in, got, c.want)
		}
	}
}

// TestMBClassAsciiRangeStaysByteOriented confirms an all-ASCII class or range in
// UTF-8 mode is still byte-oriented: a multi-byte input character is not in
// [a-z], so it does not match (and a negated all-ASCII class consumes a whole
// code point, the existing byte-oriented behaviour).
func TestMBClassAsciiRangeStaysByteOriented(t *testing.T) {
	cases := []struct{ pat, in, want string }{
		{`[a-z]`, "é", "<nil>"}, // é code point exceeds the ASCII range
		{`[a-z]+`, "abcé", "abc"},
		{`[abc]`, "中", "<nil>"},
		{`[^a]`, "é", "é"}, // negated ASCII class consumes a whole code point
	}
	for _, c := range cases {
		if got := mbMatch(t, c.pat, onigmo.UTF8, c.in); got != c.want {
			t.Errorf("UTF8 /%s/ on %q = %q, want %q", c.pat, c.in, got, c.want)
		}
	}
}

// TestMBClassASCII8BITByteOriented confirms that in binary mode a high byte
// stays a single-byte class member: [é] (raw bytes 0xC3 0xA9) is two byte
// members, each matching one byte, exactly as MRI's /n.
func TestMBClassASCII8BITByteOriented(t *testing.T) {
	// [é] in binary mode is the two members 0xC3 and 0xA9.
	re, err := onigmo.CompileEnc(`[é]`, onigmo.ASCII8BIT)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, b := range []byte{0xC3, 0xA9} {
		s := string([]byte{b})
		if m := re.Match(s); m == nil || m.Str(0) != s {
			t.Errorf("ASCII8BIT [é] on byte %#x: got %v, want a one-byte match", b, m)
		}
	}
	if m := re.Match("a"); m != nil {
		t.Errorf("ASCII8BIT [é] on \"a\": expected no match, got %q", m.Str(0))
	}
	// A negated high-byte class is byte-oriented too: [^é] in binary mode rejects
	// 0xC3 and 0xA9 but accepts any other single byte.
	reNeg, err := onigmo.CompileEnc(`[^é]`, onigmo.ASCII8BIT)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if m := reNeg.Match("a"); m == nil || m.Str(0) != "a" {
		t.Errorf("ASCII8BIT [^é] on \"a\": got %v, want \"a\"", m)
	}
	if m := reNeg.Match(string([]byte{0xC3})); m != nil {
		t.Errorf("ASCII8BIT [^é] on 0xC3: expected no match, got %q", m.Str(0))
	}
}

// TestMBClassRangeError checks parse errors of code-point ranges in UTF-8 mode
// (outside /i): an inverted multi-byte range, an inverted ASCII-low/multibyte-high
// range, and a class escape or property used as a range end — each rejected just
// as MRI raises "empty range" or "invalid range end".
func TestMBClassRangeError(t *testing.T) {
	for _, pat := range []string{
		`[é-à]`,    // inverted multi-byte range (é U+00E9 > à U+00E0)
		`[ï-a]`,    // inverted: multi-byte low, ASCII high
		`[ω-α]`,    // inverted Greek range
		`[é-\d]`,    // a class escape cannot end a range
		`[é-\p{L}]`, // nor can a property
		`[a-\p{L}]`, // ditto with an ASCII low bound (rune-aware high-bound parse)
		`[é-\q]`,    // an unsupported escape as a range end propagates the error
	} {
		if _, err := onigmo.Compile(pat); err == nil {
			t.Errorf("Compile(%q): expected a range error", pat)
		}
	}
}

// TestMBClassRangeErrorASCII8BIT covers the byte-oriented range-error branches,
// which in binary mode are the only path (a high byte is a byte member, never a
// code point): a class escape or property as a range end, an unsupported escape
// there, and an inverted byte range — each rejected as MRI's /n raises
// ArgumentError.
func TestMBClassRangeErrorASCII8BIT(t *testing.T) {
	for _, pat := range []string{
		`[a-\d]`,    // a class escape cannot end a range
		`[a-\p{L}]`, // nor can a property
		`[a-\q]`,    // an unsupported escape as a range end propagates the error
		`[z-a]`,     // inverted byte range
	} {
		if _, err := onigmo.CompileEnc(pat, onigmo.ASCII8BIT); err == nil {
			t.Errorf("CompileEnc(%q, ASCII8BIT): expected a range error", pat)
		}
	}
}
