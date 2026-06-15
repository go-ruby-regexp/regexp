package onigmo_test

import (
	"testing"

	onigmo "github.com/go-onigmo/regexp"
)

// TestInlineDotAll exercises the m option (Ruby's /m): the dot matches a newline
// as well. Expectations are hardcoded (oracle-independent) but were checked
// against MRI Onigmo (Ruby 4.0.5).
func TestInlineDotAll(t *testing.T) {
	cases := []struct {
		pat  string
		in   string
		want string // whole-match text; "" means no match
		ok   bool
	}{
		{`a.b`, "a\nb", "", false},        // plain dot does not cross a newline
		{`a.b`, "axb", "axb", true},       // ... but matches a non-newline
		{`(?m)a.b`, "a\nb", "a\nb", true}, // (?m) set directive: dot crosses '\n'
		{`(?m:a.b)`, "a\nb", "a\nb", true},
		{`(?m)a.b`, "axb", "axb", true},        // dot-all still matches non-newline
		{`(?m).+`, "x\ny", "x\ny", true},       // greedy dot-all spans the newline
		{`.+`, "x\ny", "x", true},              // greedy plain dot stops at '\n'
		{`(?m)(?-m:a.b)`, "a\nb", "", false},   // (?-m) turns dot-all back off
		{`(?m)(?-m:a.b)`, "axb", "axb", true},  //
		{`(?m:a.)c`, "a\nc", "a\nc", true},     // scope ends at ')': '.' here is plain
		{`(?m:a.)c`, "a\n\nc", "", false},      // ... so the literal c must follow
		{`a(?m).b`, "ax\nb", "", false},        // 'a' before (?m); first '.' is plain? no:
		{`a(?m).`, "a\n", "a\n", true},         // set directive scopes to rest of group
		{`(?m)a.b|c.d`, "c\nd", "c\nd", true},  // leading (?m) propagates to later branch
		{`a(?m)|c.d`, "c\nd", "", false},       // (?m) after an atom does not propagate
	}
	for _, tc := range cases {
		re, err := onigmo.Compile(tc.pat)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.pat, err)
		}
		m := re.Match(tc.in)
		if (m != nil) != tc.ok {
			t.Errorf("/%s/ on %q: match=%v want %v", tc.pat, tc.in, m != nil, tc.ok)
			continue
		}
		if m != nil && m.Str(0) != tc.want {
			t.Errorf("/%s/ on %q: got %q want %q", tc.pat, tc.in, m.Str(0), tc.want)
		}
	}
}

// TestInlineExtended exercises the x option (extended / free-spacing): unescaped
// whitespace and '#' comments in the pattern are ignored, except inside a
// character class. Expectations checked against MRI Onigmo (Ruby 4.0.5).
func TestInlineExtended(t *testing.T) {
	cases := []struct {
		pat string
		in  string
		ok  bool
	}{
		{"(?x) a b c", "abc", true},        // spaces between atoms are ignored
		{"(?x)a\tb", "ab", true},           // a tab is ignored too
		{"(?x)a\fb", "ab", true},           // form feed is ignored
		{"(?x)a\rb", "ab", true},           // carriage return is ignored
		{"(?x)a\nb", "ab", true},           // newline is ignored
		{"(?x)a # a comment\nb", "ab", true}, // a # comment runs to end of line
		{"(?x)a#trailing comment", "a", true}, // ... or to the end of the pattern
		{"(?x)a *", "aaa", true},           // whitespace before a quantifier is ignored
		{"(?x)a *", "", true},              // (a* matches empty)
		{"(?x)a | b", "b", true},           // whitespace around | is ignored
		{"(?x)a\\ b", "a b", true},         // an escaped space is a literal space
		{"(?x)a\\ b", "ab", false},         //
		{"(?x)[ a]", " ", true},            // inside a class, space is a literal member
		{"(?x)[ a]", "a", true},            //
		{"(?x)[ a]", "b", false},           //
		{"(?x)[#]", "#", true},             // inside a class, # is a literal member
		{"a (?x)b c", "a bc", true},        // (?x) scopes to the rest of the group only
		{"(?x:a b)c", "abc", true},         // scoped extended group
		{"(?x:a b)c", "ab c", false},       // the trailing c is outside the x scope
		{"(?ix) A B ", "ab", true},         // combined i and x
		{"(?mx) a . b ", "a\nb", true},     // combined m and x: dot-all plus free-spacing
		{"(?x)a(?-x: b)c", "a bc", true},   // (?-x:) turns extended mode back off
		{"(?x)a(?-x: b)c", "abc", false},   // ... so the space is now significant
	}
	for _, tc := range cases {
		re, err := onigmo.Compile(tc.pat)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.pat, err)
		}
		if got := re.MatchString(tc.in); got != tc.ok {
			t.Errorf("/%s/ on %q: match=%v want %v", tc.pat, tc.in, got, tc.ok)
		}
	}
}
