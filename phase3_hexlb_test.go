package onigmo_test

import (
	"testing"

	onigmo "github.com/go-ruby-regexp/regexp"
)

// TestHexDigitClass pins the \h hex-digit class and its complement \H, both as a
// standalone escape and as a character-class member, with exact spans hardcoded
// against MRI / real Onigmo (Ruby 4.0.5) so the committed test is independent of
// the host Ruby. \h is exactly [0-9A-Fa-f]; \H is its byte-complement.
func TestHexDigitClass(t *testing.T) {
	cases := []struct {
		pat   string
		in    string
		want  string // whole match; "\x00" means no match
		begin int
	}{
		{`\h`, "g9z", "9", 1},        // first hex digit
		{`\h+`, "9aFg", "9aF", 0},    // every hex digit class member
		{`\h+`, "0123456789abcdefABCDEFg", "0123456789abcdefABCDEF", 0}, // the full set
		{`\h`, "ghz", "\x00", 0},     // no hex digit present
		{`\H`, "9z", "z", 1},         // first non-hex
		{`\H+`, "  9ab", "  ", 0},    // run of non-hex
		{`\H`, "9a", "\x00", 0},      // all hex: no match
		{`[\h]+x`, "9aFx", "9aFx", 0},   // \h inside a class
		{`[\H]+9`, "zz9", "zz9", 0},      // \H inside a class
		{`[\h\s]+`, "9a \tF", "9a \tF", 0}, // \h unioned with \s
		{`[^\h]+`, "9zz9", "zz", 1},     // negated class containing \h
		{`0x\h+`, "0xC0FFEEz", "0xC0FFEE", 0}, // realistic hex literal
		{`(?<=\h)x`, "9x", "x", 1},      // \h is one byte wide, so it is valid in a lookbehind
	}
	for _, c := range cases {
		re, err := onigmo.Compile(c.pat)
		if err != nil {
			t.Errorf("compile /%s/: %v", c.pat, err)
			continue
		}
		m := re.Match(c.in)
		if c.want == "\x00" {
			if m != nil {
				t.Errorf("/%s/ on %q: matched %q, want no match", c.pat, c.in, m.Str(0))
			}
			continue
		}
		if m == nil {
			t.Errorf("/%s/ on %q: no match, want %q", c.pat, c.in, c.want)
			continue
		}
		if m.Str(0) != c.want || m.Begin(0) != c.begin {
			t.Errorf("/%s/ on %q: %q@%d, want %q@%d", c.pat, c.in, m.Str(0), m.Begin(0), c.want, c.begin)
		}
	}
}

// TestLinebreak pins the \R linebreak escape with exact byte spans hardcoded
// against MRI (Ruby 4.0.5). \R is lowered to (?>\r\n|[\n\v\f\r\x85  ]):
// it matches a CR-LF pair atomically (so /\R\n/ never splits a CRLF) or any one
// linebreak code point, including the multi-byte NEL (U+0085, bytes C2 85), LS
// (U+2028, E2 80 A8) and PS (U+2029, E2 80 A9). Spans are byte offsets, which is
// why the multi-byte cases pin pre-match byte lengths explicitly.
func TestLinebreak(t *testing.T) {
	cases := []struct {
		pat   string
		in    string
		want  string // "\x00" = no match
		begin int    // byte offset
	}{
		// Single ASCII linebreaks.
		{`\R`, "\n", "\n", 0},
		{`\R`, "\r", "\r", 0},
		{`\R`, "\v", "\v", 0},
		{`\R`, "\f", "\f", 0},
		{`\R`, "x", "\x00", 0}, // a non-linebreak does not match
		{`\R`, " ", "\x00", 0}, // a plain space is not a linebreak

		// The CR-LF pair is matched as one indivisible unit.
		{`\R`, "\r\n", "\r\n", 0},
		{`a\Rb`, "a\r\nb", "a\r\nb", 0}, // CRLF between literals
		{`\Rx`, "\r\nx", "\r\nx", 0},    // \R eats the CRLF, then x matches
		{`\R\n`, "\r\n", "\x00", 0},     // atomic: the CRLF is never split for a trailing \n
		{`\R\r`, "\n\r", "\n\r", 0},     // but two separate breaks are two matches

		// A run of linebreaks; each CRLF stays atomic.
		{`\R+`, "\r\n\n\r", "\r\n\n\r", 0},
		{`\R+`, "abc", "\x00", 0},

		// Multi-byte Unicode linebreaks (byte offsets).
		{`\R`, "", "", 0},          // NEL alone
		{`x\R`, "x", "x", 0},       // NEL after a 1-byte literal (whole match incl. x)
		{`\R`, " ", " ", 0},          // LS alone
		{`\R`, " ", " ", 0},          // PS alone
		{`a\Rb`, "a b", "a b", 0},    // LS between literals (begin 0)
		{`\R+`, "\r\n ", "\r\n ", 0}, // mixed run
	}
	for _, c := range cases {
		re, err := onigmo.Compile(c.pat)
		if err != nil {
			t.Errorf("compile /%s/: %v", c.pat, err)
			continue
		}
		m := re.Match(c.in)
		if c.want == "\x00" {
			if m != nil {
				t.Errorf("/%s/ on %q: matched %q, want no match", c.pat, c.in, m.Str(0))
			}
			continue
		}
		if m == nil {
			t.Errorf("/%s/ on %q: no match, want %q", c.pat, c.in, c.want)
			continue
		}
		if m.Str(0) != c.want || m.Begin(0) != c.begin {
			t.Errorf("/%s/ on %q: %q@%d, want %q@%d", c.pat, c.in, m.Str(0), m.Begin(0), c.want, c.begin)
		}
	}
}

// TestLinebreakRejectedInLookbehind asserts that \R — being variable-width and
// rune-aware — is rejected inside a fixed-width lookbehind, exactly as Onigmo
// rejects "invalid pattern in look-behind". (\h, one byte wide, is accepted; that
// is covered positively in TestHexDigitClass.)
func TestLinebreakRejectedInLookbehind(t *testing.T) {
	for _, pat := range []string{`(?<=\R)a`, `(?<!\R)a`} {
		if _, err := onigmo.Compile(pat); err == nil {
			t.Errorf("/%s/: expected a variable-width-lookbehind error, got nil", pat)
		}
	}
}
