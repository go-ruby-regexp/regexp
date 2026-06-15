package charset

import "testing"

// TestValid checks the recognised-name predicate for every supported name and a
// representative rejected one.
func TestValid(t *testing.T) {
	valid := []string{
		"L", "N", "P", "S", "Z", "C",
		"Lu", "Ll", "Lt", "Lm", "Lo", "Nd",
		"Alpha", "Alnum", "Digit", "Space", "Upper", "Lower", "Word",
	}
	for _, name := range valid {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "X", "Nl", "Pc", "alpha", "WORD", "Bogus"} {
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
