package onigmo

import "testing"

// TestMatchBoundsAtParity checks that MatchBoundsAt returns exactly the group-0
// span MatchAt reports (or the same no-match), across the DFA fast path
// (capture-free and capturing patterns the DFA accepts) and the backtracking-VM
// fallback (a backreference pattern the DFA rejects), at every position.
func TestMatchBoundsAtParity(t *testing.T) {
	cases := []struct{ pat, in string }{
		{`[A-Za-z0-9_]+`, "foo123 + bar456"}, // capture-free -> DFA
		{`(\d)(\d)`, "ab12cd34"},             // capturing -> DFA (bounds only)
		{`(a)\1`, "xaaxbb"},                  // backref -> VM fallback
		{`\s+`, "  a b  c"},
		{`α+`, "ααβγ"}, // multibyte
		{`\Afoo`, "foobar"},
		{`^x`, "a\nxb"},
	}
	for _, c := range cases {
		re := mustCompile(t, c.pat)
		for pos := 0; pos <= len(c.in); pos++ {
			md := re.MatchAt(c.in, pos)
			b, e, ok := re.MatchBoundsAt(c.in, pos)
			if md == nil {
				if ok {
					t.Fatalf("pat=%q in=%q pos=%d: MatchAt=nil but MatchBoundsAt=(%d,%d,true)", c.pat, c.in, pos, b, e)
				}
				continue
			}
			if !ok || b != md.Begin(0) || e != md.End(0) {
				t.Fatalf("pat=%q in=%q pos=%d: MatchAt=(%d,%d) MatchBoundsAt=(%d,%d,%v)",
					c.pat, c.in, pos, md.Begin(0), md.End(0), b, e, ok)
			}
		}
	}
}

// TestMatchBoundsParity checks MatchBounds against Match's group-0 span across the
// DFA path and the VM fallback.
func TestMatchBoundsParity(t *testing.T) {
	cases := []struct{ pat, in string }{
		{`[0-9]+`, "abc123def"},  // DFA, matches
		{`[0-9]+`, "no digits!"}, // DFA, no match
		{`(a)\1`, "zzaazz"},      // backref -> VM, matches
		{`(a)\1`, "zzazz"},       // backref -> VM, no match
		{`(?<=x)y`, "axybz xy"},  // lookbehind -> VM
	}
	for _, c := range cases {
		re := mustCompile(t, c.pat)
		md := re.Match(c.in)
		b, e, ok := re.MatchBounds(c.in)
		if md == nil {
			if ok {
				t.Fatalf("pat=%q in=%q: Match=nil but MatchBounds=(%d,%d,true)", c.pat, c.in, b, e)
			}
			continue
		}
		if !ok || b != md.Begin(0) || e != md.End(0) {
			t.Fatalf("pat=%q in=%q: Match=(%d,%d) MatchBounds=(%d,%d,%v)",
				c.pat, c.in, md.Begin(0), md.End(0), b, e, ok)
		}
	}
}

// TestMatchBoundsAtOutOfRange checks the pos-bounds guard.
func TestMatchBoundsAtOutOfRange(t *testing.T) {
	re := mustCompile(t, `\w+`)
	if _, _, ok := re.MatchBoundsAt("abc", -1); ok {
		t.Fatal("MatchBoundsAt with pos=-1 should be ok=false")
	}
	if _, _, ok := re.MatchBoundsAt("abc", 4); ok {
		t.Fatal("MatchBoundsAt with pos>len should be ok=false")
	}
	// pos == len is in range: an empty-matching pattern may still match there.
	if _, _, ok := re.MatchBoundsAt("abc", 3); ok {
		t.Fatal(`\w+ at end of input should not match`)
	}
}

// TestMatchAtDFANoMatch exercises the capture-free DFA MatchAt path's no-match
// return (the branch a matching test alone leaves uncovered).
func TestMatchAtDFANoMatch(t *testing.T) {
	re := mustCompile(t, `[0-9]+`) // capture-free -> DFA path in MatchAt
	if md := re.MatchAt("abc123", 0); md != nil {
		t.Fatalf("expected no anchored match of [0-9]+ at pos 0 of %q, got (%d,%d)", "abc123", md.Begin(0), md.End(0))
	}
	if md := re.MatchAt("abc123", 3); md == nil || md.Begin(0) != 3 || md.End(0) != 6 {
		t.Fatalf("expected [0-9]+ to match 123 at pos 3")
	}
}
