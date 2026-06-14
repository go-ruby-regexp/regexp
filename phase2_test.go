package onigmo_test

import (
	"testing"

	onigmo "github.com/go-onigmo/regexp"
)

func TestLookahead(t *testing.T) {
	re := mustCompile(t, `\d+(?=px)`)
	m := re.Match("size 10px wide")
	if m == nil || m.Str(0) != "10" {
		t.Fatalf("positive lookahead: %+v", m)
	}
	if mustCompile(t, `foo(?=bar)`).MatchString("foobaz") {
		t.Fatal(`"foobaz" should not match foo(?=bar)`)
	}
}

func TestNegativeLookahead(t *testing.T) {
	// \d+ backtracks from "10" (which is followed by "px") to "1", whose next
	// position is "0px" — not "px" — so the negative lookahead succeeds there.
	// This matches Ruby exactly: the match is "1" at [0,1).
	re := mustCompile(t, `\d+(?!px)`)
	m := re.Match("10px 20em")
	if m == nil || m.Str(0) != "1" || m.Begin(0) != 0 {
		t.Fatalf("negative lookahead: %+v", m)
	}
	// A digit run not followed by "px" matches in full.
	if m := re.Match("20em"); m == nil || m.Str(0) != "20" {
		t.Fatalf("negative lookahead on 20em: %+v", m)
	}
}

func TestLookaheadCaptureVisible(t *testing.T) {
	// A capture made inside a positive lookahead is visible to the caller.
	m := mustCompile(t, `(?=(\d+))\d`).Match("x42y")
	if m == nil || m.Str(1) != "42" {
		t.Fatalf("lookahead capture should be 42: %+v", m)
	}
}

func TestLookbehind(t *testing.T) {
	re := mustCompile(t, `(?<=\$)\d+`)
	m := re.Match("total $99 due")
	if m == nil || m.Str(0) != "99" {
		t.Fatalf("positive lookbehind: %+v", m)
	}
	if mustCompile(t, `(?<=foo)bar`).MatchString("xxxbar") {
		t.Fatal(`"xxxbar" should not match (?<=foo)bar`)
	}
}

func TestNegativeLookbehind(t *testing.T) {
	re := mustCompile(t, `(?<!\d)\d`)
	m := re.Match("a1")
	if m == nil || m.Begin(0) != 1 {
		t.Fatalf("negative lookbehind should match the 1 not preceded by a digit: %+v", m)
	}
}

func TestLookbehindAlternationWidths(t *testing.T) {
	// Alternatives of different (but each constant) widths are allowed.
	re := mustCompile(t, `(?<=ab|c)d`)
	if m := re.Match("abd"); m == nil || m.Begin(0) != 2 {
		t.Fatalf(`(?<=ab|c)d on "abd": %+v`, m)
	}
	if m := re.Match("cd"); m == nil || m.Begin(0) != 1 {
		t.Fatalf(`(?<=ab|c)d on "cd": %+v`, m)
	}
}

func TestVariableWidthLookbehindRejected(t *testing.T) {
	// Onigmo (and this engine) reject lookbehind whose body can vary in width.
	for _, pat := range []string{`(?<=a+)b`, `(?<=a{2,3})b`, `(?<=(x)\1)y`} {
		if _, err := onigmo.Compile(pat); err == nil {
			t.Errorf("Compile(%q) should reject variable-width lookbehind", pat)
		}
	}
}

func TestPrevMatchAnchor(t *testing.T) {
	// \G anchors to the scan origin, so for a single Match it behaves like \A.
	if !mustCompile(t, `\Gabc`).MatchString("abcdef") {
		t.Fatal(`\Gabc should match "abcdef"`)
	}
	if mustCompile(t, `\Gabc`).MatchString("xabcdef") {
		t.Fatal(`\Gabc should not match "xabcdef" (\G pins position 0)`)
	}
}
