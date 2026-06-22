package onigmo_test

import (
	"testing"
	"time"

	onigmo "github.com/go-ruby-regexp/regexp"
)

// TestLazyQuantifiers asserts the exact spans of non-greedy quantifiers with
// hardcoded expectations (pinned against MRI / real Onigmo, Ruby 4.0.5), so the
// committed test does not depend on the host Ruby. A lazy quantifier prefers the
// fewest repetitions and only takes more when the rest of the pattern forces it.
func TestLazyQuantifiers(t *testing.T) {
	cases := []struct {
		pat  string
		in   string
		want string // whole match; "\x00" means no match
		caps []string
	}{
		{`a*?`, "aaa", "", nil},                       // zero is enough
		{`a+?`, "aaa", "a", nil},                      // one is enough
		{`a??`, "a", "", nil},                         // zero preferred
		{`a??b`, "ab", "ab", nil},                     // forced to take the a
		{`<.+?>`, "<a><b>", "<a>", nil},               // shortest tag
		{`<.*?>`, "<><b>", "<>", nil},                 // empty contents
		{`a{2,4}?`, "aaaa", "aa", nil},                // the minimum 2
		{`a{2,}?`, "aaaa", "aa", nil},                 // the minimum 2
		{`a{0,3}?b`, "aaab", "aaab", nil},             // forced up to 3
		{`".*?"`, `say "hi" and "bye"`, `"hi"`, nil},  // first quoted run
		{`(\w+?)(\w+)`, "abcd", "abcd", []string{"a", "bcd"}}, // lazy yields then greedy takes
		{`(a|b)*?c`, "abc", "abc", []string{"b"}},     // forced through both
		{`x(.*?)x`, "xaxbx", "xax", []string{"a"}},    // shortest middle
		{`(?:ab)+?`, "ababab", "ab", nil},             // one copy
		{`(a?)*?b`, "aaab", "aaab", []string{"a"}},    // empty-capable lazy loop
		{`(a*)*?b`, "aaab", "aaab", []string{"aaa"}},  // nested lazy/greedy
		{`(a*)*?b`, "aaac", "\x00", nil},              // no match
		{`\d+?`, "12345", "1", nil},                   // single digit
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

// TestLazyEmptyLoopTerminates checks that a non-greedy loop over an
// empty-matchable body terminates (the zero-width-loop guard works regardless of
// branch order) and reports the correct result.
func TestLazyEmptyLoopTerminates(t *testing.T) {
	for _, c := range []struct {
		pat, in string
		match   bool
	}{
		{`(?:)*?x`, "y", false},
		{`(.*?)*x`, "aaax", true},
		{`(a??)*b`, "aaab", true},
	} {
		done := make(chan bool, 1)
		go func(p, s string) {
			re, err := onigmo.Compile(p)
			if err != nil {
				done <- false
				return
			}
			done <- re.Match(s) != nil
		}(c.pat, c.in)
		select {
		case got := <-done:
			if got != c.match {
				t.Errorf("/%s/ on %q: match=%v want %v", c.pat, c.in, got, c.match)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("/%s/ on %q did not terminate", c.pat, c.in)
		}
	}
}
