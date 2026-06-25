package onigmo_test

import "testing"

// The single-quote spelling of a named group, (?'name'...), is exactly
// equivalent to the angle-bracket form (?<name>...): same capture-by-name
// semantics, same name→index map, and the two spellings interoperate (a group
// defined one way is referenceable the other way). These tests mirror MRI:
//
//	/(?'y'\d+)/.match("ab12")[:y]            #=> "12"
//	/(?'y'\d)\k'y'/.match("ab55")            #=> matches
//	/(?'y'\d)\k<y>/.match("ab55")            #=> matches (mixed spelling)
//	/(?<y>\d)\k'y'/.match("ab55")            #=> matches (mixed spelling)

// TestQuoteNamedGroup covers the (?'name'...) capture form and the named-capture
// API exposed for it.
func TestQuoteNamedGroup(t *testing.T) {
	re := mustCompile(t, `(?'y'\d+)`)
	m := re.Match("ab12")
	if m == nil {
		t.Fatal("expected a match")
	}
	if m.StrName("y") != "12" {
		t.Fatalf("StrName(y) = %q, want %q", m.StrName("y"), "12")
	}
	if m.IndexOfName("y") != 1 {
		t.Fatalf("IndexOfName(y) = %d, want 1", m.IndexOfName("y"))
	}
}

// TestQuoteNamedGroupMultiple confirms several quoted names coexist and resolve
// to the right indices, exactly as the angle-bracket form does.
func TestQuoteNamedGroupMultiple(t *testing.T) {
	re := mustCompile(t, `(?'y'\d{4})-(?'m'\d{2})`)
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
}

// TestQuoteNamedBackref covers the \k'name' backreference spelling.
func TestQuoteNamedBackref(t *testing.T) {
	re := mustCompile(t, `(?'c'.)\k'c'`)
	if !re.MatchString("aa") {
		t.Fatal(`"aa" should match (?'c'.)\k'c'`)
	}
	if re.MatchString("ab") {
		t.Fatal(`"ab" should not match (?'c'.)\k'c'`)
	}
}

// TestNamedGroupSpellingsInterop confirms the two spellings are equivalent: a
// group defined with one delimiter is referenceable with the other, in both
// directions, just like MRI.
func TestNamedGroupSpellingsInterop(t *testing.T) {
	cases := []struct {
		pat  string
		good string
		bad  string
	}{
		{`(?'c'.)\k<c>`, "aa", "ab"}, // quote definition, angle backref
		{`(?<c>.)\k'c'`, "aa", "ab"}, // angle definition, quote backref
	}
	for _, c := range cases {
		re := mustCompile(t, c.pat)
		if !re.MatchString(c.good) {
			t.Errorf("%q should match /%s/", c.good, c.pat)
		}
		if re.MatchString(c.bad) {
			t.Errorf("%q should not match /%s/", c.bad, c.pat)
		}
	}
}

// TestQuoteNameInGCall confirms the quoted name resolves through a subexpression
// call \g'name' too (the \g quote delimiter was already supported; this pins the
// definition + call both using the quote spelling end-to-end).
func TestQuoteNameInGCall(t *testing.T) {
	re := mustCompile(t, `(?'p'\d)\g'p'`)
	if !re.MatchString("12") {
		t.Fatal(`"12" should match (?'p'\d)\g'p'`)
	}
	if re.MatchString("1x") {
		t.Fatal(`"1x" should not match (?'p'\d)\g'p'`)
	}
}
