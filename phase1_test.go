package onigmo_test

import (
	"testing"

	onigmo "github.com/go-onigmo/regexp"
)

func mustCompile(t *testing.T, pattern string) *onigmo.Regexp {
	t.Helper()
	re, err := onigmo.Compile(pattern)
	if err != nil {
		t.Fatalf("Compile(%q): %v", pattern, err)
	}
	return re
}

func TestBackreference(t *testing.T) {
	re := mustCompile(t, `(\w+) \1`)
	m := re.Match("ab ab cd")
	if m == nil || m.Str(0) != "ab ab" || m.Str(1) != "ab" {
		t.Fatalf("doubled-word backref: %+v", m)
	}
	// Backref that cannot be satisfied forces backtracking to no match.
	if re.MatchString("ab cd") {
		t.Fatal(`"ab cd" should not match (\w+) \1`)
	}
}

func TestBackrefUnsetGroupMatchesEmpty(t *testing.T) {
	// (z)? does not participate, so \1 is empty and the whole thing matches "b".
	re := mustCompile(t, `(z)?\1b`)
	m := re.Match("b")
	if m == nil || m.Str(0) != "b" {
		t.Fatalf("unset backref should match empty: %+v", m)
	}
}

func TestNamedBackreference(t *testing.T) {
	re := mustCompile(t, `(?<c>.)\k<c>`)
	if !re.MatchString("aa") {
		t.Fatal(`"aa" should match (?<c>.)\k<c>`)
	}
	if re.MatchString("ab") {
		t.Fatal(`"ab" should not match (?<c>.)\k<c>`)
	}
}

func TestNamedGroups(t *testing.T) {
	re := mustCompile(t, `(?<y>\d{4})-(?<m>\d{2})`)
	m := re.Match("on 2026-06 ok")
	if m == nil {
		t.Fatal("expected a match")
	}
	if m.StrName("y") != "2026" || m.StrName("m") != "06" {
		t.Fatalf("named captures: y=%q m=%q", m.StrName("y"), m.StrName("m"))
	}
	if m.IndexOfName("y") != 1 || m.IndexOfName("m") != 2 {
		t.Fatalf("name indices: y=%d m=%d", m.IndexOfName("y"), m.IndexOfName("m"))
	}
	if m.IndexOfName("nope") != -1 {
		t.Fatal("unknown name should be -1")
	}
	if m.StrName("nope") != "" {
		t.Fatal("unknown name should yield empty string")
	}
}
