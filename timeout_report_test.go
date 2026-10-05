package onigmo

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// catastrophic is a backreference pattern — outside the engine's lazy-NFA subset,
// so every match runs on the backtracking VM, the only path that can reach a
// limit. Against a run of "a" with no trailing "b" there is no match to find and
// exponentially many ways of not finding one.
const catastrophic = `(a+)+\1b`

func subject(n int) string { return strings.Repeat("a", n) }

// TestTimeoutIsReportedNotFoldedIntoNoMatch is the fail-open regression test: a
// match abandoned at the wall-clock limit must be distinguishable from a subject
// that genuinely does not match.
func TestTimeoutIsReportedNotFoldedIntoNoMatch(t *testing.T) {
	re, err := Compile(catastrophic)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	re = re.WithTimeout(20 * time.Millisecond)
	s := subject(26)

	if md, err := re.MatchErr(s); !errors.Is(err, ErrTimeout) || md != nil {
		t.Errorf("MatchErr = (%v, %v), want (nil, ErrTimeout)", md, err)
	}
	if md, err := re.MatchAtErr(s, 0); !errors.Is(err, ErrTimeout) || md != nil {
		t.Errorf("MatchAtErr = (%v, %v), want (nil, ErrTimeout)", md, err)
	}
	if ok, err := re.MatchStringErr(s); !errors.Is(err, ErrTimeout) || ok {
		t.Errorf("MatchStringErr = (%v, %v), want (false, ErrTimeout)", ok, err)
	}
	if _, _, ok, err := re.MatchBoundsErr(s); !errors.Is(err, ErrTimeout) || ok {
		t.Errorf("MatchBoundsErr = (%v, %v), want (false, ErrTimeout)", ok, err)
	}
	if _, _, ok, err := re.MatchBoundsAtErr(s, 0); !errors.Is(err, ErrTimeout) || ok {
		t.Errorf("MatchBoundsAtErr = (%v, %v), want (false, ErrTimeout)", ok, err)
	}

	// The non-Err twins still fold both reasons into "no match" — the behaviour a
	// guard must not rely on, recorded here as a decision rather than an accident.
	if re.Match(s) != nil {
		t.Error("Match = non-nil, want nil")
	}
	if re.MatchAt(s, 0) != nil {
		t.Error("MatchAt = non-nil, want nil")
	}
	if re.MatchString(s) {
		t.Error("MatchString = true, want false")
	}
	if _, _, ok := re.MatchBounds(s); ok {
		t.Error("MatchBounds ok = true, want false")
	}
	if _, _, ok := re.MatchBoundsAt(s, 0); ok {
		t.Error("MatchBoundsAt ok = true, want false")
	}
}

// TestBudgetIsReportedWithoutAnyTimeout is the one that matters most: the step
// budget is reached with NO timeout configured at all, so the fail-open does not
// need Regexp.timeout to be set to be exploitable. "a"*26 under the engine's
// default budget is abandoned in well under a second.
func TestBudgetIsReportedWithoutAnyTimeout(t *testing.T) {
	re, err := Compile(catastrophic)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if re.Timeout() != 0 {
		t.Fatalf("Timeout() = %v, want 0 — this test must run with no limit set", re.Timeout())
	}
	s := subject(26)

	if md, err := re.MatchErr(s); !errors.Is(err, ErrBudget) || md != nil {
		t.Errorf("MatchErr = (%v, %v), want (nil, ErrBudget)", md, err)
	}
	if ok, err := re.MatchStringErr(s); !errors.Is(err, ErrBudget) || ok {
		t.Errorf("MatchStringErr = (%v, %v), want (false, ErrBudget)", ok, err)
	}
	if md, err := re.MatchAtErr(s, 0); !errors.Is(err, ErrBudget) || md != nil {
		t.Errorf("MatchAtErr = (%v, %v), want (nil, ErrBudget)", md, err)
	}
	if _, _, ok, err := re.MatchBoundsErr(s); !errors.Is(err, ErrBudget) || ok {
		t.Errorf("MatchBoundsErr = (%v, %v), want (false, ErrBudget)", ok, err)
	}
	if _, _, ok, err := re.MatchBoundsAtErr(s, 0); !errors.Is(err, ErrBudget) || ok {
		t.Errorf("MatchBoundsAtErr = (%v, %v), want (false, ErrBudget)", ok, err)
	}
}

// TestNoLimitMeansNoError is the negative control: without a limit reached, every
// …Err form reports a nil error, for a real match and a real non-match alike. So
// a non-nil error always means "abandoned", never "did not match".
func TestNoLimitMeansNoError(t *testing.T) {
	re, err := Compile(`(a)(b)`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	md, err := re.MatchErr("xabz")
	if err != nil || md == nil || md.Str(0) != "ab" {
		t.Fatalf("MatchErr = (%v, %v), want the match \"ab\" with a nil error", md, err)
	}
	md, err = re.MatchAtErr("xabz", 1)
	if err != nil || md == nil || md.Begin(0) != 1 {
		t.Fatalf("MatchAtErr = (%v, %v), want a match at 1 with a nil error", md, err)
	}
	if ok, err := re.MatchStringErr("xabz"); !ok || err != nil {
		t.Errorf("MatchStringErr = (%v, %v), want (true, nil)", ok, err)
	}
	if b, e, ok, err := re.MatchBoundsErr("xabz"); !ok || err != nil || b != 1 || e != 3 {
		t.Errorf("MatchBoundsErr = (%d, %d, %v, %v), want (1, 3, true, nil)", b, e, ok, err)
	}
	if b, e, ok, err := re.MatchBoundsAtErr("xabz", 1); !ok || err != nil || b != 1 || e != 3 {
		t.Errorf("MatchBoundsAtErr = (%d, %d, %v, %v), want (1, 3, true, nil)", b, e, ok, err)
	}

	// A genuine non-match: nil/false WITH a nil error.
	if md, err := re.MatchErr("zzz"); md != nil || err != nil {
		t.Errorf("MatchErr(no match) = (%v, %v), want (nil, nil)", md, err)
	}
	if md, err := re.MatchAtErr("xabz", 0); md != nil || err != nil {
		t.Errorf("MatchAtErr(no match) = (%v, %v), want (nil, nil)", md, err)
	}
	if ok, err := re.MatchStringErr("zzz"); ok || err != nil {
		t.Errorf("MatchStringErr(no match) = (%v, %v), want (false, nil)", ok, err)
	}
	if _, _, ok, err := re.MatchBoundsErr("zzz"); ok || err != nil {
		t.Errorf("MatchBoundsErr(no match) = (%v, %v), want (false, nil)", ok, err)
	}
	if _, _, ok, err := re.MatchBoundsAtErr("zzz", 0); ok || err != nil {
		t.Errorf("MatchBoundsAtErr(no match) = (%v, %v), want (false, nil)", ok, err)
	}

	// An out-of-range anchor is a non-match, not an error.
	if md, err := re.MatchAtErr("xabz", 99); md != nil || err != nil {
		t.Errorf("MatchAtErr(pos out of range) = (%v, %v), want (nil, nil)", md, err)
	}
	if _, _, ok, err := re.MatchBoundsAtErr("xabz", -1); ok || err != nil {
		t.Errorf("MatchBoundsAtErr(pos out of range) = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestLimitSentinelsAreDistinguishable pins the contract errors.Is rests on.
func TestLimitSentinelsAreDistinguishable(t *testing.T) {
	if ErrTimeout == nil || ErrBudget == nil {
		t.Fatal("exported limit sentinels must be non-nil")
	}
	if errors.Is(ErrTimeout, ErrBudget) || errors.Is(ErrBudget, ErrTimeout) {
		t.Error("ErrTimeout and ErrBudget must be distinguishable")
	}
}
