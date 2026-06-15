package onigmo_test

import (
	"testing"

	onigmo "github.com/go-onigmo/regexp"
)

func TestPosixClassMatch(t *testing.T) {
	for _, tc := range []struct {
		pat, in, want string
	}{
		{`[[:alpha:]]+`, "ab12cd", "ab"},
		{`[[:digit:]]+`, "ab12cd", "12"},
		{`[[:alnum:]]+`, "  a1b2  ", "a1b2"},
		{`[[:upper:]]+`, "abCDef", "CD"},
		{`[[:lower:]]+`, "ABcdEF", "cd"},
		{`[[:space:]]+`, "x \t\ny", " \t\n"},
		{`[[:blank:]]+`, "x \ty", " \t"},
		{`[[:xdigit:]]+`, "ghFF00zz", "FF00"},
		{`[[:punct:]]+`, "a!@#b", "!@#"},
		{`[[:word:]]+`, "  foo_bar  ", "foo_bar"},
		{`[[:graph:]]+`, "  ab!  ", "ab!"},
		{`[[:print:]]+`, "\tab \n", "ab "},
		// Combined with ordinary members and other POSIX classes.
		{`[x[:digit:]]+`, "x1y2", "x1"},
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

func TestPosixClassNegated(t *testing.T) {
	// [[:^digit:]] is the complement of the digits, so it matches the letters.
	re := mustCompile(t, `[[:^digit:]]+`)
	if m := re.Match("12ab34"); m == nil || m.Str(0) != "ab" {
		t.Fatalf(`[[:^digit:]]+ on "12ab34": %+v`, m)
	}
}

func TestPosixClassInNegatedOuterClass(t *testing.T) {
	// A POSIX class inside a negated bracket expression: [^[:digit:]] matches
	// any non-digit byte.
	re := mustCompile(t, `[^[:digit:]]+`)
	if m := re.Match("a1b2"); m == nil || m.Str(0) != "a" {
		t.Fatalf(`[^[:digit:]]+ on "a1b2": %+v`, m)
	}
}

func TestPosixClassUnknownRejected(t *testing.T) {
	if _, err := onigmo.Compile(`[[:bogus:]]`); err == nil {
		t.Fatal("Compile([[:bogus:]]) should reject an unknown POSIX class")
	}
}
