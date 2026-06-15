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
// also adds the inline options (?flags) (a set directive scoped to the rest of
// the enclosing group), (?flags:...) (a scoped group), and the (?-flags) /
// (?f-f:...) turn-off forms, exactly as in Onigmo/Ruby. Three option letters are
// recognised: i (case-insensitive matching — a folded letter literal is lowered
// to a rune-aware FoldLiteral via unicode.SimpleFold, character classes fold
// rune-aware, and backreferences fold ASCII-only), m (dot-all: the dot also
// matches a newline), and x (extended/free-spacing: unescaped whitespace and #
// comments in the pattern are ignored, except inside a character class). Any
// other flag letter is reported as a syntax error. Phase 3 also adds the
// Unicode property escapes \p{name} / \P{name} (with the in-brace negation
// \p{^name}), both as a standalone atom and as a character-class member; the
// recognised names are validated by the sibling charset package.
//
// Lookbehind, as in Onigmo/Ruby, requires each alternative of its body to have
// a constant byte width (different alternatives may differ, e.g. (?<=ab|c));
// bodies whose width can vary — unbounded or {m,n} (m != n) quantifiers, and
// backreferences — are rejected at parse time.
package syntax

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/go-onigmo/regexp/internal/ast"
	"github.com/go-onigmo/regexp/internal/charset"
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

// flags holds the inline option state in effect at a point in the parse. Each
// option is toggled by (?flags) / (?-flags) and scoped by (?flags:...), exactly
// as in Onigmo/Ruby. Three options are modelled: i (case-insensitive), m
// (dot-all: the dot matches a newline) and x (extended/free-spacing).
type flags struct {
	fold     bool // case-insensitive matching (i)
	dotAll   bool // the dot matches '\n' too (m)
	extended bool // extended mode: ignore unescaped whitespace and # comments (x)
}

// pendingCall records a \g<…> subexpression call whose target cannot be resolved
// to an absolute group index at the point it is parsed — a \g<name> or \g<n>
// reference may name a group defined later in the pattern. The relative forms
// \g<+n>/\g<-n> are resolved immediately (they are positional) and so never
// become a pendingCall. After the whole pattern is parsed, resolveCalls walks
// these and fills each node's Index, erroring on an undefined name or number.
type pendingCall struct {
	node *ast.Call
	name string // \g<name>: empty when the reference was numeric
	num  int    // \g<n>: the absolute group number; used only when name == ""
}

// parser is a single-use recursive-descent parser over the pattern bytes.
type parser struct {
	src        string
	pos        int
	ncap       int
	names      map[string]int
	maxBackref int           // largest numeric backreference seen, validated after parsing
	calls      []pendingCall // \g<name>/\g<n> references awaiting post-parse resolution
	flags      flags         // inline option state at the cursor (e.g. /i via (?i))
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
	if err := p.resolveCalls(); err != nil {
		return Result{}, err
	}
	return Result{Root: node, NumCapture: p.ncap, Names: p.names}, nil
}

// resolveCalls fills in the absolute group Index of every \g<name>/\g<n>
// subexpression call that could not be resolved while parsing (a forward
// reference). A \g<name> resolves through the named-group map; a \g<n> must name
// an existing group (1..ncap) — \g<0> (the whole pattern) is always valid and is
// stored directly by the parser, so it never appears here. An unknown name or an
// out-of-range number is a syntax error, matching Onigmo/Ruby.
func (p *parser) resolveCalls() error {
	for _, c := range p.calls {
		if c.name != "" {
			idx, ok := p.names[c.name]
			if !ok {
				return p.errorf("undefined group name <%s> for \\g", c.name)
			}
			c.node.Index = idx
			continue
		}
		if c.num < 1 || c.num > p.ncap {
			return p.errorf("undefined group <%d> for \\g", c.num)
		}
		c.node.Index = c.num
	}
	return nil
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSyntax, fmt.Sprintf(format, args...))
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

// skipExtended consumes the pattern bytes that extended/free-spacing mode (the x
// option) ignores: the unescaped whitespace bytes Onigmo treats as insignificant
// — space, tab, newline, form feed and carriage return (note: not the vertical
// tab) — and '#' comments that run to the end of the line (or the pattern). It is
// a no-op unless x is in effect, and it is never called inside a character class,
// where those bytes are literal. Onigmo has one further idiosyncrasy this engine
// does not reproduce: a '#' comment glued directly to an atom and immediately
// followed by a quantifier (e.g. /(?x)a#c\n+/) is a syntax error there but is
// accepted here as a+ after a comment; whitespace anywhere around the comment
// makes Onigmo accept it too, so the divergence is confined to that one shape.
func (p *parser) skipExtended() {
	if !p.flags.extended {
		return
	}
	for !p.eof() {
		switch p.peek() {
		case ' ', '\t', '\n', '\f', '\r':
			p.next()
		case '#':
			for !p.eof() && p.peek() != '\n' {
				p.next()
			}
		default:
			return
		}
	}
}

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
	p.skipExtended()
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
		// Skip insignificant whitespace/comments before the next atom so the loop
		// condition sees the real next token (or the branch/group terminator). A
		// (?x) directive in this branch enables this from here on.
		p.skipExtended()
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

// quantMod is the matching-preference modifier that may trail a quantifier: the
// default greedy form, the non-greedy/lazy form (a trailing '?'), or the
// possessive form (a trailing '+').
type quantMod int

const (
	modGreedy     quantMod = iota // longest first, gives back on backtrack (a*)
	modLazy                       // shortest first, takes more only when forced (a*?)
	modPossessive                 // greedy then committed: no give-back at all (a*+)
)

// parseQuantifier applies trailing quantifiers to atom. A single quantifier is
// *, +, ?, or {m,n}; Onigmo also accepts a quantifier stacked on a quantifier
// (e.g. a**, a+++, a{2}*), so after building one this loops to apply any further
// quantifier to the result. In extended mode the insignificant
// whitespace/comments between the atom and a following quantifier are ignored,
// so e.g. /(?x)a *$/ applies * to a.
func (p *parser) parseQuantifier(atom ast.Node) (ast.Node, error) {
	for {
		p.skipExtended()
		if p.eof() {
			return atom, nil
		}
		var next ast.Node
		var err error
		switch p.peek() {
		case '*':
			p.next()
			next = p.quantify(atom, 0, -1)
		case '+':
			p.next()
			next = p.quantify(atom, 1, -1)
		case '?':
			p.next()
			next = p.quantify(atom, 0, 1)
		case '{':
			var matched bool
			next, matched, err = p.parseBrace(atom)
			if err != nil {
				return nil, err
			}
			if !matched {
				// The '{' was not a valid repetition: it is a literal brace, a fresh
				// term that does not bind to atom. parseBrace left the cursor at the
				// '{' so the outer parseConcat re-reads it as a literal; the stacking
				// loop ends, returning what was quantified so far.
				return atom, nil
			}
		default:
			return atom, nil
		}
		atom = next
	}
}

// quantify builds the node for one of the bare quantifiers * + ? over atom with
// bounds [min,max], reading its trailing greedy/lazy/possessive modifier. The
// greedy and lazy forms are a single Star; the possessive form (a*+, a++, a?+)
// is lowered to an atomic group wrapping the equivalent greedy quantifier — a*+
// is exactly (?>a*) — so the possessives and (?>…) share one VM mechanism.
//
// The possessive modifier is read only for these single-character quantifiers.
// A '+' after a {m,n} brace is, in Onigmo, a *stacked* greedy quantifier on the
// braced repeat (it warns "redundant nested repeat operator" and the repeat
// still gives back), not a possessive; so parseBrace deliberately does not read
// a modifier and the trailing '+' is picked up by parseQuantifier's stacking
// loop instead, matching MRI exactly.
func (p *parser) quantify(atom ast.Node, min, max int) ast.Node {
	switch p.quantMod() {
	case modLazy:
		return &ast.Star{Sub: atom, Min: min, Max: max, Greedy: false}
	case modPossessive:
		return &ast.Atomic{Sub: &ast.Star{Sub: atom, Min: min, Max: max, Greedy: true}}
	default:
		return &ast.Star{Sub: atom, Min: min, Max: max, Greedy: true}
	}
}

// quantMod reads the optional modifier that may directly follow a *, +, or ?
// quantifier: a trailing '?' makes it non-greedy (lazy: a*?, a+?, a??) and a
// trailing '+' makes it possessive (a*+, a++, a?+); with no modifier the
// quantifier is greedy. The modifier byte is consumed only when present. In
// extended mode insignificant whitespace between the quantifier and the modifier
// is skipped first, as Onigmo does.
func (p *parser) quantMod() quantMod {
	p.skipExtended()
	if p.eof() {
		return modGreedy
	}
	switch p.peek() {
	case '?':
		p.next()
		return modLazy
	case '+':
		p.next()
		return modPossessive
	default:
		return modGreedy
	}
}

// parseBrace parses a {m}, {m,}, or {m,n} repetition over atom. A '{' that is
// not a valid repetition is treated as a literal brace, matching Ruby's
// behaviour; matched reports whether a real repetition was parsed (false means
// the returned node is a literal '{' that does not bind to atom).
//
// Only the lazy '?' modifier is read here ({m,n}? is non-greedy). A trailing '+'
// is NOT consumed as a possessive: in Onigmo a '+' after a brace is a stacked
// greedy quantifier on the braced repeat — it warns "redundant nested repeat
// operator" and the repeat still gives back — so it is left for parseQuantifier's
// stacking loop to apply, matching MRI exactly (e.g. a{2,3}+a matches "aaa").
func (p *parser) parseBrace(atom ast.Node) (ast.Node, bool, error) {
	start := p.pos
	p.next() // consume '{'
	min, okMin := p.parseInt()
	if !okMin {
		// Not a count: treat '{' as a literal. Leave the cursor at the '{' so the
		// outer parser re-reads it as a literal term.
		p.pos = start
		return nil, false, nil
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
				return nil, false, nil
			}
			max = m
		}
	}
	if p.eof() || p.peek() != '}' {
		p.pos = start
		return nil, false, nil
	}
	p.next() // consume '}'
	if max != -1 && max < min {
		return nil, false, p.errorf("invalid repetition range {%d,%d}", min, max)
	}
	greedy := true
	p.skipExtended()
	if !p.eof() && p.peek() == '?' {
		p.next()
		greedy = false
	}
	return &ast.Star{Sub: atom, Min: min, Max: max, Greedy: greedy}, true, nil
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
		return &ast.AnyChar{DotAll: p.flags.dotAll}, nil
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
		return p.parseLiteralRune(), nil
	}
}

// parseLiteralRune consumes the next literal character and builds its atom. When
// case-insensitive mode (/i) is in effect it decodes a whole UTF-8 code point and,
// if that code point has a non-trivial simple-case-folding orbit (every ASCII
// letter, and many non-ASCII letters), emits a rune-aware FoldLiteral so that a
// Unicode case partner matches too — e.g. /É/i matches "é" and /k/i matches the
// Kelvin sign. A code point with no fold partner (an ASCII non-letter, or a
// letter Unicode does not case-fold) needs no rune awareness, so its leading byte
// is emitted as a plain byte Literal exactly as before; the remaining
// continuation bytes (if any) are consumed as their own byte literals on later
// iterations, which is byte-identical to the input. Outside /i, every character is
// a byte Literal, keeping the engine byte-oriented.
func (p *parser) parseLiteralRune() ast.Node {
	if !p.flags.fold {
		return &ast.Literal{B: p.next()}
	}
	r, size := utf8.DecodeRuneInString(p.src[p.pos:])
	if r == utf8.RuneError && size <= 1 {
		// An invalid UTF-8 lead byte (or a lone byte) cannot be a foldable code
		// point; treat it as a single opaque byte, matching the byte-oriented core.
		return &ast.Literal{B: p.next()}
	}
	if unicode.SimpleFold(r) == r {
		// No case partner (an ASCII non-letter, or a letter Unicode does not fold):
		// rune-awareness would add nothing, so emit the leading byte as a plain
		// literal and let any continuation bytes follow on later iterations — which
		// is byte-identical to matching the whole code point.
		return &ast.Literal{B: p.next()}
	}
	p.pos += size
	return &ast.FoldLiteral{R: r}
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
		case !p.eof() && p.peek() == '>':
			p.next() // consume '>'
			return p.parseAtomic()
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
		case !p.eof() && (p.peek() == 'i' || p.peek() == 'm' || p.peek() == 'x' || p.peek() == '-'):
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
// The i (case-insensitive), m (dot-all) and x (extended) flags are supported;
// any other flag letter is a syntax error. The optional '-' introduces the
// negated (turned-off) flags.
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
	applied.apply(on, off)
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

// optSet records which inline option letters appeared in one run of an inline
// option construct.
type optSet struct {
	fold, dotAll, extended bool
}

// add records flag letter c into the set, reporting whether it was a recognised
// option letter.
func (s *optSet) add(c byte) bool {
	switch c {
	case 'i':
		s.fold = true
	case 'm':
		s.dotAll = true
	case 'x':
		s.extended = true
	default:
		return false
	}
	return true
}

// apply turns the options in on on and the options in off off, with off winning
// (Ruby permits both in one construct, e.g. (?i-i:...), the trailing -i winning).
func (f *flags) apply(on, off optSet) {
	if on.fold {
		f.fold = true
	}
	if on.dotAll {
		f.dotAll = true
	}
	if on.extended {
		f.extended = true
	}
	if off.fold {
		f.fold = false
	}
	if off.dotAll {
		f.dotAll = false
	}
	if off.extended {
		f.extended = false
	}
}

// parseFlagLetters reads the flag specification of an inline option construct: a
// run of supported flag letters, then an optional '-' followed by another run of
// letters to turn off. It reports which options were switched on and which were
// switched off (Ruby permits both, e.g. (?i-i:...), the later -i winning).
func (p *parser) parseFlagLetters() (on, off optSet, err error) {
	for !p.eof() && p.peek() != '-' && p.peek() != ':' && p.peek() != ')' {
		if !on.add(p.next()) {
			return optSet{}, optSet{}, p.errorf("unsupported inline option flag")
		}
	}
	if !p.eof() && p.peek() == '-' {
		p.next()
		for !p.eof() && p.peek() != ':' && p.peek() != ')' {
			if !off.add(p.next()) {
				return optSet{}, optSet{}, p.errorf("unsupported inline option flag")
			}
		}
	}
	return on, off, nil
}

// parseAtomic parses the body of an atomic (possessive) group (?>…) whose "(?>"
// has already been consumed, up to and including the closing ')'. The group is
// non-capturing; inline options set inside it are scoped to it, like any other
// group. The resulting ast.Atomic compiles to the atomic-cut barrier (every
// backtrack point its body creates is discarded once the body matches), which is
// also the lowering target of the possessive quantifiers.
func (p *parser) parseAtomic() (ast.Node, error) {
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
	return &ast.Atomic{Sub: sub}, nil
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
	case *ast.Call:
		// A subexpression call re-runs another group's sub-pattern, whose matched
		// width is not a compile-time constant in general (and is formally
		// undecidable once the call is recursive). Like a backreference, it is
		// therefore disqualified from a fixed-width lookbehind body.
		return false
	case *ast.Atomic:
		// Onigmo/Ruby rejects an atomic group — and therefore any possessive
		// quantifier, which lowers to one — anywhere in a lookbehind body ("invalid
		// pattern in look-behind"), regardless of whether its sub-pattern is itself
		// fixed-width. Mirror that by disqualifying it here.
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
	case *ast.UnicodeProp:
		// A property matches one code point, whose UTF-8 byte width varies, so a
		// lookbehind whose width must be a compile-time constant cannot contain it.
		return false
	case *ast.FoldLiteral:
		// A folded code point can match case partners of different UTF-8 width
		// (e.g. /k/i matches a 1-byte "K" or the 3-byte Kelvin sign), so its byte
		// width is not a compile-time constant.
		return false
	case *ast.Class:
		// A rune-aware class — one carrying a \p{…} member or folded under /i —
		// matches a code point of variable byte width; a byte-oriented class is one
		// byte.
		return len(t.Props) == 0 && !t.Fold
	}
	// Literal, AnyChar, byte Class, Anchor, Look, Empty, and containers whose
	// parts all checked out are constant-width.
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
	case 'p', 'P':
		name, negate, err := p.parseProp(b == 'P')
		if err != nil {
			return nil, err
		}
		return &ast.UnicodeProp{Name: name, Negate: negate}, nil
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
	case 'g':
		return p.parseCall()
	case '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
		return &ast.Literal{B: b}, nil
	case ' ', '\t', '\n', '\f', '\r', '#':
		// The whitespace bytes that extended mode skips, and '#', are literal when
		// backslash-escaped — so /(?x)a\ b/ matches "a b". Onigmo accepts these
		// escapes whether or not x is in effect, matching Ruby.
		return &ast.Literal{B: b}, nil
	default:
		return nil, p.errorf("unsupported escape \\%c", b)
	}
}

// parseProp parses the body of a Unicode property escape whose introducing
// '\p' or '\P' has already been consumed: a brace-delimited property name
// {name}, with an optional leading '^' inside the braces for negation. baseNeg
// is true for the '\P' (negated) form; an inner '^' toggles it again, exactly
// as in Onigmo/Ruby (so \P{^L} is the same as \p{L}). The one-letter forms
// \pL / \PL are not accepted — Onigmo rejects them too — so a '{' is required.
// The property name must be one charset recognises, else it is a syntax error.
func (p *parser) parseProp(baseNeg bool) (name string, negate bool, err error) {
	if p.eof() || p.peek() != '{' {
		return "", false, p.errorf("expected { after \\p")
	}
	p.next() // consume '{'
	negate = baseNeg
	if !p.eof() && p.peek() == '^' {
		p.next()
		negate = !negate
	}
	start := p.pos
	for !p.eof() && p.peek() != '}' {
		p.next()
	}
	if p.eof() {
		return "", false, p.errorf("missing } in \\p{...}")
	}
	name = p.src[start:p.pos]
	p.next() // consume '}'
	if name == "" {
		return "", false, p.errorf("empty property name in \\p{}")
	}
	if !charset.Valid(name) {
		return "", false, p.errorf("invalid character property name {%s}", name)
	}
	return name, negate, nil
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

// parseCall parses a \g<…> subexpression call, whose introducing 'g' has just
// been consumed. The target may be spelled four ways, all of which Onigmo/Ruby
// accept and which resolve to an absolute, 1-based group index (0 = the whole
// pattern):
//
//	\g<name>   a named group; resolved through the named-group map, possibly a
//	           forward reference (deferred to resolveCalls).
//	\g<n>      an absolute group number; \g<0> recurses the whole pattern and is
//	           valid immediately, any other number is validated in resolveCalls.
//	\g<+n>     a relative forward reference: the n-th group whose '(' comes after
//	           this token. Positional, so resolved here from the current count.
//	\g<-n>     a relative backward reference: the n-th group whose '(' came
//	           before this token (\g<-1> is the nearest preceding group).
//
// Onigmo also accepts the quote delimiters \g'…'; both are handled. Matching
// Onigmo, a leading-zero number such as \g<01> and the degenerate \g<+0>/\g<-0>
// are rejected.
func (p *parser) parseCall() (ast.Node, error) {
	if p.eof() || (p.peek() != '<' && p.peek() != '\'') {
		return nil, p.errorf("expected <name> or <n> after \\g")
	}
	open := p.next()
	close := byte('>')
	if open == '\'' {
		close = '\''
	}
	start := p.pos
	for !p.eof() && p.peek() != close {
		p.next()
	}
	if p.eof() {
		return nil, p.errorf("missing %c in \\g", close)
	}
	body := p.src[start:p.pos]
	p.next() // consume the closing delimiter
	if body == "" {
		return nil, p.errorf("empty \\g name")
	}
	node := &ast.Call{}
	switch {
	case body[0] == '+' || body[0] == '-':
		n, ok := parseDecimal(body[1:])
		if !ok || n == 0 {
			return nil, p.errorf("invalid relative \\g reference <%s>", body)
		}
		// Relative references are positional: +n counts groups opened after this
		// token (the next group is ncap+1), -n counts groups already opened (the
		// nearest preceding is ncap). resolveCalls then range-checks the result.
		var idx int
		if body[0] == '+' {
			idx = p.ncap + n
		} else {
			idx = p.ncap + 1 - n
		}
		p.calls = append(p.calls, pendingCall{node: node, num: idx})
	case body[0] >= '0' && body[0] <= '9':
		n, ok := parseDecimal(body)
		if !ok || (len(body) > 1 && body[0] == '0') {
			// A leading-zero number (\g<01>) is rejected by Onigmo.
			return nil, p.errorf("invalid \\g number <%s>", body)
		}
		if n == 0 {
			// \g<0> recurses the whole pattern; it is always valid.
			node.Index = 0
		} else {
			p.calls = append(p.calls, pendingCall{node: node, num: n})
		}
	default:
		if !validGroupName(body) {
			return nil, p.errorf("invalid \\g name <%s>", body)
		}
		p.calls = append(p.calls, pendingCall{node: node, name: body})
	}
	return node, nil
}

// parseDecimal parses s as a non-negative decimal integer with no sign and no
// other characters, reporting success. It backs \g<n>/\g<+n>/\g<-n> where the
// reference body was already isolated between the delimiters.
func parseDecimal(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// validGroupName reports whether s is a syntactically valid group name (the same
// character set parseGroupName accepts for a (?<name>…) definition): one or more
// of letters, digits and underscore. It guards a \g<name> reference body.
func validGroupName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return s != ""
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
		// Under case-insensitive mode the class is rune-aware, so a member that is
		// a multi-byte UTF-8 code point (e.g. (?i)[é] or a bound of (?i)[α-ω]) is
		// decoded as a whole rune into a RuneRange rather than its raw bytes. A '\'
		// escape is left to parseClassItem below, which yields only ASCII
		// bytes/ranges/properties. Outside /i this branch is skipped and the class
		// stays byte-oriented, matching the engine's rune/byte boundary.
		if cls.Fold && p.peek() >= 0x80 {
			if err := p.parseFoldRuneMember(cls); err != nil {
				return nil, err
			}
			continue
		}
		lo, sub, prop, err := p.parseClassItem()
		if err != nil {
			return nil, err
		}
		if prop != nil {
			// A \p{…} / \P{…} member makes the class rune-aware (see ast.Class).
			cls.Props = append(cls.Props, *prop)
			continue
		}
		if sub != nil {
			// A class escape such as \d expands to ranges directly.
			cls.Ranges = append(cls.Ranges, sub...)
			continue
		}
		// Possible range lo-hi.
		if !p.eof() && p.peek() == '-' && p.pos+1 < len(p.src) && p.src[p.pos+1] != ']' {
			p.next() // consume '-'
			// Under /i the high bound may be a multi-byte code point even when the
			// low bound was ASCII (e.g. (?i)[a-é]); parseClassRangeBound decodes it
			// whole and the range becomes a rune range when either bound exceeds the
			// byte space. Outside /i the high bound is a single byte as before.
			if cls.Fold {
				hi, err := p.parseClassRangeBound()
				if err != nil {
					return nil, err
				}
				if hi < rune(lo) {
					return nil, p.errorf("invalid range %q-%q in character class", lo, hi)
				}
				cls.RuneRanges = append(cls.RuneRanges, ast.RuneClassRange{Lo: rune(lo), Hi: hi})
				continue
			}
			hi, subHi, propHi, err := p.parseClassItem()
			if err != nil {
				return nil, err
			}
			if subHi != nil || propHi != nil {
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

// parseFoldRuneMember parses one multi-byte code-point member (or the low bound
// of a code-point range) inside a case-insensitive class, whose lead byte is at
// the cursor. It decodes the whole code point, and if a '-' (not the closing
// "-]") follows, the high bound — which may itself be a multi-byte rune or an
// ASCII byte — completing a RuneRange. A single member becomes the degenerate
// range lo..lo. The decoded high bound must not be below the low bound.
func (p *parser) parseFoldRuneMember(cls *ast.Class) error {
	lo, size := utf8.DecodeRuneInString(p.src[p.pos:])
	p.pos += size
	if !p.eof() && p.peek() == '-' && p.pos+1 < len(p.src) && p.src[p.pos+1] != ']' {
		p.next() // consume '-'
		hi, err := p.parseClassRangeBound()
		if err != nil {
			return err
		}
		if hi < lo {
			return p.errorf("invalid range %q-%q in character class", lo, hi)
		}
		cls.RuneRanges = append(cls.RuneRanges, ast.RuneClassRange{Lo: lo, Hi: hi})
		return nil
	}
	cls.RuneRanges = append(cls.RuneRanges, ast.RuneClassRange{Lo: lo, Hi: lo})
	return nil
}

// parseClassRangeBound reads the high bound of a code-point range in a
// case-insensitive class. The bound is a single code point: a multi-byte rune is
// decoded whole; otherwise a single byte (which may be a '\'-escaped ASCII
// character such as \n) is taken. A class escape (\d) or property (\p{…}) is not
// a valid range bound, mirroring the byte-oriented parser's rejection.
func (p *parser) parseClassRangeBound() (rune, error) {
	if p.peek() >= 0x80 {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		p.pos += size
		return r, nil
	}
	hi, sub, prop, err := p.parseClassItem()
	if err != nil {
		return 0, err
	}
	if sub != nil || prop != nil {
		return 0, p.errorf("invalid range end in character class")
	}
	return rune(hi), nil
}

// parseClassItem parses one member of a character class: a single byte
// (returned as lo with sub and prop nil), a class escape such as \d (returned
// as a slice of ranges), or a Unicode property escape \p{…}/\P{…} (returned as
// prop). It assumes at least one byte remains.
func (p *parser) parseClassItem() (byte, []ast.ClassRange, *ast.PropRef, error) {
	b := p.next()
	if b != '\\' {
		return b, nil, nil, nil
	}
	if p.eof() {
		return 0, nil, nil, p.errorf("trailing backslash in character class")
	}
	e := p.next()
	switch e {
	case 'd':
		return 0, digitRanges(), nil, nil
	case 'w':
		return 0, wordRanges(), nil, nil
	case 's':
		return 0, spaceRanges(), nil, nil
	case 'D':
		return 0, negateRanges(digitRanges()), nil, nil
	case 'W':
		return 0, negateRanges(wordRanges()), nil, nil
	case 'S':
		return 0, negateRanges(spaceRanges()), nil, nil
	case 'p', 'P':
		name, negate, err := p.parseProp(e == 'P')
		if err != nil {
			return 0, nil, nil, err
		}
		return 0, nil, &ast.PropRef{Name: name, Negate: negate}, nil
	case 'n':
		return '\n', nil, nil, nil
	case 't':
		return '\t', nil, nil, nil
	case 'r':
		return '\r', nil, nil, nil
	case '\\', ']', '[', '^', '-':
		return e, nil, nil, nil
	default:
		return 0, nil, nil, p.errorf("unsupported escape \\%c in character class", e)
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
