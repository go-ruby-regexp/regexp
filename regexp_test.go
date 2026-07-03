package onigmo

import (
	"errors"
	"testing"
	"time"

	"github.com/go-ruby-regexp/regexp/internal/syntax"
)

func TestCompileError(t *testing.T) {
	_, err := Compile("(")
	if !errors.Is(err, syntax.ErrSyntax) {
		t.Fatalf("expected ErrSyntax, got %v", err)
	}
}

func TestCompileSuccessAndString(t *testing.T) {
	re, err := Compile(`a(b)c`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if re.String() != `a(b)c` {
		t.Fatalf("String() = %q", re.String())
	}
}

func TestMatchAndAccessors(t *testing.T) {
	re, err := Compile(`(\d+)-(\d+)`)
	if err != nil {
		t.Fatal(err)
	}
	m := re.Match("see 2026-06 ok")
	if m == nil {
		t.Fatal("expected a match")
	}
	if m.NGroups() != 2 {
		t.Fatalf("NGroups = %d", m.NGroups())
	}
	if m.Str(0) != "2026-06" {
		t.Errorf("Str(0) = %q", m.Str(0))
	}
	if m.Str(1) != "2026" || m.Str(2) != "06" {
		t.Errorf("captures = %q, %q", m.Str(1), m.Str(2))
	}
	if m.Begin(0) != 4 || m.End(0) != 11 {
		t.Errorf("span = (%d,%d)", m.Begin(0), m.End(0))
	}
	if m.Pre() != "see " {
		t.Errorf("Pre() = %q", m.Pre())
	}
	if m.Post() != " ok" {
		t.Errorf("Post() = %q", m.Post())
	}
}

func TestMatchString(t *testing.T) {
	re := mustCompile(t, `\d+`)
	if !re.MatchString("ab12") {
		t.Error("expected MatchString true")
	}
	if re.MatchString("abc") {
		t.Error("expected MatchString false")
	}
}

func TestMatchAt(t *testing.T) {
	// \G anchors at the requested offset and the whole string stays visible, so
	// ^ matches only at a real line start and offsets are absolute.
	re := mustCompile(t, `\G(?:[a-z]+)`)
	s := "ab cd\nef"
	if m := re.MatchAt(s, 3); m == nil || m.Str(0) != "cd" || m.Begin(0) != 3 || m.End(0) != 5 {
		t.Fatalf("MatchAt(%q,3) = %#v, want cd at [3,5)", s, m)
	}
	// No match anchored at a position whose byte is not [a-z].
	if m := re.MatchAt(s, 2); m != nil {
		t.Errorf("MatchAt at a space should be nil, got %#v", m)
	}
	// ^ must match only at a genuine line start.
	caret := mustCompile(t, `\G(?:^[a-z]+)`)
	if caret.MatchAt(s, 6) == nil { // pos 6 is just after '\n'
		t.Error("^[a-z]+ should match at the line start (pos 6)")
	}
	if caret.MatchAt(s, 3) != nil { // pos 3 is mid-line
		t.Error("^[a-z]+ should not match mid-line (pos 3)")
	}
	// Out-of-range positions return nil.
	if re.MatchAt(s, -1) != nil || re.MatchAt(s, len(s)+1) != nil {
		t.Error("out-of-range pos should yield nil")
	}
	// A position exactly at len(s) is valid input to attempt (and fails here).
	if re.MatchAt("", 0) != nil {
		t.Error("empty input should not match [a-z]+")
	}
}

func TestNoMatch(t *testing.T) {
	re := mustCompile(t, `xyz`)
	if m := re.Match("abc"); m != nil {
		t.Fatalf("expected nil MatchData, got %#v", m)
	}
}

func TestAccessorsOutOfRange(t *testing.T) {
	re := mustCompile(t, `a`)
	m := re.Match("a")
	if m == nil {
		t.Fatal("expected match")
	}
	// Negative and too-large indices return the documented sentinels.
	if m.Begin(-1) != -1 || m.End(-1) != -1 {
		t.Error("negative index should yield -1")
	}
	if m.Begin(5) != -1 || m.End(5) != -1 {
		t.Error("out-of-range index should yield -1")
	}
	if m.Str(-1) != "" || m.Str(5) != "" {
		t.Error("out-of-range Str should be empty")
	}
}

func TestNonParticipatingGroup(t *testing.T) {
	// The second alternative's group never participates; its span is -1.
	re := mustCompile(t, `(a)|(b)`)
	m := re.Match("a")
	if m == nil {
		t.Fatal("expected match")
	}
	if m.Begin(2) != -1 || m.End(2) != -1 {
		t.Errorf("group 2 should not participate: (%d,%d)", m.Begin(2), m.End(2))
	}
	if m.Str(2) != "" {
		t.Errorf("non-participating Str = %q", m.Str(2))
	}
}

// TestTimeoutAPI exercises the public wall-clock timeout: WithTimeout sets the
// limit on a copy without mutating the receiver, Timeout reports it, a positive
// timeout bounds a catastrophic match (returning no match), and a non-positive
// timeout clears the limit.
func TestTimeoutAPI(t *testing.T) {
	re := mustCompile(t, `(a+)+\1b`) // catastrophic, backref disables memoization
	if re.Timeout() != 0 {
		t.Fatalf("fresh Regexp Timeout = %v, want 0", re.Timeout())
	}
	input := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// A 1ns timeout aborts the catastrophic search: no match (the deadline trips
	// before the engine can finish backtracking).
	timed := re.WithTimeout(time.Nanosecond)
	if timed.Timeout() != time.Nanosecond {
		t.Fatalf("WithTimeout Timeout = %v, want 1ns", timed.Timeout())
	}
	if m := timed.Match(input); m != nil {
		t.Fatalf("timed-out catastrophic match returned a result: %q", m.Str(0))
	}
	// The receiver is unchanged (immutability / concurrency safety).
	if re.Timeout() != 0 {
		t.Fatalf("WithTimeout mutated the receiver: Timeout = %v", re.Timeout())
	}

	// A non-positive timeout clears the limit; the copy then has no deadline.
	cleared := timed.WithTimeout(0)
	if cleared.Timeout() != 0 {
		t.Fatalf("WithTimeout(0) Timeout = %v, want 0", cleared.Timeout())
	}
	neg := timed.WithTimeout(-time.Second)
	if neg.Timeout() != 0 {
		t.Fatalf("WithTimeout(negative) Timeout = %v, want 0", neg.Timeout())
	}

	// With a generous timeout a well-behaved pattern still matches normally (the
	// deadline path is taken but never trips).
	ok := mustCompile(t, "abc").WithTimeout(time.Minute)
	if m := ok.Match("xxabcxx"); m == nil || m.Str(0) != "abc" {
		t.Fatalf("generous-timeout match failed: %v", m)
	}
}

// TestLazyBuildDeferred verifies the compile/first-match split introduced to
// keep Regexp.new fast: Compile only parses (validating syntax) and leaves the
// heavy matcher state (instruction program + DFA) unbuilt, and the first match
// builds it. A freshly-compiled Regexp therefore has a nil program until it is
// used.
func TestLazyBuildDeferred(t *testing.T) {
	re := mustCompile(t, `[a-z]+\d`)
	// Nothing heavy has been lowered yet: only the parse result is retained.
	if re.m.prog != nil {
		t.Fatal("Compile eagerly built the program; expected it deferred to first match")
	}
	// Encoding is answerable without forcing the build (it reads the stored enc).
	if re.Encoding() != UTF8 {
		t.Fatalf("Encoding() = %v, want UTF8", re.Encoding())
	}
	if re.m.prog != nil {
		t.Fatal("Encoding() forced the machine build; it must not")
	}
	// The first match lowers the program; a later match reuses the same instance.
	if re.Match("ab7") == nil {
		t.Fatal("expected a match")
	}
	built := re.m.prog
	if built == nil {
		t.Fatal("first match did not build the program")
	}
	re.MatchString("cd8")
	if re.m.prog != built {
		t.Fatal("second match rebuilt the program; build must happen exactly once")
	}
}

// TestCompileErrorIsEagerNotDeferred pins the MRI-fidelity contract that a
// malformed pattern is rejected at Compile time (like Ruby's Regexp.new raising
// RegexpError) and never silently deferred to a would-be first match. Deferring
// only the machine build must not defer the *syntax error*.
func TestCompileErrorIsEagerNotDeferred(t *testing.T) {
	for _, bad := range []string{"(", "a)", `\1`, `(?<n>)\g<x>`, `a{2,1}`} {
		re, err := Compile(bad)
		if err == nil {
			t.Fatalf("Compile(%q) returned no error; a malformed pattern must fail at compile time, not first match", bad)
		}
		if re != nil {
			t.Fatalf("Compile(%q) returned a non-nil Regexp alongside an error", bad)
		}
	}
}

// TestLazyBuildConcurrent hammers a single freshly-compiled Regexp with many
// goroutines that all trigger the deferred build simultaneously, across every
// entry point (Match, MatchString, MatchAt, Encoding) and both the DFA-subset
// and backtracking (backref) paths. Run under -race it proves the sync.Once
// guarding the lazy lowering is data-race-free — no goroutine ever observes a
// half-built program — and that concurrent matches on the shared instance agree.
func TestLazyBuildConcurrent(t *testing.T) {
	for _, pat := range []string{`[a-z]+\d`, `(a+)\1`} { // DFA-subset, then backref (VM path)
		re := mustCompile(t, pat)
		const g = 64
		start := make(chan struct{})
		done := make(chan bool, g)
		for i := 0; i < g; i++ {
			go func(i int) {
				<-start // release all goroutines at once to maximise build contention
				switch i % 4 {
				case 0:
					done <- re.Match("aaa7") != nil
				case 1:
					done <- re.MatchString("aaa7")
				case 2:
					done <- re.MatchAt("aaa7", 0) != nil
				default:
					done <- re.Encoding() == UTF8
				}
			}(i)
		}
		close(start)
		for i := 0; i < g; i++ {
			if !<-done {
				t.Fatalf("pattern %q: a concurrent match disagreed with the expected result", pat)
			}
		}
	}
}

// TestWithTimeoutSharesMachine verifies a WithTimeout copy shares the receiver's
// lazily-built matcher state rather than triggering a second build: copying the
// Regexp copies the *machine pointer, so a timeout variant and its origin resolve
// to the same program once either is matched.
func TestWithTimeoutSharesMachine(t *testing.T) {
	re := mustCompile(t, `[a-z]+`)
	timed := re.WithTimeout(time.Minute)
	if re.m != timed.m {
		t.Fatal("WithTimeout copy does not share the origin's machine")
	}
	timed.Match("abc") // builds through the copy
	if re.m.prog == nil {
		t.Fatal("build through the timeout copy did not populate the shared machine")
	}
}

func mustCompile(t *testing.T, p string) *Regexp {
	t.Helper()
	re, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	return re
}
