package onigmo

import (
	"errors"
	"strings"
	"testing"
)

// TestWithStackLimit: a stack limit is reported, as ErrStackLimit (which is also
// ErrBudget), by every …Err method and never mistaken for a failed match; the
// default and a non-positive limit stay unlimited; the receiver is not mutated.
func TestWithStackLimit(t *testing.T) {
	const pat = `\G(?:'(\\.|.)*?')`
	src := "'" + strings.Repeat(`\\ \' `, (256<<10)/6)
	re := mustCompile(t, pat).WithStackLimit(1 << 20)

	if md, err := re.MatchAtErr(src, 0); !errors.Is(err, ErrStackLimit) || !errors.Is(err, ErrBudget) || md != nil {
		t.Errorf("MatchAtErr = (%v, %v), want (nil, ErrStackLimit)", md, err)
	}
	if _, _, ok, err := re.MatchBoundsAtErr(src, 0); !errors.Is(err, ErrStackLimit) || ok {
		t.Errorf("MatchBoundsAtErr = (%v, %v), want (false, ErrStackLimit)", ok, err)
	}
	if ok, err := re.MatchStringErr(src); !errors.Is(err, ErrStackLimit) || ok {
		t.Errorf("MatchStringErr = (%v, %v), want (false, ErrStackLimit)", ok, err)
	}
	if md, err := re.MatchErr(src); !errors.Is(err, ErrStackLimit) || md != nil {
		t.Errorf("MatchErr = (%v, %v), want (nil, ErrStackLimit)", md, err)
	}
	if _, _, ok, err := re.MatchBoundsErr(src); !errors.Is(err, ErrStackLimit) || ok {
		t.Errorf("MatchBoundsErr = (%v, %v), want (false, ErrStackLimit)", ok, err)
	}
	// The plain forms fold the refusal into "no match", like ErrTimeout.
	if md := re.MatchAt(src, 0); md != nil {
		t.Errorf("MatchAt = %v, want nil", md)
	}
	// A refusal is not a failed match: a genuine non-match has err == nil.
	if md, err := re.MatchAtErr("abc", 0); md != nil || err != nil {
		t.Errorf("non-match = (%v, %v), want (nil, nil)", md, err)
	}
	if !errors.Is(ErrStackLimit, ErrBudget) || errors.Is(ErrBudget, ErrStackLimit) {
		t.Error("ErrStackLimit must wrap ErrBudget and ErrBudget must not be ErrStackLimit")
	}

	// Unlimited: the default, zero and negative, and the receiver is untouched.
	base := mustCompile(t, pat)
	small := "'" + strings.Repeat(`\\ \' `, (16<<10)/6)
	for _, r := range []*Regexp{base, base.WithStackLimit(0), base.WithStackLimit(-1)} {
		if md, err := r.MatchAtErr(small, 0); md == nil || err != nil {
			t.Errorf("unlimited match = (%v, %v)", md, err)
		}
	}
	if re.String() != pat || re.Timeout() != 0 {
		t.Errorf("WithStackLimit changed String/Timeout: %q %v", re.String(), re.Timeout())
	}
}
