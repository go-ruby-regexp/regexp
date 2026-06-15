package onigmo

import (
	"errors"
	"testing"
	"time"

	"github.com/go-onigmo/regexp/internal/syntax"
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

func mustCompile(t *testing.T, p string) *Regexp {
	t.Helper()
	re, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	return re
}
