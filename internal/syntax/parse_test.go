package syntax

import (
	"errors"
	"reflect"
	"testing"

	"github.com/go-onigmo/regexp/internal/ast"
)

// mustParse parses p, failing the test on error.
func mustParse(t *testing.T, p string) Result {
	t.Helper()
	r, err := Parse(p)
	if err != nil {
		t.Fatalf("Parse(%q): unexpected error: %v", p, err)
	}
	return r
}

func TestParseLiteralAndConcat(t *testing.T) {
	r := mustParse(t, "ab")
	c, ok := r.Root.(*ast.Concat)
	if !ok || len(c.Subs) != 2 {
		t.Fatalf("expected ast.Concat of 2, got %#v", r.Root)
	}
	if l, ok := c.Subs[0].(*ast.Literal); !ok || l.B != 'a' {
		t.Fatalf("expected literal a, got %#v", c.Subs[0])
	}
}

func TestParseSingleLiteral(t *testing.T) {
	r := mustParse(t, "a")
	if l, ok := r.Root.(*ast.Literal); !ok || l.B != 'a' {
		t.Fatalf("expected literal a, got %#v", r.Root)
	}
}

func TestParseEmpty(t *testing.T) {
	r := mustParse(t, "")
	if _, ok := r.Root.(*ast.Empty); !ok {
		t.Fatalf("expected ast.Empty, got %#v", r.Root)
	}
}

func TestParseAlternateSingleCollapses(t *testing.T) {
	r := mustParse(t, "a")
	if _, ok := r.Root.(*ast.Alternate); ok {
		t.Fatalf("single branch must not produce ast.Alternate")
	}
}

func TestParseAlternate(t *testing.T) {
	r := mustParse(t, "a|b|c")
	a, ok := r.Root.(*ast.Alternate)
	if !ok || len(a.Subs) != 3 {
		t.Fatalf("expected ast.Alternate of 3, got %#v", r.Root)
	}
}

func TestParseDotAndAnchors(t *testing.T) {
	r := mustParse(t, ".^$")
	c := r.Root.(*ast.Concat)
	if _, ok := c.Subs[0].(*ast.AnyChar); !ok {
		t.Fatalf("expected ast.AnyChar")
	}
	if c.Subs[1].(*ast.Anchor).Kind != ast.AnchorBeginLine {
		t.Fatalf("expected ^")
	}
	if c.Subs[2].(*ast.Anchor).Kind != ast.AnchorEndLine {
		t.Fatalf("expected $")
	}
}

func TestParseQuantifiers(t *testing.T) {
	for _, tc := range []struct {
		pat      string
		min, max int
	}{
		{"a*", 0, -1},
		{"a+", 1, -1},
		{"a?", 0, 1},
		{"a{3}", 3, 3},
		{"a{2,}", 2, -1},
		{"a{2,5}", 2, 5},
	} {
		r := mustParse(t, tc.pat)
		s, ok := r.Root.(*ast.Star)
		if !ok {
			t.Fatalf("%q: expected ast.Star, got %#v", tc.pat, r.Root)
		}
		if s.Min != tc.min || s.Max != tc.max {
			t.Errorf("%q: got {%d,%d} want {%d,%d}", tc.pat, s.Min, s.Max, tc.min, tc.max)
		}
	}
}

func TestParseBraceLiteralFallback(t *testing.T) {
	// Each of these has a '{' that is not a valid repetition, so '{' is a
	// literal byte.
	for _, pat := range []string{"a{", "a{x}", "a{1,x}", "a{1,2"} {
		r := mustParse(t, pat)
		var found bool
		walk(r.Root, func(n ast.Node) {
			if l, ok := n.(*ast.Literal); ok && l.B == '{' {
				found = true
			}
		})
		if !found {
			t.Errorf("%q: expected a literal '{' in the AST", pat)
		}
	}
}

func TestParseBraceInvalidRange(t *testing.T) {
	_, err := Parse("a{3,1}")
	if !errors.Is(err, ErrSyntax) {
		t.Fatalf("expected ErrSyntax, got %v", err)
	}
}

func TestParseGroups(t *testing.T) {
	r := mustParse(t, "(a)(?:b)(c)")
	if r.NumCapture != 2 {
		t.Fatalf("expected 2 captures, got %d", r.NumCapture)
	}
	c := r.Root.(*ast.Concat)
	g0 := c.Subs[0].(*ast.Group)
	if !g0.Capture || g0.Index != 1 {
		t.Errorf("group 0: capture=%v index=%d", g0.Capture, g0.Index)
	}
	if c.Subs[1].(*ast.Group).Capture {
		t.Errorf("group 1 should be non-capturing")
	}
	if c.Subs[2].(*ast.Group).Index != 2 {
		t.Errorf("group 2 index wrong")
	}
}

func TestParseLookaround(t *testing.T) {
	for _, tc := range []struct {
		pat            string
		behind, negate bool
	}{
		{`(?=a)`, false, false},
		{`(?!a)`, false, true},
		{`(?<=a)`, true, false},
		{`(?<!a)`, true, true},
	} {
		r := mustParse(t, tc.pat)
		l, ok := r.Root.(*ast.Look)
		if !ok {
			t.Fatalf("%q: expected ast.Look, got %#v", tc.pat, r.Root)
		}
		if l.Behind != tc.behind || l.Negate != tc.negate {
			t.Errorf("%q: behind=%v negate=%v want %v/%v", tc.pat, l.Behind, l.Negate, tc.behind, tc.negate)
		}
	}
}

func TestParseLookbehindWidths(t *testing.T) {
	for _, tc := range []struct {
		pat      string
		min, max int
	}{
		{`(?<=abc)x`, 3, 3},     // fixed concat
		{`(?<=a{2})x`, 2, 2},    // fixed repetition
		{`(?<=ab|c)x`, 1, 2},    // alternation of differing fixed widths
		{`(?<=(ab))x`, 2, 2},    // group
		{`(?<=^ab)x`, 2, 2},     // anchor contributes zero width
		{`(?<=(?=z)ab)x`, 2, 2}, // nested lookahead is zero-width
		{`(?<=)x`, 0, 0},        // empty body
	} {
		r := mustParse(t, tc.pat)
		c := r.Root.(*ast.Concat)
		l := c.Subs[0].(*ast.Look)
		if l.Min != tc.min || l.Max != tc.max {
			t.Errorf("%q: width [%d,%d] want [%d,%d]", tc.pat, l.Min, l.Max, tc.min, tc.max)
		}
	}
}

func TestParsePrevMatchAnchor(t *testing.T) {
	r := mustParse(t, `\Gabc`)
	c := r.Root.(*ast.Concat)
	if c.Subs[0].(*ast.Anchor).Kind != ast.AnchorPrevMatch {
		t.Fatalf("expected \\G anchor, got %#v", c.Subs[0])
	}
}

func TestParseEscapes(t *testing.T) {
	r := mustParse(t, `\A\z\Z\n\t\r\.\\`)
	c := r.Root.(*ast.Concat)
	if c.Subs[0].(*ast.Anchor).Kind != ast.AnchorBeginText {
		t.Error("\\A")
	}
	if c.Subs[1].(*ast.Anchor).Kind != ast.AnchorEndText {
		t.Error("\\z")
	}
	if c.Subs[2].(*ast.Anchor).Kind != ast.AnchorEndTextOptNL {
		t.Error("\\Z")
	}
	if c.Subs[3].(*ast.Literal).B != '\n' {
		t.Error("\\n")
	}
	if c.Subs[4].(*ast.Literal).B != '\t' {
		t.Error("\\t")
	}
	if c.Subs[5].(*ast.Literal).B != '\r' {
		t.Error("\\r")
	}
	if c.Subs[6].(*ast.Literal).B != '.' {
		t.Error("\\.")
	}
	if c.Subs[7].(*ast.Literal).B != '\\' {
		t.Error("\\\\")
	}
}

func TestParsePerlClasses(t *testing.T) {
	for _, tc := range []struct {
		pat    string
		negate bool
	}{
		{`\d`, false}, {`\D`, true},
		{`\w`, false}, {`\W`, true},
		{`\s`, false}, {`\S`, true},
	} {
		r := mustParse(t, tc.pat)
		cls, ok := r.Root.(*ast.Class)
		if !ok {
			t.Fatalf("%q: expected ast.Class, got %#v", tc.pat, r.Root)
		}
		if cls.Negate != tc.negate {
			t.Errorf("%q: negate=%v want %v", tc.pat, cls.Negate, tc.negate)
		}
	}
}

func TestParseClassRangesAndNegation(t *testing.T) {
	r := mustParse(t, "[^a-z0]")
	cls := r.Root.(*ast.Class)
	if !cls.Negate {
		t.Fatal("expected negated class")
	}
	want := []ast.ClassRange{{Lo: 'a', Hi: 'z'}, {Lo: '0', Hi: '0'}}
	if !reflect.DeepEqual(cls.Ranges, want) {
		t.Fatalf("ranges = %v want %v", cls.Ranges, want)
	}
}

func TestParseClassLeadingBracket(t *testing.T) {
	// A ']' right after '[' is a literal member.
	r := mustParse(t, "[]a]")
	cls := r.Root.(*ast.Class)
	want := []ast.ClassRange{{Lo: ']', Hi: ']'}, {Lo: 'a', Hi: 'a'}}
	if !reflect.DeepEqual(cls.Ranges, want) {
		t.Fatalf("ranges = %v want %v", cls.Ranges, want)
	}
}

func TestParseClassDashAtEnd(t *testing.T) {
	// A '-' just before ']' is a literal dash, not a range operator.
	r := mustParse(t, "[a-]")
	cls := r.Root.(*ast.Class)
	want := []ast.ClassRange{{Lo: 'a', Hi: 'a'}, {Lo: '-', Hi: '-'}}
	if !reflect.DeepEqual(cls.Ranges, want) {
		t.Fatalf("ranges = %v want %v", cls.Ranges, want)
	}
}

func TestParseClassEscapes(t *testing.T) {
	r := mustParse(t, `[\d\w\s\D\W\S\n\t\r\\\]\[\^\-]`)
	cls := r.Root.(*ast.Class)
	if len(cls.Ranges) == 0 {
		t.Fatal("expected ranges")
	}
}

func TestParseClassEscapeMembers(t *testing.T) {
	r := mustParse(t, `[\n\t\r\\\]\[\^\-]`)
	cls := r.Root.(*ast.Class)
	want := []ast.ClassRange{
		{Lo: '\n', Hi: '\n'}, {Lo: '\t', Hi: '\t'}, {Lo: '\r', Hi: '\r'},
		{Lo: '\\', Hi: '\\'}, {Lo: ']', Hi: ']'}, {Lo: '[', Hi: '['}, {Lo: '^', Hi: '^'}, {Lo: '-', Hi: '-'},
	}
	if !reflect.DeepEqual(cls.Ranges, want) {
		t.Fatalf("ranges = %v want %v", cls.Ranges, want)
	}
}

func TestNegateRanges(t *testing.T) {
	// \D inside a class is the complement of [0-9].
	r := mustParse(t, `[\D]`)
	cls := r.Root.(*ast.Class)
	want := []ast.ClassRange{{Lo: 0, Hi: '0' - 1}, {Lo: '9' + 1, Hi: 0xff}}
	if !reflect.DeepEqual(cls.Ranges, want) {
		t.Fatalf("ranges = %v want %v", cls.Ranges, want)
	}
}

func TestNegateRangesIncludesZero(t *testing.T) {
	// \w starts at '0' (0x30), so its complement starts at byte 0; \S whose
	// first range is \t (0x09) also yields a leading [0..0x08] range. Use \w to
	// hit the "first range above zero" path; the all-from-zero edge is covered
	// by a synthetic range starting at 0.
	got := negateRanges([]ast.ClassRange{{Lo: 0, Hi: 5}})
	want := []ast.ClassRange{{Lo: 6, Hi: 0xff}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("negateRanges = %v want %v", got, want)
	}
}

func TestNegateRangesFullByte(t *testing.T) {
	// A range covering the whole byte space negates to nothing.
	got := negateRanges([]ast.ClassRange{{Lo: 0, Hi: 0xff}})
	if len(got) != 0 {
		t.Fatalf("negateRanges of full range = %v want empty", got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, pat := range []string{
		`(`,              // missing closing )
		`)`,              // unexpected )
		`(?`,             // unsupported group syntax at EOF
		`(?<a)`,          // missing > in group name
		`(?<>a)`,         // empty group name
		`(?<a b>x)`,      // invalid character in group name
		`(?<ab`,          // group name runs to EOF without '>'
		`(?<a>x)(?<a>y)`, // duplicate group name
		`\1`,             // backreference to a non-existent group
		`(a)\2`,          // backreference index exceeds group count
		`\12`,            // multi-digit backreference exceeds group count
		`\k<none>`,       // undefined group name
		`\k<ab`,          // \k name runs to EOF without '>'
		`\k`,             // \k without a name
		`*`,              // nothing to repeat
		`+`,              // nothing to repeat
		`?`,              // nothing to repeat
		`\`,              // trailing backslash
		`\q`,             // unsupported escape
		`[`,              // missing closing ]
		`[a`,             // missing closing ]
		`[a-`,            // missing closing ] (dash at very end with no close)
		`[\`,             // trailing backslash in class
		`[\q]`,           // unsupported escape in class
		`[z-a]`,          // invalid range
		`[a-\d]`,         // invalid range end (class escape as range hi)
		`a|*`,            // error in alternation branch after |
		`(*)`,            // error inside a group's alternation
		`[a-\q]`,         // unsupported escape as range hi
		`(?=a`,           // lookahead missing closing )
		`(?!a`,           // negative lookahead missing closing )
		`(?<=a`,          // lookbehind missing closing )
		`(?<!a`,          // negative lookbehind missing closing )
		`(?=*)`,          // error inside a lookahead body
		`(?<=a*)b`,       // variable-width lookbehind (unbounded quantifier)
		`(?<=a{2,3})b`,   // variable-width lookbehind ({m,n}, m != n)
		`(?<=(a)\1)b`,    // variable-width lookbehind (backreference)
		`(?<=a|b+)c`,     // lookbehind alternation with a variable-width branch
	} {
		if _, err := Parse(pat); !errors.Is(err, ErrSyntax) {
			t.Errorf("Parse(%q): expected ErrSyntax, got %v", pat, err)
		}
	}
}

func TestParseQuantifierAtEOFNoOp(t *testing.T) {
	// parseQuantifier on the last atom with nothing following.
	r := mustParse(t, "a")
	if _, ok := r.Root.(*ast.Literal); !ok {
		t.Fatal("expected plain literal")
	}
}

// walk visits every node in an AST.
func walk(n ast.Node, f func(ast.Node)) {
	f(n)
	switch t := n.(type) {
	case *ast.Concat:
		for _, s := range t.Subs {
			walk(s, f)
		}
	case *ast.Alternate:
		for _, s := range t.Subs {
			walk(s, f)
		}
	case *ast.Star:
		walk(t.Sub, f)
	case *ast.Group:
		walk(t.Sub, f)
	case *ast.Look:
		walk(t.Sub, f)
	}
}
