package syntax

import (
	"errors"
	"reflect"
	"testing"

	"github.com/go-ruby-regexp/regexp/internal/ast"
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

func TestParseEscapedASCIIPunct(t *testing.T) {
	// Onigmo/MRI accept a backslash before any ASCII punctuation byte as that
	// literal byte, even when it is not a metacharacter. Verify a representative
	// spread both outside and inside a character class.
	for _, b := range []byte{'!', '/', '@', '%', '~', '"', '\'', '&', '=', '<', '>', ':', ';', ',', '`'} {
		r := mustParse(t, `\`+string(b))
		lit, ok := r.Root.(*ast.Literal)
		if !ok || lit.B != b {
			t.Errorf(`\%c outside class: got %#v, want literal %q`, b, r.Root, b)
		}
		rc := mustParse(t, `[\`+string(b)+`]`)
		cls, ok := rc.Root.(*ast.Class)
		if !ok || len(cls.Ranges) != 1 || byte(cls.Ranges[0].Lo) != b || byte(cls.Ranges[0].Hi) != b {
			t.Errorf(`[\%c]: got %#v, want class member %q`, b, rc.Root, b)
		}
	}
}

func TestParseControlEscapes(t *testing.T) {
	// \f \v \a \e are the form-feed, vertical-tab, bell, and escape control
	// bytes, accepted both outside and inside a character class (Onigmo/MRI).
	for _, tc := range []struct {
		esc  byte
		want byte
	}{{'f', '\f'}, {'v', '\v'}, {'a', '\a'}, {'e', 0x1b}} {
		r := mustParse(t, `\`+string(tc.esc))
		if lit, ok := r.Root.(*ast.Literal); !ok || lit.B != tc.want {
			t.Errorf(`\%c outside class: got %#v, want literal 0x%02x`, tc.esc, r.Root, tc.want)
		}
		rc := mustParse(t, `[\`+string(tc.esc)+`]`)
		cls, ok := rc.Root.(*ast.Class)
		if !ok || len(cls.Ranges) != 1 || byte(cls.Ranges[0].Lo) != tc.want {
			t.Errorf(`[\%c]: got %#v, want class member 0x%02x`, tc.esc, rc.Root, tc.want)
		}
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

func TestParsePosixClasses(t *testing.T) {
	// Every standard POSIX class name expands to the expected ASCII byte ranges
	// (verified against MRI Onigmo 4.0.5).
	for _, tc := range []struct {
		name string
		want []ast.ClassRange
	}{
		{"alpha", []ast.ClassRange{{Lo: 'A', Hi: 'Z'}, {Lo: 'a', Hi: 'z'}}},
		{"digit", []ast.ClassRange{{Lo: '0', Hi: '9'}}},
		{"alnum", []ast.ClassRange{{Lo: '0', Hi: '9'}, {Lo: 'A', Hi: 'Z'}, {Lo: 'a', Hi: 'z'}}},
		{"upper", []ast.ClassRange{{Lo: 'A', Hi: 'Z'}}},
		{"lower", []ast.ClassRange{{Lo: 'a', Hi: 'z'}}},
		{"space", []ast.ClassRange{{Lo: '\t', Hi: '\r'}, {Lo: ' ', Hi: ' '}}},
		{"blank", []ast.ClassRange{{Lo: '\t', Hi: '\t'}, {Lo: ' ', Hi: ' '}}},
		{"cntrl", []ast.ClassRange{{Lo: 0, Hi: 0x1f}, {Lo: 0x7f, Hi: 0x7f}}},
		{"graph", []ast.ClassRange{{Lo: '!', Hi: '~'}}},
		{"print", []ast.ClassRange{{Lo: ' ', Hi: '~'}}},
		{"punct", []ast.ClassRange{{Lo: '!', Hi: '/'}, {Lo: ':', Hi: '@'}, {Lo: '[', Hi: '`'}, {Lo: '{', Hi: '~'}}},
		{"xdigit", []ast.ClassRange{{Lo: '0', Hi: '9'}, {Lo: 'A', Hi: 'F'}, {Lo: 'a', Hi: 'f'}}},
		{"word", []ast.ClassRange{{Lo: '0', Hi: '9'}, {Lo: 'A', Hi: 'Z'}, {Lo: '_', Hi: '_'}, {Lo: 'a', Hi: 'z'}}},
	} {
		r := mustParse(t, "[[:"+tc.name+":]]")
		cls := r.Root.(*ast.Class)
		if !reflect.DeepEqual(cls.Ranges, tc.want) {
			t.Errorf("[[:%s:]] ranges = %v want %v", tc.name, cls.Ranges, tc.want)
		}
		if cls.Negate {
			t.Errorf("[[:%s:]] should not negate the outer class", tc.name)
		}
	}
}

func TestParsePosixClassNegated(t *testing.T) {
	// [[:^digit:]] is the complement of [0-9] over the full byte range.
	r := mustParse(t, "[[:^digit:]]")
	cls := r.Root.(*ast.Class)
	want := []ast.ClassRange{{Lo: 0, Hi: '0' - 1}, {Lo: '9' + 1, Hi: 0xff}}
	if !reflect.DeepEqual(cls.Ranges, want) {
		t.Fatalf("[[:^digit:]] ranges = %v want %v", cls.Ranges, want)
	}
}

func TestParsePosixClassMixedWithMembers(t *testing.T) {
	// A POSIX class can be combined with ordinary members and other POSIX
	// classes inside the same bracket expression.
	r := mustParse(t, "[x[:digit:]_[:upper:]]")
	cls := r.Root.(*ast.Class)
	want := []ast.ClassRange{
		{Lo: 'x', Hi: 'x'},
		{Lo: '0', Hi: '9'},
		{Lo: '_', Hi: '_'},
		{Lo: 'A', Hi: 'Z'},
	}
	if !reflect.DeepEqual(cls.Ranges, want) {
		t.Fatalf("ranges = %v want %v", cls.Ranges, want)
	}
}

func TestParseLiteralBracketInClass(t *testing.T) {
	// A '[' inside a class that is not followed by ':' is a literal member.
	r := mustParse(t, "[a[b]")
	cls := r.Root.(*ast.Class)
	want := []ast.ClassRange{{Lo: 'a', Hi: 'a'}, {Lo: '[', Hi: '['}, {Lo: 'b', Hi: 'b'}}
	if !reflect.DeepEqual(cls.Ranges, want) {
		t.Fatalf("ranges = %v want %v", cls.Ranges, want)
	}
}

func TestParsePosixClassErrors(t *testing.T) {
	for _, pat := range []string{
		`[[:bogus:]]`, // unknown class name
		`[[:alpha]`,   // name not closed by ":]" (hits ']' instead of ':')
		`[[:alph`,     // runs to EOF before ":]"
		`[[:alpha:`,   // ':' present but no ']' after it
		`[[:Alpha:]]`, // uppercase letter is not a valid name character
		`[[:^:]]`,     // empty (and thus unknown) negated name
	} {
		if _, err := Parse(pat); !errors.Is(err, ErrSyntax) {
			t.Errorf("Parse(%q): expected ErrSyntax, got %v", pat, err)
		}
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
		`(?'a)`,          // missing ' in quoted group name
		`(?'')`,          // empty quoted group name
		`(?'a b')`,       // invalid character in quoted group name
		`(?'ab`,          // quoted group name runs to EOF without '\''
		`(?'a')(?'a')`,   // duplicate quoted group name
		`\k'none'`,       // undefined quoted group name
		`\k'ab`,          // \k quoted name runs to EOF without '\''
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

// foldStates collects the fold state of every character atom, class, and backref
// in an AST, in walk order, for asserting inline-option scoping. A letter under
// /i is parsed as a rune-aware FoldLiteral (folded), a non-folding character as a
// plain Literal (not folded); a Class and a Backref carry an explicit Fold flag.
func foldStates(n ast.Node) []bool {
	var out []bool
	walk(n, func(x ast.Node) {
		switch t := x.(type) {
		case *ast.Literal:
			out = append(out, false)
		case *ast.FoldLiteral:
			out = append(out, true)
		case *ast.Class:
			out = append(out, t.Fold)
		case *ast.Backref:
			out = append(out, t.Fold)
		}
	})
	return out
}

func TestParseInlineFlagSetDirective(t *testing.T) {
	// (?i) sets fold for the rest of the enclosing group; it emits no node, so
	// "a(?i)b" is a 2-literal concat with only the second folded.
	r := mustParse(t, "a(?i)b")
	c, ok := r.Root.(*ast.Concat)
	if !ok || len(c.Subs) != 2 {
		t.Fatalf("expected concat of 2 (the directive emits no node), got %#v", r.Root)
	}
	if got := foldStates(r.Root); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("fold states = %v, want [false true]", got)
	}
}

func TestParseInlineFlagScopedGroup(t *testing.T) {
	// (?i:...) is a non-capturing group whose body folds; the trailing literal
	// outside it does not.
	r := mustParse(t, "(?i:a)b")
	c := r.Root.(*ast.Concat)
	g, ok := c.Subs[0].(*ast.Group)
	if !ok || g.Capture {
		t.Fatalf("expected non-capturing group, got %#v", c.Subs[0])
	}
	if got := foldStates(r.Root); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("fold states = %v, want [true false]", got)
	}
}

func TestParseInlineFlagTurnOff(t *testing.T) {
	// (?-i) turns folding back off; (?i-i:...) nets to off as well.
	if got := foldStates(mustParse(t, "(?i)a(?-i)b").Root); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("(?i)a(?-i)b fold = %v, want [true false]", got)
	}
	r := mustParse(t, "(?i-i:a)")
	g := r.Root.(*ast.Group)
	// Net folding is off, so the body is a plain (non-folding) byte Literal, not a
	// rune-aware FoldLiteral.
	if _, ok := g.Sub.(*ast.Literal); !ok {
		t.Fatalf("(?i-i:a) body should be a plain Literal, got %#v", g.Sub)
	}
}

func TestParseInlineFlagScopeDoesNotLeak(t *testing.T) {
	// A (?i) inside a group must not affect a sibling after the group closes.
	if got := foldStates(mustParse(t, "(a(?i)b)c").Root); len(got) != 3 || got[0] || !got[1] || got[2] {
		t.Fatalf("(a(?i)b)c fold = %v, want [false true false]", got)
	}
	// Nor must it leak out of a lookaround body.
	if got := foldStates(mustParse(t, "(?=a(?i))b").Root); len(got) != 2 || got[0] || got[1] {
		t.Fatalf("(?=a(?i))b fold = %v, want [false false]", got)
	}
}

func TestParseInlineFlagBranchPropagation(t *testing.T) {
	// A leading (?i) prefix of a branch propagates to later branches; one set
	// after a consuming atom does not.
	if got := foldStates(mustParse(t, "(?i)a|b").Root); len(got) != 2 || !got[0] || !got[1] {
		t.Fatalf("(?i)a|b fold = %v, want [true true]", got)
	}
	if got := foldStates(mustParse(t, "x(?i)y|z").Root); len(got) != 3 || got[0] || !got[1] || got[2] {
		t.Fatalf("x(?i)y|z fold = %v, want [false true false]", got)
	}
	if got := foldStates(mustParse(t, "a|(?i)b|c").Root); len(got) != 3 || got[0] || !got[1] || !got[2] {
		t.Fatalf("a|(?i)b|c fold = %v, want [false true true]", got)
	}
}

func TestParseInlineFlagOnClassAndBackref(t *testing.T) {
	// The fold flag reaches character classes and backreferences too.
	r := mustParse(t, `(?i)(a)[b]\1`)
	var sawClass, sawRef bool
	walk(r.Root, func(x ast.Node) {
		if cls, ok := x.(*ast.Class); ok {
			sawClass = true
			if !cls.Fold {
				t.Fatal("class under (?i) should fold")
			}
		}
		if ref, ok := x.(*ast.Backref); ok {
			sawRef = true
			if !ref.Fold {
				t.Fatal("backref under (?i) should fold")
			}
		}
	})
	if !sawClass || !sawRef {
		t.Fatalf("expected a class and a backref in the AST (class=%v ref=%v)", sawClass, sawRef)
	}
}

func TestParseInlineFlagErrors(t *testing.T) {
	for _, pat := range []string{
		`(?a)b`,    // 'a' is not a recognised flag letter
		`(?u:a)`,   // ditto for 'u' in a scoped group
		`(?ia:a)`,  // a recognised 'i' then an unsupported letter before ':'
		`(?im a)`,  // recognised i/m then an unsupported letter before the close
		`(?i-a:a)`, // unsupported flag letter after '-'
		`(?i`,      // EOF before ')' or ':'
		`(?-`,      // EOF right after '-'
		`(?i-`,     // EOF after the '-'
		`(?i:a`,    // scoped group body runs to EOF without a closing ')'
		`(?i:\q)`,  // a parse error inside a scoped-group body propagates out
	} {
		if _, err := Parse(pat); !errors.Is(err, ErrSyntax) {
			t.Errorf("Parse(%q): expected ErrSyntax, got %v", pat, err)
		}
	}
}
