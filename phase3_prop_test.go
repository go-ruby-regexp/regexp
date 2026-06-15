package onigmo_test

import (
	"strings"
	"testing"

	onigmo "github.com/go-onigmo/regexp"
)

// TestUnicodePropMatch checks the standalone \p{…} atom against hardcoded
// (oracle-independent) expectations on UTF-8 input. The whole-match substring is
// compared, which is independent of whether offsets are byte- or
// character-based.
func TestUnicodePropMatch(t *testing.T) {
	for _, tc := range []struct {
		pat, in, want string
	}{
		// General categories — one-letter groups.
		{`\p{L}+`, "héllo123", "héllo"},
		{`\p{N}+`, "café42!", "42"},
		{`\p{P}+`, "a!?,b", "!?,"},
		{`\p{S}+`, "a+=<b", "+=<"},
		{`\p{Z}+`, "a  b", "  "},
		{`\p{C}+`, "a\t\nb", "\t\n"},
		// Letter subcategories.
		{`\p{Lu}+`, "héWXz", "WX"},
		{`\p{Ll}+`, "HÉllo", "llo"},
		{`\p{Lo}+`, "ab中文cd", "中文"},
		{`\p{Lt}`, "aǅb", "ǅ"},
		{`\p{Lm}`, "aʰb", "ʰ"},
		{`\p{Nd}+`, "x42y", "42"},
		// Onigmo POSIX-style aliases.
		{`\p{Alpha}+`, "héllo123", "héllo"},
		{`\p{Alnum}+`, "héllo123!", "héllo123"},
		{`\p{Digit}+`, "a12b", "12"},
		{`\p{Space}+`, "a \t\vb", " \t\v"},
		{`\p{Upper}+`, "abCDef", "CD"},
		{`\p{Lower}+`, "ABcdEF", "cd"},
		{`\p{Word}+`, "naïve_42 x", "naïve_42"},
		// Negation: \P{…} and \p{^…} both complement.
		{`\P{L}+`, "abc123!", "123!"},
		{`\p{^L}+`, "abc123!", "123!"},
		{`\P{Alpha}+`, "héllo123", "123"},
		// \P{^…} double-negates back to the positive class.
		{`\P{^Nd}+`, "a42b", "42"},
		// In a quantified scan the rune-aware atom skips multi-byte chars
		// correctly so a negated property does not match a code point's interior
		// byte.
		{`\P{Nd}+`, "é2", "é"},
	} {
		re, err := onigmo.Compile(tc.pat)
		if err != nil {
			t.Errorf("Compile(%q): %v", tc.pat, err)
			continue
		}
		m := re.Match(tc.in)
		if m == nil {
			t.Errorf("/%s/ on %q: no match, want %q", tc.pat, tc.in, tc.want)
			continue
		}
		if got := m.Str(0); got != tc.want {
			t.Errorf("/%s/ on %q: got %q want %q", tc.pat, tc.in, got, tc.want)
		}
	}
}

// TestUnicodePropNoMatch covers properties that should not match anywhere in the
// input.
func TestUnicodePropNoMatch(t *testing.T) {
	for _, tc := range []struct{ pat, in string }{
		{`\p{Lu}`, "abc"},
		{`\p{Nd}`, "abc"},
		{`\p{L}`, "123 !"},
		{`\P{L}`, "héllo"},
	} {
		re := mustCompile(t, tc.pat)
		if m := re.Match(tc.in); m != nil {
			t.Errorf("/%s/ on %q: unexpected match %q", tc.pat, tc.in, m.Str(0))
		}
	}
}

// TestUnicodePropInClass checks \p{…} as a member of a character class, which
// makes the whole class rune-aware while keeping its ordinary byte-range members
// working.
func TestUnicodePropInClass(t *testing.T) {
	for _, tc := range []struct{ pat, in, want string }{
		{`[\p{L}\d]+`, "héllo3!", "héllo3"},
		{`[\p{Lu}x]+`, "xXAby", "xXA"},
		{`[^\p{L}]+`, "héllo123x", "123"},
		{`[\p{Nd}a-c]+`, "ab9dz", "ab9"},
		// Folding still applies to the ASCII range members of a rune-aware class:
		// (?i)[\p{Nd}m-p] accepts the uppercase counterpart of the range.
		{`(?i)[\p{Nd}m-p]+`, "5Nq", "5N"},
	} {
		re := mustCompile(t, tc.pat)
		m := re.Match(tc.in)
		if m == nil {
			t.Errorf("/%s/ on %q: no match, want %q", tc.pat, tc.in, tc.want)
			continue
		}
		if got := m.Str(0); got != tc.want {
			t.Errorf("/%s/ on %q: got %q want %q", tc.pat, tc.in, got, tc.want)
		}
	}
}

// TestUnicodePropCaptures checks property atoms inside capturing groups so the
// rune-aware advance threads capture spans correctly.
func TestUnicodePropCaptures(t *testing.T) {
	re := mustCompile(t, `(\p{Lu})(\p{Ll}+)`)
	m := re.Match("Héllo")
	if m == nil {
		t.Fatal(`(\p{Lu})(\p{Ll}+) on "Héllo": no match`)
	}
	if g1, g2 := m.Str(1), m.Str(2); g1 != "H" || g2 != "éllo" {
		t.Fatalf("captures: g1=%q g2=%q want H/éllo", g1, g2)
	}
}

// TestUnicodePropInvalidUTF8 checks that a rune-aware atom does not match a lone
// continuation byte (an interior byte of a code point with no leading byte),
// mirroring MRI which only ever positions at character boundaries.
func TestUnicodePropInvalidUTF8(t *testing.T) {
	// A lone 0xA9 (a UTF-8 continuation byte) is not a valid standalone rune.
	if m := mustCompile(t, `\p{L}`).Match("\xa9"); m != nil {
		t.Fatalf(`\p{L} on lone continuation byte: unexpected match %q`, m.Str(0))
	}
	// A negated property must also reject the interior byte, not match it.
	if m := mustCompile(t, `\P{L}`).Match("\xa9"); m != nil {
		t.Fatalf(`\P{L} on lone continuation byte: unexpected match %q`, m.Str(0))
	}
}

// TestUnicodePropInLookaround exercises the rune-aware step inside a lookahead
// sub-VM (the property atom is compiled and run identically there).
func TestUnicodePropInLookaround(t *testing.T) {
	re := mustCompile(t, `\p{L}+(?=\p{Nd})`)
	m := re.Match("héllo9")
	if m == nil || m.Str(0) != "héllo" {
		t.Fatalf(`\p{L}+(?=\p{Nd}) on "héllo9": %+v`, m)
	}
	// A negative class-in-lookahead path: letters not followed by another letter.
	re2 := mustCompile(t, `\p{L}(?![\p{L}])`)
	if m := re2.Match("ab9"); m == nil || m.Str(0) != "b" {
		t.Fatalf(`\p{L}(?![\p{L}]) on "ab9": %+v`, m)
	}
}

// TestUnicodePropParseErrors covers the parser's rejection branches for the
// \p{…} construct.
func TestUnicodePropParseErrors(t *testing.T) {
	for _, pat := range []string{
		`\p`,        // missing brace (EOF after \p)
		`\pL`,       // one-letter form is not accepted (no brace)
		`\p{L`,      // unterminated braces
		`\p{}`,      // empty name
		`\p{Bogus}`, // unknown property name
		`\P{Nope}`,  // unknown name via \P
		`[\p{Bad}]`, // unknown name inside a class
		`[\p`,       // missing brace inside a class
	} {
		if _, err := onigmo.Compile(pat); err == nil {
			t.Errorf("Compile(%q): expected a syntax error", pat)
		}
	}
}

// TestUnicodePropRangeEndRejected checks that a \p{…} member cannot be the end
// of a character-class range (e.g. [a-\p{L}] is invalid), matching Onigmo.
func TestUnicodePropRangeEndRejected(t *testing.T) {
	if _, err := onigmo.Compile(`[a-\p{L}]`); err == nil {
		t.Fatal(`Compile([a-\p{L}]): expected a syntax error for a property range end`)
	}
}

// TestUnicodePropLookbehindRejected checks that a variable-byte-width rune-aware
// atom is rejected inside a (fixed-width) lookbehind, both as a standalone atom
// and as a class member.
func TestUnicodePropLookbehindRejected(t *testing.T) {
	for _, pat := range []string{
		`(?<=\p{L})x`,
		`(?<=[\p{L}])x`,
	} {
		_, err := onigmo.Compile(pat)
		if err == nil || !strings.Contains(err.Error(), "lookbehind") {
			t.Errorf("Compile(%q): expected a variable-width-lookbehind error, got %v", pat, err)
		}
	}
}

// TestUnicodePropDigitVsN distinguishes the Nd-based aliases from the broader N
// category: a superscript digit is N (and Number) but not Nd/Digit.
func TestUnicodePropDigitVsN(t *testing.T) {
	// U+00B2 SUPERSCRIPT TWO is category No: in \p{N} but not \p{Nd}/\p{Digit}.
	if m := mustCompile(t, `\p{N}`).Match("²"); m == nil || m.Str(0) != "²" {
		t.Fatalf(`\p{N} on "²": %+v`, m)
	}
	if m := mustCompile(t, `\p{Nd}`).Match("²"); m != nil {
		t.Fatalf(`\p{Nd} on "²": unexpected match`)
	}
	if m := mustCompile(t, `\p{Digit}`).Match("²"); m != nil {
		t.Fatalf(`\p{Digit} on "²": unexpected match`)
	}
}
