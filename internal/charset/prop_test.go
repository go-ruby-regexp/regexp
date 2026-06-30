package charset

import "testing"

// TestValid checks the recognised-name predicate for every supported name and a
// representative rejected one.
func TestValid(t *testing.T) {
	valid := []string{
		"L", "N", "P", "S", "Z", "C",
		"Lu", "Ll", "Lt", "Lm", "Lo", "Nd", "Nl", "No",
		"Alpha", "Alnum", "Digit", "Space", "Upper", "Lower", "Word",
	}
	for _, name := range valid {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "X", "Pc", "alpha", "WORD", "Bogus"} {
		if Valid(name) {
			t.Errorf("Valid(%q) = true, want false", name)
		}
	}
}

// TestMatch exercises every property predicate, including the alias edge cases
// that differ from a single general category, and both negation and the
// unknown-name defensive fallback.
func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		negate bool
		r      rune
		want   bool
	}{
		// One-letter groups.
		{"L", false, 'é', true},
		{"L", false, '1', false},
		{"N", false, '4', true},
		{"N", false, '²', true}, // No is still N
		{"P", false, '!', true},
		{"P", false, 'a', false},
		{"S", false, '+', true},
		{"S", false, 'a', false},
		{"Z", false, ' ', true},
		{"Z", false, '\v', false}, // vertical tab is C, not Z
		{"C", false, '\t', true},
		{"C", false, 'a', false},
		// Letter/number subcategories.
		{"Lu", false, 'H', true},
		{"Lu", false, 'h', false},
		{"Ll", false, 'h', true},
		{"Ll", false, 'H', false},
		{"Lt", false, 'ǅ', true},
		{"Lt", false, 'A', false},
		{"Lm", false, 'ʰ', true},
		{"Lm", false, 'a', false},
		{"Lo", false, '中', true},
		{"Lo", false, 'a', false},
		{"Nd", false, '4', true},
		{"Nd", false, '²', false}, // superscript two is No, not Nd
		{"Nl", false, 'Ⅻ', true},  // roman numeral twelve is Nl
		{"Nl", false, '4', false},
		{"No", false, '²', true}, // superscript two is No
		{"No", false, '4', false},
		// Aliases.
		{"Alpha", false, 'é', true},
		{"Alpha", false, '4', false},
		{"Alpha", false, 0x0345, true}, // Other_Alphabetic combining mark
		{"Alpha", false, 'Ⅻ', true},    // Nl roman numeral is Alphabetic
		{"Alnum", false, '4', true},
		{"Alnum", false, 'a', true},
		{"Alnum", false, '!', false},
		{"Digit", false, '7', true},
		{"Digit", false, 'a', false},
		{"Space", false, '\v', true}, // White_Space includes vertical tab
		{"Space", false, 'a', false},
		{"Upper", false, 'H', true},
		{"Upper", false, 'h', false},
		{"Lower", false, 'h', true},
		{"Lower", false, 'H', false},
		{"Word", false, '_', true},    // connector punctuation
		{"Word", false, 0x0301, true}, // combining acute (a Mark)
		{"Word", false, '4', true},
		{"Word", false, ' ', false},
		// Negation flips the result.
		{"L", true, 'é', false},
		{"L", true, '1', true},
		// Unknown name: the defensive fallback returns false regardless of negate.
		{"Bogus", false, 'a', false},
		{"Bogus", true, 'a', false},
	} {
		if got := Match(tc.name, tc.negate, tc.r); got != tc.want {
			t.Errorf("Match(%q, %v, %q) = %v, want %v", tc.name, tc.negate, tc.r, got, tc.want)
		}
	}
}

// TestFoldEqual checks simple (1:1) case-folding orbit membership, including the
// non-trivial orbits ASCII letters share with their Unicode case partners (the
// Kelvin sign for "k", LATIN SMALL LETTER LONG S for "s") and the three-member
// Greek sigma orbit.
func TestFoldEqual(t *testing.T) {
	for _, tc := range []struct {
		a, b rune
		want bool
	}{
		{'a', 'a', true},      // identity
		{'A', 'a', true},      // ASCII case
		{'a', 'A', true},      // ASCII case, other direction
		{'É', 'é', true},      // Latin-1 accented pair
		{'k', 0x212A, true},   // "k" ↔ KELVIN SIGN
		{'s', 0x017F, true},   // "s" ↔ LATIN SMALL LETTER LONG S
		{'Σ', 'ς', true},      // Greek sigma orbit member
		{'ς', 'σ', true},      // and another member of the same orbit
		{'a', 'b', false},     // different letters never fold-match
		{'a', '5', false},     // letter vs digit
		{'é', 'e', false},     // accent is significant (simple folding)
	} {
		if got := FoldEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("FoldEqual(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestFoldRangeContains checks folded code-point range membership in both
// directions and for a non-ASCII case partner reaching an ASCII range.
func TestFoldRangeContains(t *testing.T) {
	for _, tc := range []struct {
		r, lo, hi rune
		want      bool
	}{
		{'A', 'a', 'z', true},    // upper-case input matches a lower-case range
		{'m', 'a', 'z', true},    // direct membership
		{0x212A, 'a', 'z', true}, // KELVIN SIGN folds into [a-z]
		{'Δ', 'α', 'ω', true},    // upper Greek into a lower Greek range
		{'5', 'a', 'z', false},   // a digit is in no letter range
		{'é', 'a', 'z', false},   // a non-ASCII letter is outside the ASCII range
	} {
		if got := FoldRangeContains(tc.r, tc.lo, tc.hi); got != tc.want {
			t.Errorf("FoldRangeContains(%q,%q,%q) = %v, want %v", tc.r, tc.lo, tc.hi, got, tc.want)
		}
	}
}
