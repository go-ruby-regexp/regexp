package onigmo_test

import (
	"testing"

	onigmo "github.com/go-onigmo/regexp"
)

// TestInlineFoldFlag exercises ASCII case-insensitive matching via the inline
// (?i) / (?i:...) / (?-i) options. Expectations are hardcoded (derived from MRI
// Onigmo, Ruby 4.0) so the test stays oracle-independent.
func TestInlineFoldFlag(t *testing.T) {
	for _, tc := range []struct {
		pat, in, want string // want is the whole match, or "" for no match
		nomatch       bool
	}{
		// Set directive: folds the rest of the enclosing group.
		{pat: `(?i)abc`, in: "xxABCyy", want: "ABC"},
		{pat: `(?i)ABC`, in: "abc", want: "abc"},
		{pat: `(?i)aBc`, in: "AbC", want: "AbC"},
		// Scoped group only folds its body.
		{pat: `(?i:abc)d`, in: "ABCd", want: "ABCd"},
		{pat: `(?i:abc)d`, in: "ABCD", nomatch: true},
		// A directive after a consuming atom is local to its branch.
		{pat: `a(?i)bc`, in: "ABC", nomatch: true},
		{pat: `a(?i)bc`, in: "aBC", want: "aBC"},
		// Turning folding off again.
		{pat: `(?i)a(?-i)b`, in: "Ab", want: "Ab"},
		{pat: `(?i)a(?-i)b`, in: "AB", nomatch: true},
		{pat: `(?i-i:a)`, in: "A", nomatch: true},
		// Scope does not leak out of a group.
		{pat: `(a(?i)b)c`, in: "abC", nomatch: true},
		{pat: `(a(?i)b)c`, in: "aBc", want: "aBc"},
		// Branch propagation: a leading (?i) prefix carries to later branches.
		{pat: `(?i)a|b`, in: "B", want: "B"},
		{pat: `a(?i)|b`, in: "B", nomatch: true},
		{pat: `x(?i)y|z`, in: "Z", nomatch: true},
		{pat: `a|(?i)b|c`, in: "C", want: "C"},
		// Classes fold, including negation.
		{pat: `(?i)[a-z]+`, in: "ABCdef", want: "ABCdef"},
		{pat: `(?i)[A-Z]+`, in: "abcXYZ", want: "abcXYZ"},
		{pat: `(?i)[^a-z]+`, in: "ABC123", want: "123"},
		{pat: `(?i)[m-p]`, in: "M", want: "M"},
		{pat: `(?i)[m-p]`, in: "Q", nomatch: true},
		// Non-letters are unaffected by folding.
		{pat: `(?i)5`, in: "5", want: "5"},
		// Backreferences fold when /i is in effect at the backref.
		{pat: `(?i)(ab)\1`, in: "AbAB", want: "AbAB"},
		{pat: `(ab)(?i)\1`, in: "abAB", want: "abAB"},
		{pat: `(ab)\1`, in: "abAB", nomatch: true},
		// Named backreference folds too.
		{pat: `(?i)(?<g>ab)\k<g>`, in: "ABab", want: "ABab"},
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

// TestInlineFlagRejectsUnsupported confirms that flags other than i are
// reported as syntax errors (until later phases add m/x/etc.).
func TestInlineFlagRejectsUnsupported(t *testing.T) {
	for _, pat := range []string{`(?m)a`, `(?x:a)`, `(?z)a`} {
		if _, err := onigmo.Compile(pat); err == nil {
			t.Errorf("Compile(%q): expected an error for an unsupported flag", pat)
		}
	}
}
