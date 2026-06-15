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
// Phase 3 begins with POSIX bracket expressions [[:name:]] (and negated
// [[:^name:]]) inside character classes, for the standard classes alpha, digit,
// alnum, upper, lower, space, blank, cntrl, graph, print, punct, xdigit, and
// word; their byte ranges match Onigmo's defaults for the ASCII byte space. It
// also adds ASCII case-insensitive matching through the inline options (?i)
// (a set directive scoped to the rest of the enclosing group), (?i:...) (a
// scoped group), and (?-i) / (?i-i:...) to turn it off again; the fold flag is
// recorded on literals, character classes, and backreferences. Only the i flag
// is recognised so far — other inline flags are reported as syntax errors.
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

// flags holds the inline option state in effect at a point in the parse. Only
// the case-insensitive option (i) is modelled so far; it is toggled by (?i) /
// (?-i) and scoped by (?i:...) / (?-i:...), exactly as in Onigmo/Ruby.
type flags struct {
	fold bool // case-insensitive matching (/i)
}

// parser is a single-use recursive-descent parser over the pattern bytes.
type parser struct {
	src        string
	pos        int
	ncap       int
	names      map[string]int
	maxBackref int   // largest numeric backreference seen, validated after parsing
	flags      flags // inline option state at the cursor (e.g. /i via (?i))
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
//
// Inline option scoping follows Onigmo/Ruby: an option-setting directive (?i) /
// (?-i) that appears as the leading prefix of a branch — before any atom is
// consumed — updates the baseline options that subsequent branches of the same
// alternation inherit; once a branch has consumed an atom, a later (?i) is local
// to that branch only. So (?i)a|b folds b, but a(?i)|b does not.
func (p *parser) parseAlternate() (ast.Node, error) {
	baseline := p.flags
	first, prefix, err := p.parseConcat()
	if err != nil {
		return nil, err
	}
	baseline = prefix
	subs := []ast.Node{first}
	for !p.eof() && p.peek() == '|' {
		p.next()
		p.flags = baseline
		n, prefix, err := p.parseConcat()
		if err != nil {
			return nil, err
		}
		baseline = prefix
		subs = append(subs, n)
	}
	if len(subs) == 1 {
		return subs[0], nil
	}
	return &ast.Alternate{Subs: subs}, nil
}

// parseConcat parses a run of quantified terms until a '|', a ')', or EOF. It
// also returns the option state established by any leading inline-flag prefix
// (the options in effect just before the first consuming atom), which the
// alternation uses as the baseline for the following branch.
func (p *parser) parseConcat() (ast.Node, flags, error) {
	var subs []ast.Node
	prefix := p.flags
	sawAtom := false
	for !p.eof() && p.peek() != '|' && p.peek() != ')' {
		term, err := p.parseTerm()
		if err != nil {
			return nil, flags{}, err
		}
		if term != nil {
			// A consuming term ends the leading inline-flag prefix; further flag
			// changes in this branch no longer propagate to sibling branches.
			subs = append(subs, term)
			sawAtom = true
		} else if !sawAtom {
			// A leading inline-flag directive (it emits no node): record the
			// updated options as the branch's prefix baseline.
			prefix = p.flags
		}
	}
	switch len(subs) {
	case 0:
		return &ast.Empty{}, prefix, nil
	case 1:
		return subs[0], prefix, nil
	default:
		return &ast.Concat{Subs: subs}, prefix, nil
	}
}

// parseTerm parses a single atom followed by an optional quantifier. It returns
// a nil node (and no error) for an inline option-setting directive (?i)/(?-i),
// which consumes input but produces no AST node and takes no quantifier.
func (p *parser) parseTerm() (ast.Node, error) {
	atom, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	if atom == nil {
		return nil, nil
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
		return &ast.Literal{B: b, Fold: p.flags.fold}, nil
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
		case !p.eof() && (p.peek() == 'i' || p.peek() == '-'):
			return p.parseInlineFlags()
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
	// Options set by an inline (?i) directive inside the group are scoped to the
	// group, so snapshot the entry options and restore them once the group body
	// is parsed (e.g. in (a(?i))b the trailing b is not folded).
	saved := p.flags
	sub, err := p.parseAlternate()
	if err != nil {
		return nil, err
	}
	if p.eof() || p.peek() != ')' {
		return nil, p.errorf("missing closing )")
	}
	p.next() // consume ')'
	p.flags = saved
	return &ast.Group{Sub: sub, Capture: capture, Index: index, Name: name}, nil
}

// parseInlineFlags parses an inline option construct whose "(?" has already been
// consumed and whose cursor is at the first flag letter or '-'. Two forms are
// recognised, matching Onigmo/Ruby:
//
//	(?flags)        a set directive: it changes the current options for the
//	                remainder of the enclosing group and produces no AST node
//	                (parseInlineFlags returns a nil node).
//	(?flags:body)   a scoped group: body is parsed under the modified options,
//	                which are restored when the group closes.
//
// Only the i (case-insensitive) flag is supported; any other flag letter is a
// syntax error. The optional '-' introduces the negated (turned-off) flags.
func (p *parser) parseInlineFlags() (ast.Node, error) {
	saved := p.flags
	on, off, err := p.parseFlagLetters()
	if err != nil {
		return nil, err
	}
	if p.eof() {
		return nil, p.errorf("unterminated inline options")
	}
	applied := saved
	if on {
		applied.fold = true
	}
	if off {
		applied.fold = false
	}
	// parseFlagLetters consumes up to (but not past) the terminator, which the
	// EOF check above proved is present, so it is either ':' or ')'.
	if p.next() == ')' {
		// Set directive: mutate the options for the rest of the enclosing group
		// and emit nothing.
		p.flags = applied
		return nil, nil
	}
	// Scoped group (?flags:body): parse the body under the new options, then
	// restore them.
	p.flags = applied
	sub, err := p.parseAlternate()
	if err != nil {
		return nil, err
	}
	if p.eof() || p.peek() != ')' {
		return nil, p.errorf("missing closing )")
	}
	p.next() // consume ')'
	p.flags = saved
	return &ast.Group{Sub: sub, Capture: false}, nil
}

// parseFlagLetters reads the flag specification of an inline option construct: a
// run of supported flag letters, then an optional '-' followed by another run of
// letters to turn off. It reports whether i was switched on and whether it was
// switched off (Ruby permits both, e.g. (?i-i:...), the later -i winning).
func (p *parser) parseFlagLetters() (on, off bool, err error) {
	for !p.eof() && p.peek() != '-' && p.peek() != ':' && p.peek() != ')' {
		if p.next() != 'i' {
			return false, false, p.errorf("unsupported inline option flag")
		}
		on = true
	}
	if !p.eof() && p.peek() == '-' {
		p.next()
		for !p.eof() && p.peek() != ':' && p.peek() != ')' {
			if p.next() != 'i' {
				return false, false, p.errorf("unsupported inline option flag")
			}
			off = true
		}
	}
	return on, off, nil
}

// parseLook parses the body of a lookaround assertion whose introducer (one of
// (?= (?! (?<= (?<!) has already been consumed, up to and including the closing
// ')'. behind and negate select the variant. Lookbehind sub-patterns must have
// a constant byte width per alternative (see fixedWidth).
func (p *parser) parseLook(behind, negate bool) (ast.Node, error) {
	// Inline options set inside a lookaround body are scoped to it, like any
	// other group.
	saved := p.flags
	sub, err := p.parseAlternate()
	if err != nil {
		return nil, err
	}
	if p.eof() || p.peek() != ')' {
		return nil, p.errorf("missing closing )")
	}
	p.next() // consume ')'
	p.flags = saved
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
		return &ast.Backref{Index: idx, Fold: p.flags.fold}, nil
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
	return &ast.Backref{Index: idx, Fold: p.flags.fold}, nil
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
	cls := &ast.Class{Fold: p.flags.fold}
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
		// A POSIX bracket expression [[:name:]] (or negated [[:^name:]]) appears
		// only inside a character class. It is recognised when the cursor is at a
		// '[' immediately followed by ':'; otherwise '[' is a literal member.
		if p.peek() == '[' && p.pos+1 < len(p.src) && p.src[p.pos+1] == ':' {
			ranges, err := p.parsePosixClass()
			if err != nil {
				return nil, err
			}
			cls.Ranges = append(cls.Ranges, ranges...)
			continue
		}
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

// parsePosixClass parses a POSIX bracket expression [[:name:]] or its negated
// form [[:^name:]] inside a character class. The cursor is at the leading '['
// (which is known to be followed by ':'). It returns the byte ranges the class
// contributes; for the negated form those are the complement, over the full
// 0..255 byte range, of the positive class — matching Onigmo's byte-oriented
// behaviour where, e.g., [[:^alpha:]] matches any non-ASCII-letter byte.
func (p *parser) parsePosixClass() ([]ast.ClassRange, error) {
	p.next() // consume '['
	p.next() // consume ':'
	negate := false
	if !p.eof() && p.peek() == '^' {
		p.next()
		negate = true
	}
	start := p.pos
	for !p.eof() && p.peek() != ':' {
		c := p.peek()
		if !(c >= 'a' && c <= 'z') {
			// POSIX class names are lowercase ASCII letters; anything else means
			// this is not a well-formed bracket expression.
			return nil, p.errorf("invalid POSIX bracket name")
		}
		p.next()
	}
	// Require the closing ":]".
	if p.eof() || p.peek() != ':' || p.pos+1 >= len(p.src) || p.src[p.pos+1] != ']' {
		return nil, p.errorf("premature end of POSIX bracket class")
	}
	name := p.src[start:p.pos]
	p.next() // consume ':'
	p.next() // consume ']'
	ranges, ok := posixClass(name)
	if !ok {
		return nil, p.errorf("invalid POSIX bracket type [:%s:]", name)
	}
	if negate {
		return negateRanges(ranges), nil
	}
	return ranges, nil
}

// posixClass returns the ASCII byte ranges for a POSIX bracket class name,
// reporting whether the name is one of the standard classes. The ranges match
// Onigmo's defaults for the ASCII portion of the byte space (verified against
// MRI); the negated form complements them over the full byte range.
func posixClass(name string) ([]ast.ClassRange, bool) {
	switch name {
	case "alpha":
		return []ast.ClassRange{{Lo: 'A', Hi: 'Z'}, {Lo: 'a', Hi: 'z'}}, true
	case "digit":
		return []ast.ClassRange{{Lo: '0', Hi: '9'}}, true
	case "alnum":
		return []ast.ClassRange{{Lo: '0', Hi: '9'}, {Lo: 'A', Hi: 'Z'}, {Lo: 'a', Hi: 'z'}}, true
	case "upper":
		return []ast.ClassRange{{Lo: 'A', Hi: 'Z'}}, true
	case "lower":
		return []ast.ClassRange{{Lo: 'a', Hi: 'z'}}, true
	case "space":
		return []ast.ClassRange{{Lo: '\t', Hi: '\r'}, {Lo: ' ', Hi: ' '}}, true
	case "blank":
		return []ast.ClassRange{{Lo: '\t', Hi: '\t'}, {Lo: ' ', Hi: ' '}}, true
	case "cntrl":
		return []ast.ClassRange{{Lo: 0, Hi: 0x1f}, {Lo: 0x7f, Hi: 0x7f}}, true
	case "graph":
		return []ast.ClassRange{{Lo: '!', Hi: '~'}}, true
	case "print":
		return []ast.ClassRange{{Lo: ' ', Hi: '~'}}, true
	case "punct":
		return []ast.ClassRange{{Lo: '!', Hi: '/'}, {Lo: ':', Hi: '@'}, {Lo: '[', Hi: '`'}, {Lo: '{', Hi: '~'}}, true
	case "xdigit":
		return []ast.ClassRange{{Lo: '0', Hi: '9'}, {Lo: 'A', Hi: 'F'}, {Lo: 'a', Hi: 'f'}}, true
	case "word":
		return []ast.ClassRange{{Lo: '0', Hi: '9'}, {Lo: 'A', Hi: 'Z'}, {Lo: '_', Hi: '_'}, {Lo: 'a', Hi: 'z'}}, true
	default:
		return nil, false
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
