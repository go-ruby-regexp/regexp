package onigmo_test

import (
	"testing"
	"time"

	onigmo "github.com/go-onigmo/regexp"
)

// atomicCap is one expected capturing group: its matched text and its begin
// offset (in bytes). A begin of -1 marks a group that did not participate.
type atomicCap struct {
	text  string
	begin int
}

// TestAtomicAndPossessive asserts the exact spans of atomic groups (?>…) and
// possessive quantifiers *+ ++ ?+ {m,n}+ with hardcoded expectations pinned
// against MRI / real Onigmo (Ruby 4.0.5), so the committed test does not depend
// on the host Ruby. Both forms rest on the same atomic-cut barrier: once the
// sub-pattern matches, every backtrack point it created is discarded, so the
// engine never re-tries a shorter repetition or an alternate sub-match to make
// the rest of the pattern succeed.
func TestAtomicAndPossessive(t *testing.T) {
	cases := []struct {
		pat   string
		in    string
		want  string // whole match; "\x00" means no match
		begin int    // whole-match begin offset (ignored when no match)
		caps  []atomicCap
	}{
		// Possessive quantifiers eat greedily and never give back.
		{`a++`, "aaa", "aaa", 0, nil},
		{`a++a`, "aaa", "\x00", 0, nil},  // no give-back: the trailing a cannot match
		{`a*+`, "aaa", "aaa", 0, nil},
		{`a*+a`, "aaa", "\x00", 0, nil},
		{`a*+b`, "b", "b", 0, nil},       // zero repetitions, then b
		{`a?+`, "a", "a", 0, nil},
		{`a?+a`, "aa", "aa", 0, nil},     // the possessive ? keeps its one a; trailing a takes the 2nd
		{`a?+a`, "a", "\x00", 0, nil},    // only one a: nothing left for the trailing a
		// A '+' after a {m,n} brace is, in Onigmo, a stacked greedy quantifier on the
		// braced repeat — NOT a possessive — so a{2,3}+ still gives back: it is
		// (a{2,3})+, which lets the trailing literal succeed.
		{`a{2,3}+`, "aaaa", "aaa", 0, nil},   // (a{2,3})+ with no follower: one iteration of 3
		{`a{2,3}+a`, "aaaa", "aaaa", 0, nil}, // gives back so the literal a matches the 4th
		{`a{2,3}+a`, "aaa", "aaa", 0, nil},   // braced repeat gives back to 2, literal takes the 3rd
		{`a{2,3}+a`, "aa", "\x00", 0, nil},   // only two a's: nothing left for the literal
		{`\d++\.\d`, "12.3", "12.3", 0, nil}, // possessive digits then a non-digit it does not eat
		{`[a-c]++d`, "abcd", "abcd", 0, nil}, // possessive class
		{`a++b`, "aaab", "aaab", 0, nil},     // possessive then a distinct literal

		// Atomic groups commit their sub-match.
		{`(?>a+)`, "aaa", "aaa", 0, nil},
		{`(?>a+)a`, "aaa", "\x00", 0, nil},  // committed: no give-back
		{`(?>a*)b`, "aaab", "aaab", 0, nil},
		{`(?>a*)b`, "b", "b", 0, nil},
		{`x(?>a*)`, "xaaa", "xaaa", 0, nil}, // atomic at end
		{`(?>a|ab)c`, "abc", "\x00", 0, nil},   // commits to the first alternative
		{`(?>a+)+b`, "aaab", "aaab", 0, nil},   // nested atomic under a greedy +
		{`(?>a*)*b`, "aaab", "aaab", 0, nil},   // atomic star under a greedy star
		{`(?>a+?)b`, "aaab", "ab", 2, nil},     // atomic over a lazy body: commits to the minimum
		{`(?>a??)a`, "aa", "a", 0, nil},        // atomic over a lazy optional: commits to zero
		{`(?>)a`, "a", "a", 0, nil},            // empty atomic group is a no-op

		// Captures inside an atomic group persist (most recent binding wins).
		{`(?>(a+))(b)`, "aaab", "aaab", 0, []atomicCap{{"aaa", 0}, {"b", 3}}},
		{`((?>a+))a`, "aaa", "\x00", 0, nil}, // atomic inside a capture: no give-back, no match
		{`(?>(a)|(ab))(c)`, "abc", "\x00", 0, nil},

		// Possessive over a capturing group: the last iteration's binding wins.
		{`(a)++`, "aaa", "aaa", 0, []atomicCap{{"a", 2}}},
		{`(a)*+`, "aaa", "aaa", 0, []atomicCap{{"a", 2}}},
		{`(ab)++`, "ababab", "ababab", 0, []atomicCap{{"ab", 4}}},
		{`(a|b)*+c`, "abc", "abc", 0, []atomicCap{{"b", 1}}},
		{`((a)|(b))++`, "ab", "ab", 0, []atomicCap{{"b", 1}, {"a", 0}, {"b", 1}}},
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
		if m.Str(0) != c.want || m.Begin(0) != c.begin {
			t.Errorf("/%s/ on %q: whole match = %q@%d, want %q@%d", c.pat, c.in, m.Str(0), m.Begin(0), c.want, c.begin)
		}
		for i, cap := range c.caps {
			g := i + 1
			if cap.begin < 0 {
				if m.Begin(g) >= 0 {
					t.Errorf("/%s/ on %q: group %d = %q@%d, want non-participating", c.pat, c.in, g, m.Str(g), m.Begin(g))
				}
				continue
			}
			if m.Str(g) != cap.text || m.Begin(g) != cap.begin {
				t.Errorf("/%s/ on %q: group %d = %q@%d, want %q@%d", c.pat, c.in, g, m.Str(g), m.Begin(g), cap.text, cap.begin)
			}
		}
	}
}

// TestAtomicInLookaround checks the atomic-cut mechanism inside lookaround
// sub-searches (which the VM runs on an isolated backtrack stack), so the
// OpAtomicBegin/OpAtomicEnd handling in execLook is exercised.
func TestAtomicInLookaround(t *testing.T) {
	cases := []struct {
		pat   string
		in    string
		want  string // "\x00" = no match
		begin int
	}{
		{`(?=(?>a+)b)a+b`, "aaab", "aaab", 0}, // atomic inside a positive lookahead that holds
		{`(?=(?>a+)c)a+b`, "aaab", "\x00", 0}, // atomic lookahead commits, then c fails: lookahead false
		{`(?!(?>a+)c)a+b`, "aaab", "aaab", 0}, // negative lookahead: the inner atomic match fails so the assertion holds
		{`(?>a|ab)`, "ab", "a", 0},            // bare atomic alternation commits to the first
		{`(?=(?>a*+))a`, "aa", "a", 0},        // possessive (lowered to atomic) inside a lookahead
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
		if m.Str(0) != c.want || m.Begin(0) != c.begin {
			t.Errorf("/%s/ on %q: whole match = %q@%d, want %q@%d", c.pat, c.in, m.Str(0), m.Begin(0), c.want, c.begin)
		}
	}
}

// TestAtomicParseErrors asserts the parse-time errors of malformed atomic groups
// and possessives: an unterminated (?>…), and an atomic group (or a possessive,
// which lowers to one) inside a lookbehind — which Onigmo/Ruby reject outright.
func TestAtomicParseErrors(t *testing.T) {
	for _, pat := range []string{
		`(?>a+`,          // missing closing )
		`(?>a|`,          // parse error inside the body propagates
		`(?<=(?>ab))c`,   // atomic group in a lookbehind: rejected
		`(?<=a*+)b`,      // possessive (lowered to atomic) in a lookbehind: rejected
	} {
		if _, err := onigmo.Compile(pat); err == nil {
			t.Errorf("/%s/: expected a syntax error, got nil", pat)
		}
	}
}

// TestPossessiveRejectsBadTarget keeps the "nothing to repeat" guard: a
// possessive modifier still requires a preceding atom, exactly as the bare
// quantifier does.
func TestPossessiveRejectsBadTarget(t *testing.T) {
	for _, pat := range []string{`*+`, `(?>*+)`} {
		_, err := onigmo.Compile(pat)
		if err == nil {
			t.Errorf("/%s/: expected a syntax error, got nil", pat)
		}
	}
}

// TestAtomicTerminates guards against a runaway loop when an atomic/possessive
// body can match empty under an outer unbounded quantifier: the empty-width loop
// guard must still stop it.
func TestAtomicTerminates(t *testing.T) {
	for _, pat := range []string{`(?>a*)*b`, `x(?>a*)+y`, `(?>a*?)*b`} {
		done := make(chan bool, 1)
		go func(p string) {
			re, err := onigmo.Compile(p)
			if err != nil {
				done <- false
				return
			}
			re.Match("xaaayb")
			done <- true
		}(pat)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("/%s/ did not terminate", pat)
		}
	}
}
