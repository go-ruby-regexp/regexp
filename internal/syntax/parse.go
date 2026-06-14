// Package syntax holds the scanner and recursive-descent parser that turn an
// Onigmo/Ruby regular-expression pattern into an abstract syntax tree (the
// node types live in the sibling ast package).
//
// Phase 0 covers the common subset: literal and escaped characters, the dot
// metacharacter, character classes (with ranges, negation, and the Perl class
// escapes), the anchors \A \z \Z ^ $, the greedy quantifiers * + ? and {m,n},
// capturing and non-capturing groups, and alternation. Phase 1 adds named
// groups (?<name>...) and backreferences (\1, \k<name>). Phase 2 adds the
// lookaround assertions (?=...) (?!...) (?<=...) (?<!...) and the \G anchor.
//
// Lookbehind, as in Onigmo/Ruby, requires each alternative of its body to have
// a constant byte width (different alternatives may differ, e.g. (?<=ab|c));
// bodies whose width can vary — unbounded or {m,n} (m != n) quantifiers, and
// backreferences — are rejected at parse time.
package syntax

import (
	"errors"
	"fmt"

	"github.com/go-onigmo/regexp/internal/ast"
)

// ErrSyntax is the base error returned for malformed patterns. All parse
// failures wrap it so callers can test with errors.Is.
var ErrSyntax = errors.New("syntax error")

// Result is the outcome of parsing a pattern: the AST root, the number of
// capturing groups, and the name→index map for named captures.
type Result struct {
	Root       ast.Node
	NumCapture int
	Names      map[string]int
}

// parser is a single-use recursive-descent parser over the pattern bytes.
type parser struct {
	src        string
	pos        int
	ncap       int
	names      map[string]int
	maxBackref int // largest numeric backreference seen, validated after parsing
}

// Parse compiles a pattern string into an AST. It returns an error wrapping
// ErrSyntax when the pattern is malformed.
func Parse(pattern string) (Result, error) {
	p := &parser{src: pattern, names: map[string]int{}}
	node, err := p.parseAlternate()
	if err != nil {
		return Result{}, err
	}
	if p.pos != len(p.src) {
		// The only byte that ends parseAlternate early without consuming the
		// rest is a stray ')'.
		return Result{}, p.errorf("unexpected %q", p.src[p.pos])
	}
	if p.maxBackref > p.ncap {
		return Result{}, p.errorf("invalid backreference \\%d", p.maxBackref)
	}
	return Result{Root: node, NumCapture: p.ncap, Names: p.names}, nil
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSyntax, fmt.Sprintf(format, args...))
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() byte { return p.src[p.pos] }

func (p *parser) next() byte {
	b := p.src[p.pos]
	p.pos++
	return b
}

// parseAlternate parses a sequence of concatenations separated by '|'. It stops
// at end of input or at a ')'.
func (p *parser) parseAlternate() (ast.Node, error) {
	first, err := p.parseConcat()
	if err != nil {
		return nil, err
	}
	subs := []ast.Node{first}
	for !p.eof() && p.peek() == '|' {
		p.next()
		n, err := p.parseConcat()
		if err != nil {
			return nil, err
		}
		subs = append(subs, n)
	}
	if len(subs) == 1 {
		return subs[0], nil
	}
	return &ast.Alternate{Subs: subs}, nil
}

// parseConcat parses a run of quantified terms until a '|', a ')', or EOF.
func (p *parser) parseConcat() (ast.Node, error) {
	var subs []ast.Node
	for !p.eof() && p.peek() != '|' && p.peek() != ')' {
		term, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		subs = append(subs, term)
	}
	switch len(subs) {
	case 0:
		return &ast.Empty{}, nil
	case 1:
		return subs[0], nil
	default:
		return &ast.Concat{Subs: subs}, nil
	}
}

// parseTerm parses a single atom followed by an optional quantifier.
func (p *parser) parseTerm() (ast.Node, error) {
	atom, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	return p.parseQuantifier(atom)
}

// parseQuantifier applies a trailing *, +, ?, or {m,n} to atom, if present.
func (p *parser) parseQuantifier(atom ast.Node) (ast.Node, error) {
	if p.eof() {
		return atom, nil
	}
	switch p.peek() {
	case '*':
		p.next()
		return &ast.Star{Sub: atom, Min: 0, Max: -1}, nil
	case '+':
		p.next()
		return &ast.Star{Sub: atom, Min: 1, Max: -1}, nil
	case '?':
		p.next()
		return &ast.Star{Sub: atom, Min: 0, Max: 1}, nil
	case '{':
		return p.parseBrace(atom)
	default:
		return atom, nil
	}
}

// parseBrace parses a {m}, {m,}, or {m,n} repetition. A '{' that is not a valid
// repetition is treated as a literal brace, matching Ruby's behaviour.
func (p *parser) parseBrace(atom ast.Node) (ast.Node, error) {
	start := p.pos
	p.next() // consume '{'
	min, okMin := p.parseInt()
	if !okMin {
		// Not a count: treat '{' as a literal.
		p.pos = start
		p.next()
		return &ast.Literal{B: '{'}, nil
	}
	max := min
	if !p.eof() && p.peek() == ',' {
		p.next()
		if !p.eof() && p.peek() == '}' {
			max = -1 // {m,}
		} else {
			m, ok := p.parseInt()
			if !ok {
				p.pos = start
				p.next()
				return &ast.Literal{B: '{'}, nil
			}
			max = m
		}
	}
	if p.eof() || p.peek() != '}' {
		p.pos = start
		p.next()
		return &ast.Literal{B: '{'}, nil
	}
	p.next() // consume '}'
	if max != -1 && max < min {
		return nil, p.errorf("invalid repetition range {%d,%d}", min, max)
	}
	return &ast.Star{Sub: atom, Min: min, Max: max}, nil
}

// parseInt reads a non-negative decimal integer. It reports whether at least
// one digit was consumed.
func (p *parser) parseInt() (int, bool) {
	startPos := p.pos
	n := 0
	for !p.eof() && p.peek() >= '0' && p.peek() <= '9' {
		n = n*10 + int(p.next()-'0')
	}
	return n, p.pos != startPos
}

// parseAtom parses a single atom: a group, a class, the dot, an anchor, an
// escape, or a literal byte.
func (p *parser) parseAtom() (ast.Node, error) {
	b := p.peek()
	switch b {
	case '(':
		return p.parseGroup()
	case '[':
		return p.parseClass()
	case '.':
		p.next()
		return &ast.AnyChar{}, nil
	case '^':
		p.next()
		return &ast.Anchor{Kind: ast.AnchorBeginLine}, nil
	case '$':
		p.next()
		return &ast.Anchor{Kind: ast.AnchorEndLine}, nil
	case '\\':
		return p.parseEscape()
	case '*', '+', '?':
		return nil, p.errorf("nothing to repeat: %q", b)
	default:
		p.next()
		return &ast.Literal{B: b}, nil
	}
}

// parseGroup parses a capturing group (...), a non-capturing group (?:...), or
// a named capturing group (?<name>...).
func (p *parser) parseGroup() (ast.Node, error) {
	p.next() // consume '('
	capture := true
	name := ""
	if !p.eof() && p.peek() == '?' {
		p.next()
		switch {
		case !p.eof() && p.peek() == ':':
			p.next() // consume ':'
			capture = false
		case !p.eof() && p.peek() == '=':
			p.next() // consume '='
			return p.parseLook(false, false)
		case !p.eof() && p.peek() == '!':
			p.next() // consume '!'
			return p.parseLook(false, true)
		case !p.eof() && p.peek() == '<' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '=':
			p.next() // consume '<'
			p.next() // consume '='
			return p.parseLook(true, false)
		case !p.eof() && p.peek() == '<' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '!':
			p.next() // consume '<'
			p.next() // consume '!'
			return p.parseLook(true, true)
		case !p.eof() && p.peek() == '<':
			n, err := p.parseGroupName()
			if err != nil {
				return nil, err
			}
			name = n
		default:
			return nil, p.errorf("unsupported group syntax")
		}
	}
	var index int
	if capture {
		p.ncap++
		index = p.ncap
		if name != "" {
			if _, dup := p.names[name]; dup {
				return nil, p.errorf("duplicate group name <%s>", name)
			}
			p.names[name] = index
		}
	}
	sub, err := p.parseAlternate()
	if err != nil {
		return nil, err
	}
	if p.eof() || p.peek() != ')' {
		return nil, p.errorf("missing closing )")
	}
	p.next() // consume ')'
	return &ast.Group{Sub: sub, Capture: capture, Index: index, Name: name}, nil
}

// parseLook parses the body of a lookaround assertion whose introducer (one of
// (?= (?! (?<= (?<!) has already been consumed, up to and including the closing
// ')'. behind and negate select the variant. Lookbehind sub-patterns must have
// a constant byte width per alternative (see fixedWidth).
func (p *parser) parseLook(behind, negate bool) (ast.Node, error) {
	sub, err := p.parseAlternate()
	if err != nil {
		return nil, err
	}
	if p.eof() || p.peek() != ')' {
		return nil, p.errorf("missing closing )")
	}
	p.next() // consume ')'
	look := &ast.Look{Sub: sub, Behind: behind, Negate: negate}
	if behind {
		// Match Ruby/Onigmo: every alternative in a lookbehind must be of a
		// constant byte width (alternatives may differ from one another, e.g.
		// (?<=ab|c), but no single branch may vary in length). Backreferences
		// and unbounded or {m,n} (m != n) quantifiers are therefore rejected.
		if !fixedWidth(sub) {
			return nil, p.errorf("variable-width lookbehind is not supported")
		}
		min, max := widthRange(sub)
		look.Min, look.Max = min, max
	}
	return look, nil
}

// fixedWidth reports whether every alternative inside n matches a constant
// number of bytes, which is the condition Onigmo imposes on lookbehind bodies.
// Different alternatives may have different (constant) widths; only intra-branch
// variation — unbounded or {m,n} (m != n) quantifiers, and backreferences whose
// width is data-dependent — is disqualifying.
func fixedWidth(n ast.Node) bool {
	switch t := n.(type) {
	case *ast.Backref:
		// Width depends on captured text at match time.
		return false
	case *ast.Group:
		return fixedWidth(t.Sub)
	case *ast.Concat:
		for _, s := range t.Subs {
			if !fixedWidth(s) {
				return false
			}
		}
	case *ast.Alternate:
		for _, s := range t.Subs {
			if !fixedWidth(s) {
				return false
			}
		}
	case *ast.Star:
		return t.Min == t.Max && fixedWidth(t.Sub)
	}
	// Literal, AnyChar, Class, Anchor, Look, Empty, and containers whose parts
	// all checked out are constant-width.
	return true
}

// widthRange computes the minimum and maximum number of bytes n can match. It is
// only called on lookbehind bodies that fixedWidth has already accepted, so the
// width is always finite.
func widthRange(n ast.Node) (min, max int) {
	switch t := n.(type) {
	case *ast.Empty, *ast.Anchor, *ast.Look:
		return 0, 0
	case *ast.Literal, *ast.AnyChar, *ast.Class:
		return 1, 1
	case *ast.Group:
		return widthRange(t.Sub)
	case *ast.Concat:
		for _, s := range t.Subs {
			smin, smax := widthRange(s)
			min += smin
			max += smax
		}
		return min, max
	case *ast.Alternate:
		min = -1
		for _, s := range t.Subs {
			smin, smax := widthRange(s)
			if min == -1 || smin < min {
				min = smin
			}
			if smax > max {
				max = smax
			}
		}
		return min, max
	default: // *ast.Star with Min == Max
		s := n.(*ast.Star)
		smin, smax := widthRange(s.Sub)
		return smin * s.Min, smax * s.Max
	}
}

// parseGroupName reads a <name> after (?, returning the name (without angle
// brackets). The opening '<' is at the cursor.
func (p *parser) parseGroupName() (string, error) {
	p.next() // consume '<'
	start := p.pos
	for !p.eof() && p.peek() != '>' {
		c := p.peek()
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", p.errorf("invalid character %q in group name", c)
		}
		p.next()
	}
	if p.eof() {
		return "", p.errorf("missing > in group name")
	}
	if p.pos == start {
		return "", p.errorf("empty group name")
	}
	name := p.src[start:p.pos]
	p.next() // consume '>'
	return name, nil
}

// parseEscape parses a backslash escape outside a character class.
func (p *parser) parseEscape() (ast.Node, error) {
	p.next() // consume '\'
	if p.eof() {
		return nil, p.errorf("trailing backslash")
	}
	b := p.next()
	switch b {
	case 'A':
		return &ast.Anchor{Kind: ast.AnchorBeginText}, nil
	case 'z':
		return &ast.Anchor{Kind: ast.AnchorEndText}, nil
	case 'Z':
		return &ast.Anchor{Kind: ast.AnchorEndTextOptNL}, nil
	case 'G':
		return &ast.Anchor{Kind: ast.AnchorPrevMatch}, nil
	case 'd', 'D', 'w', 'W', 's', 'S':
		return perlClass(b), nil
	case 'n':
		return &ast.Literal{B: '\n'}, nil
	case 't':
		return &ast.Literal{B: '\t'}, nil
	case 'r':
		return &ast.Literal{B: '\r'}, nil
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		idx := int(b - '0')
		for !p.eof() && p.peek() >= '0' && p.peek() <= '9' {
			idx = idx*10 + int(p.next()-'0')
		}
		if idx > p.maxBackref {
			p.maxBackref = idx
		}
		return &ast.Backref{Index: idx}, nil
	case 'k':
		return p.parseNamedBackref()
	case '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
		return &ast.Literal{B: b}, nil
	default:
		return nil, p.errorf("unsupported escape \\%c", b)
	}
}

// parseNamedBackref parses \k<name>, resolving to the (already-defined) group
// index. The cursor is just past the 'k'.
func (p *parser) parseNamedBackref() (ast.Node, error) {
	if p.eof() || p.peek() != '<' {
		return nil, p.errorf("expected <name> after \\k")
	}
	name, err := p.parseGroupName()
	if err != nil {
		return nil, err
	}
	idx, ok := p.names[name]
	if !ok {
		return nil, p.errorf("undefined group name <%s>", name)
	}
	return &ast.Backref{Index: idx}, nil
}

// perlClass builds the Class node for one of the Perl class escapes \d \D \w \W
// \s \S.
func perlClass(b byte) *ast.Class {
	switch b {
	case 'd':
		return &ast.Class{Ranges: digitRanges()}
	case 'D':
		return &ast.Class{Ranges: digitRanges(), Negate: true}
	case 'w':
		return &ast.Class{Ranges: wordRanges()}
	case 'W':
		return &ast.Class{Ranges: wordRanges(), Negate: true}
	case 's':
		return &ast.Class{Ranges: spaceRanges()}
	default: // 'S'
		return &ast.Class{Ranges: spaceRanges(), Negate: true}
	}
}

func digitRanges() []ast.ClassRange {
	return []ast.ClassRange{{Lo: '0', Hi: '9'}}
}

func wordRanges() []ast.ClassRange {
	return []ast.ClassRange{{Lo: '0', Hi: '9'}, {Lo: 'A', Hi: 'Z'}, {Lo: '_', Hi: '_'}, {Lo: 'a', Hi: 'z'}}
}

func spaceRanges() []ast.ClassRange {
	return []ast.ClassRange{{Lo: '\t', Hi: '\n'}, {Lo: '\v', Hi: '\f'}, {Lo: '\r', Hi: '\r'}, {Lo: ' ', Hi: ' '}}
}

// parseClass parses a bracketed character class [...].
func (p *parser) parseClass() (ast.Node, error) {
	p.next() // consume '['
	cls := &ast.Class{}
	if !p.eof() && p.peek() == '^' {
		p.next()
		cls.Negate = true
	}
	// A ']' as the first member is a literal ']' in Ruby/Onigmo.
	first := true
	for {
		if p.eof() {
			return nil, p.errorf("missing closing ] in character class")
		}
		if p.peek() == ']' && !first {
			p.next()
			return cls, nil
		}
		first = false
		lo, sub, err := p.parseClassItem()
		if err != nil {
			return nil, err
		}
		if sub != nil {
			// A class escape such as \d expands to ranges directly.
			cls.Ranges = append(cls.Ranges, sub...)
			continue
		}
		// Possible range lo-hi.
		if !p.eof() && p.peek() == '-' && p.pos+1 < len(p.src) && p.src[p.pos+1] != ']' {
			p.next() // consume '-'
			hi, subHi, err := p.parseClassItem()
			if err != nil {
				return nil, err
			}
			if subHi != nil {
				return nil, p.errorf("invalid range end in character class")
			}
			if hi < lo {
				return nil, p.errorf("invalid range %q-%q in character class", lo, hi)
			}
			cls.Ranges = append(cls.Ranges, ast.ClassRange{Lo: lo, Hi: hi})
			continue
		}
		cls.Ranges = append(cls.Ranges, ast.ClassRange{Lo: lo, Hi: lo})
	}
}

// parseClassItem parses one member of a character class: either a single byte
// (returned as lo with sub == nil) or a class escape (returned as a slice of
// ranges with lo unused). It assumes at least one byte remains.
func (p *parser) parseClassItem() (byte, []ast.ClassRange, error) {
	b := p.next()
	if b != '\\' {
		return b, nil, nil
	}
	if p.eof() {
		return 0, nil, p.errorf("trailing backslash in character class")
	}
	e := p.next()
	switch e {
	case 'd':
		return 0, digitRanges(), nil
	case 'w':
		return 0, wordRanges(), nil
	case 's':
		return 0, spaceRanges(), nil
	case 'D':
		return 0, negateRanges(digitRanges()), nil
	case 'W':
		return 0, negateRanges(wordRanges()), nil
	case 'S':
		return 0, negateRanges(spaceRanges()), nil
	case 'n':
		return '\n', nil, nil
	case 't':
		return '\t', nil, nil
	case 'r':
		return '\r', nil, nil
	case '\\', ']', '[', '^', '-':
		return e, nil, nil
	default:
		return 0, nil, p.errorf("unsupported escape \\%c in character class", e)
	}
}

// negateRanges returns the complement (over the full byte range) of a sorted,
// non-overlapping set of ranges, so a negated class escape inside a positive
// class behaves correctly.
func negateRanges(rs []ast.ClassRange) []ast.ClassRange {
	var out []ast.ClassRange
	next := 0
	for _, r := range rs {
		if int(r.Lo) > next {
			out = append(out, ast.ClassRange{Lo: byte(next), Hi: r.Lo - 1})
		}
		next = int(r.Hi) + 1
	}
	if next <= 0xff {
		out = append(out, ast.ClassRange{Lo: byte(next), Hi: 0xff})
	}
	return out
}
