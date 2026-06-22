package onigmo_test

import (
	"testing"

	onigmo "github.com/go-ruby-regexp/regexp"
)

// TestRuneFold exercises rune-level case-insensitive matching (/i): a multi-byte
// letter folds to its Unicode case partner via simple (1:1) case folding, in both
// literals and character classes. Expectations are hardcoded (derived from MRI
// Onigmo, Ruby 4.0) so the test is oracle-independent.
func TestRuneFold(t *testing.T) {
	for _, tc := range []struct {
		pat, in, want string
		nomatch       bool
	}{
		// Single multi-byte literal folds either direction.
		{pat: `(?i)É`, in: "éxy", want: "é"},
		{pat: `(?i)é`, in: "É", want: "É"},
		{pat: `(?i)é`, in: "é", want: "é"},
		// A whole multi-byte word.
		{pat: `(?i)café`, in: "CAFÉ", want: "CAFÉ"},
		{pat: `(?i)naïve`, in: "NAÏVE", want: "NAÏVE"},
		// Greek, including the three-member sigma orbit Σ/σ/ς.
		{pat: `(?i)Σ`, in: "σ", want: "σ"},
		{pat: `(?i)σ`, in: "ς", want: "ς"},
		{pat: `(?i)ς`, in: "Σ", want: "Σ"},
		{pat: `(?i)Ωμέγα`, in: "ωμέγα", want: "ωμέγα"},
		// Cyrillic.
		{pat: `(?i)Б`, in: "б", want: "б"},
		// An ASCII letter still folds, and rune-aware folding lets it reach its
		// non-ASCII case partner (the Kelvin sign U+212A folds to "k").
		{pat: `(?i)k`, in: "K", want: "K"},
		{pat: `(?i)k`, in: "K", want: "K"},
		{pat: `(?i)s`, in: "ſ", want: "ſ"}, // LATIN SMALL LETTER LONG S
		// A non-letter under /i is unaffected and matches byte-exactly.
		{pat: `(?i)5`, in: "5", want: "5"},
		{pat: `(?i)é`, in: "e", nomatch: true},

		// A single multi-byte class member.
		{pat: `(?i)[é]`, in: "É", want: "É"},
		{pat: `(?i)[éx]+`, in: "ÉéXz", want: "ÉéX"},
		// Multi-byte ranges fold across case.
		{pat: `(?i)[α-ω]`, in: "Δ", want: "Δ"},
		{pat: `(?i)[Α-Ω]`, in: "δ", want: "δ"},
		{pat: `(?i)[Б-Я]+`, in: "бвгд", want: "бвгд"},
		{pat: `(?i)[é-ñ]`, in: "Ñ", want: "Ñ"},
		// A mixed ASCII range and a folded multi-byte member.
		{pat: `(?i)[a-zé]+`, in: "ABÉcd", want: "ABÉcd"},
		// An ASCII class reaches a non-ASCII case partner too.
		{pat: `(?i)[a-z]`, in: "K", want: "K"},
		// Negated folded class skips the case partner.
		{pat: `(?i)[^é]+`, in: "ÉÉxy", want: "xy"},
		{pat: `(?i)[^a-z]`, in: "É", want: "É"},
		// Out of range under folding still fails.
		{pat: `(?i)[α-ν]`, in: "Ω", nomatch: true},

		// A rune-aware folded atom refuses to start at a UTF-8 continuation byte:
		// scanning "aé" for /(?i)é/ matches the whole é, never its interior byte.
		{pat: `(?i)é`, in: "aé", want: "é"},
		// A capture of a folded literal reports the input (folded) text.
		{pat: `(?i)(é)x`, in: "Éx", want: "Éx"},
	} {
		re, err := onigmo.Compile(tc.pat)
		if err != nil {
			t.Errorf("Compile(%q): %v", tc.pat, err)
			continue
		}
		m := re.Match(tc.in)
		if tc.nomatch {
			if m != nil {
				t.Errorf("/%s/ on %q: expected no match, got %q", tc.pat, tc.in, m.Str(0))
			}
			continue
		}
		if m == nil {
			t.Errorf("/%s/ on %q: no match, want %q", tc.pat, tc.in, tc.want)
			continue
		}
		if got := m.Str(0); got != tc.want {
			t.Errorf("/%s/ on %q: got %q want %q", tc.pat, tc.in, got, tc.want)
		}
	}
}

// TestRuneFoldByteOffsets confirms the engine keeps BYTE offsets under rune-level
// folding (consistent with the rest of the engine), even though the matched text
// is a multi-byte code point.
func TestRuneFoldByteOffsets(t *testing.T) {
	re, err := onigmo.Compile(`(?i)é`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// "aé": 'a' is 1 byte, the folded 'É' input occupies bytes 1..3 (2 bytes).
	m := re.Match("aÉ")
	if m == nil {
		t.Fatal("expected a match")
	}
	if b, e := m.Begin(0), m.End(0); b != 1 || e != 3 {
		t.Errorf("byte span = [%d,%d), want [1,3)", b, e)
	}
}

// TestRuneFoldRangeError checks the parse errors of code-point ranges under /i:
// an inverted range (multi-byte or ASCII-low/multi-byte-high), and a class escape
// or property used as a range end, each matching MRI.
func TestRuneFoldRangeError(t *testing.T) {
	for _, pat := range []string{
		`(?i)[ñ-é]`,    // inverted multi-byte range
		`(?i)[ω-α]`,    // inverted Greek range
		`(?i)[à-x]`,    // inverted ASCII-low / multi-byte-high range
		`(?i)[z-a]`,    // inverted all-ASCII range under /i (now a code-point range)
		`(?i)[é-\d]`,    // a class escape cannot end a range
		`(?i)[é-\p{L}]`, // nor can a property
		`(?i)[a-\p{L}]`, // ditto with an ASCII low bound under /i
		`(?i)[é-\q]`, // an unsupported escape as a range end propagates the error
	} {
		if _, err := onigmo.Compile(pat); err == nil {
			t.Errorf("Compile(%q): expected a range error", pat)
		}
	}
}

// TestRuneFoldAsciiLowMultibyteHigh covers a range whose low bound is ASCII and
// whose high bound is a multi-byte code point (e.g. (?i)[a-é]); it is a code-point
// range, so a code point between the two bounds matches in either case.
func TestRuneFoldAsciiLowMultibyteHigh(t *testing.T) {
	re, err := onigmo.Compile(`(?i)[a-é]`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, in := range []string{"c", "ç", "Ç"} {
		if m := re.Match(in); m == nil || m.Str(0) != in {
			t.Errorf(`(?i)[a-é] on %q: got %v`, in, m)
		}
	}
	if m := re.Match("ÿ"); m != nil { // ÿ (U+00FF) is past é (U+00E9)
		t.Errorf(`(?i)[a-é] on "ÿ": expected no match, got %q`, m.Str(0))
	}
}

// TestRuneFoldInvalidUTF8 confirms an invalid UTF-8 lead byte under /i is treated
// as a single opaque byte (matched byte-exactly), keeping the engine's
// byte-oriented core intact for non-UTF-8 input.
func TestRuneFoldInvalidUTF8(t *testing.T) {
	pat := "(?i)" + string([]byte{0xff}) // 0xFF is never a valid UTF-8 lead byte
	re, err := onigmo.Compile(pat)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if re.Match(string([]byte{0xff})) == nil {
		t.Error("expected the lone 0xFF byte to match itself")
	}
	if re.Match("a") != nil {
		t.Error("0xFF literal should not match an ASCII byte")
	}
}
