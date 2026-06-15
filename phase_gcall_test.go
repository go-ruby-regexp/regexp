package onigmo_test

import (
	"strings"
	"testing"
	"time"

	onigmo "github.com/go-onigmo/regexp"
)

// gCase is one oracle-independent expectation for a \g<…> subexpression call.
// want is the whole-match substring; caps are the per-group captured substrings
// (a non-participating group is the empty string). ok is whether a match is
// expected. These encode the exact Onigmo/Ruby behaviour pinned by probing MRI
// (Ruby 4.0.5, real Onigmo) and so do not depend on the host Ruby at test time.
type gCase struct {
	pat  string
	in   string
	want string
	caps []string
	ok   bool
}

func TestGCallMatching(t *testing.T) {
	cases := []gCase{
		// Absolute numbered call; the call re-runs and re-captures the group, so
		// the most recent execution wins (\g<1> overwrites group 1 to "34").
		{`(\d+)-\g<1>`, "12-34", "12-34", []string{"34"}, true},
		{`(\d+)-\g<1>`, "12-345", "12-345", []string{"345"}, true},
		// A call that re-runs an adjacent group.
		{`(\d)\g<1>`, "12", "12", []string{"2"}, true},
		{`(\d)\g<1>`, "1", "", nil, false},
		{`(\w)\g<1>`, "ab", "ab", []string{"b"}, true},
		// The call re-runs an alternation independently.
		{`(a|b)\g<1>`, "ab", "ab", []string{"b"}, true},
		// Named call, including a forward reference resolved post-parse.
		{`(?<two>\d)\g<two>`, "34", "34", []string{"4"}, true},
		{`\g<two>(?<two>\d+)`, "123", "123", []string{"3"}, true},
		// Relative references: +n counts groups after the token, -n before it.
		{`\g<+1>(\d)`, "55", "55", []string{"5"}, true},
		{`\g<+1>(\d)`, "5", "", nil, false},
		{`(\d)\g<-1>`, "12", "12", []string{"2"}, true},
		{`(a)(b)\g<-2>\g<-1>`, "abab", "abab", []string{"a", "b"}, true},
		// Call one of several groups; the others keep their textual capture.
		{`(x)(\d)\g<2>`, "x12", "x12", []string{"x", "2"}, true},
		// A backreference sees the call's re-capture, not the original.
		{`(\d)\g<1>\1`, "122", "122", []string{"2"}, true},
		{`(\d)\g<1>\1`, "121", "", nil, false},
		// A quantified call.
		{`(?<x>\d)\g<x>+`, "1234", "1234", []string{"4"}, true},
		// A call inside a lookahead (zero-width: its captures still escape).
		{`(?=(\d)\g<1>)\d+`, "12x", "12", []string{"2"}, true},
		{`foo(?=\g<1>)(bar)`, "foobar", "foobar", []string{"bar"}, true},
		// \g<0> whole-pattern recursion.
		{`\((?:[^()]|\g<0>)*\)`, "((x))", "((x))", nil, true},
		// Unanchored, so the inner "()" substring matches even in "(()".
		{`\((?:[^()]|\g<0>)*\)`, "(()", "()", nil, true},
		// Balanced-parentheses grammar via named self-recursion. The recursive call
		// restores the outer binding on return, so group 1 is the *outermost* span.
		{`(?<bal>\((?:[^()]|\g<bal>)*\))`, "(a(b)c)", "(a(b)c)", []string{"(a(b)c)"}, true},
		{`\A(?<bal>\((?:[^()]|\g<bal>)*\))\z`, "((()))", "((()))", []string{"((()))"}, true},
		{`\A(?<bal>\((?:[^()]|\g<bal>)*\))\z`, "(()", "", nil, false},
		{`\A(?<bal>\((?:[^()]|\g<bal>)*\))\z`, "((((((((((x))))))))))", "((((((((((x))))))))))", []string{"((((((((((x))))))))))"}, true},
		// Self-recursion keeps the outermost binding (Onigmo capture semantics).
		{`(a\g<1>?)`, "aa", "aa", []string{"aa"}, true},
		// A sub-capture inside a recursive group keeps the *deepest* binding, since
		// it is not active at the recursive call sites.
		{`\A(?<b>\((?<inner>[^()]*)(?:\g<b>)?[^()]*\))\z`, "(a(b)c)", "(a(b)c)", []string{"(a(b)c)", "b"}, true},
		// A call inside a lookahead body to a recursive grammar.
		{`(?=(?<bal>\((?:[^()]|\g<bal>)*\)))\(`, "((x))", "(", []string{"((x))"}, true},
		// A recursive grammar with a nested sub-capture inside a lookahead body:
		// exercises a nested group's OpReturn falling through during the call.
		{`(?=(?<b>\((?<in>[^()]*)(?:\g<b>)?[^()]*\)))x?`, "(a(b)c)", "", []string{"(a(b)c)", "b"}, true},
		// Mutual recursion between two named groups; each keeps its own binding.
		{`\A(?<a>x(?:\g<b>)?)(?<b>y(?:\g<a>)?)\z`, "xyx", "xyx", []string{"x", "yx"}, true},
		// A recursive arithmetic-expression grammar.
		{`\A(?<term>(?<num>\d+)|\((?<expr>\g<term>(?:\+\g<term>)*)\))\z`, "(1+2+3)", "(1+2+3)", []string{"(1+2+3)", "3", "1+2+3"}, true},
		// The quote-delimited spelling \g'name' is equivalent to \g<name>.
		{`(?<two>\d)\g'two'`, "34", "34", []string{"4"}, true},
		{`(\d)\g'1'`, "12", "12", []string{"2"}, true},
		{`\g'+1'(\d)`, "55", "55", []string{"5"}, true},
	}
	for _, c := range cases {
		re, err := onigmo.Compile(c.pat)
		if err != nil {
			t.Errorf("compile /%s/: %v", c.pat, err)
			continue
		}
		m := re.Match(c.in)
		if !c.ok {
			if m != nil {
				t.Errorf("/%s/ on %q: matched %q, want no match", c.pat, c.in, m.Str(0))
			}
			continue
		}
		if m == nil {
			t.Errorf("/%s/ on %q: no match, want %q", c.pat, c.in, c.want)
			continue
		}
		if m.Str(0) != c.want {
			t.Errorf("/%s/ on %q: whole match = %q, want %q", c.pat, c.in, m.Str(0), c.want)
		}
		for i, cap := range c.caps {
			if got := m.Str(i + 1); got != cap {
				t.Errorf("/%s/ on %q: group %d = %q, want %q", c.pat, c.in, i+1, got, cap)
			}
		}
	}
}

func TestGCallParseErrors(t *testing.T) {
	for _, pat := range []string{
		`\g`,          // no delimiter
		`\g<`,         // unterminated
		`\g'`,         // unterminated quote form
		`\g<>`,        // empty name
		`\g<nope>`,    // undefined name
		`\g<5>(\d)`,   // out-of-range number (only one group)
		`\g<01>`,      // leading-zero number
		`(\d)\g<+0>`,  // degenerate relative +0
		`(\d)\g<-0>`,  // degenerate relative -0
		`\g< 1>`,      // invalid name character (space)
		`\g<a-b>`,     // invalid name character (hyphen)
		`\g<+x>`,      // non-numeric relative reference
		`\g<+>`,       // relative sign with no number
		`\g<->`,       // relative sign with no number
	} {
		if _, err := onigmo.Compile(pat); err == nil {
			t.Errorf("/%s/: expected a compile error, got none", pat)
		}
	}
}

func TestGCallValidNumberAndRelative(t *testing.T) {
	// \g<0> is always valid (whole-pattern recursion) even with no groups.
	if _, err := onigmo.Compile(`a\g<0>?`); err != nil {
		t.Errorf(`/a\g<0>?/: unexpected error: %v`, err)
	}
	// A relative reference that resolves out of range is rejected (no group 1).
	if _, err := onigmo.Compile(`\g<+1>`); err == nil {
		t.Error(`/\g<+1>/ with no group: expected error`)
	}
	if _, err := onigmo.Compile(`\g<-1>`); err == nil {
		t.Error(`/\g<-1>/ with no preceding group: expected error`)
	}
}

func TestGCallLookbehindRejected(t *testing.T) {
	// A subexpression call has data/recursion-dependent width, so like a
	// backreference it is rejected inside a fixed-width lookbehind. (MRI accepts
	// the simple one-char case; this is a documented boundary divergence.)
	if _, err := onigmo.Compile(`(?<=(\w)\g<1>)x`); err == nil {
		t.Error(`lookbehind containing \g should be rejected`)
	} else if !strings.Contains(err.Error(), "lookbehind") {
		t.Errorf("expected a lookbehind error, got %v", err)
	}
}

func TestGCallRecursionTerminates(t *testing.T) {
	// Unbounded recursion (\g<0> with no consuming progress, which Onigmo rejects
	// statically) must terminate via the depth/step budget rather than hang or
	// overflow the Go stack, and must report no match.
	for _, pat := range []string{`\A\g<0>\z`, `(?<r>\g<r>)`, `\g<0>`, `(?=(?<r>\g<r>))x`} {
		done := make(chan *onigmo.MatchData, 1)
		go func(p string) {
			re, err := onigmo.Compile(p)
			if err != nil {
				done <- nil
				return
			}
			done <- re.Match("xxxx")
		}(pat)
		select {
		case m := <-done:
			if m != nil {
				t.Errorf("/%s/: unexpected match on recursion limit", pat)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("/%s/: recursion did not terminate", pat)
		}
	}
}
