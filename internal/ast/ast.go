// Package ast holds the abstract syntax tree node types for the regular
// expression grammar. It is intentionally statement-free — only type
// definitions and empty interface-witness methods — so the coverage gate
// excludes it (the cover tool cannot attribute statement-free code).
package ast

// Node is an AST node. The concrete node types below all implement it; the
// marker method exists only so the set of node types is closed within this
// module.
type Node interface {
	isNode()
}

// Literal matches a single byte exactly. It is the byte-oriented literal used
// for every character outside case-insensitive mode, and for an under-/i
// character that has no Unicode case partner (an ASCII non-letter, or a code
// point Unicode does not case-fold) — folding such a character is a no-op, so no
// rune awareness is needed. An under-/i character that does have a case partner
// is emitted as a rune-aware FoldLiteral instead, so e.g. a Kelvin sign matches
// "k".
type Literal struct {
	B byte
}

// FoldLiteral matches a single Unicode code point case-insensitively (/i). It is
// rune-aware, like UnicodeProp: the VM decodes one UTF-8 code point at the cursor
// and accepts it when it is in the same simple-case-folding orbit as R (Go's
// unicode.SimpleFold), advancing by that code point's byte length. So /É/i
// matches "é", and /k/i matches the Kelvin sign U+212A, exactly as Onigmo/Ruby's
// simple (1:1) folding does. Full/special case folding (ß→"ss", locale rules) is
// deliberately out of scope, matching the engine's documented simple-folding
// boundary.
type FoldLiteral struct {
	R rune
}

// AnyChar matches any byte (the dot metacharacter). By default a newline is
// excluded; when DotAll is set (Ruby's /m option, set inline by (?m)) the dot
// matches a newline as well.
type AnyChar struct {
	DotAll bool
}

// ClassRange is a single inclusive byte range inside a character class. A
// single character c is represented as the range c..c.
type ClassRange struct {
	Lo, Hi byte
}

// RuneClassRange is a single inclusive code-point range inside a character
// class. It is produced when a class member is a multi-byte code point whose
// bounds do not fit in a byte: a literal multi-byte member or range in UTF8 mode
// (e.g. [é] or [à-ï]), a folded class's non-ASCII member under /i (e.g. (?i)[é]
// or (?i)[α-ω]), or \R's multi-byte linebreak set. A single code point c is the
// range c..c.
type RuneClassRange struct {
	Lo, Hi rune
}

// PropRef is a reference to a Unicode property inside a character class (a
// \p{name} or \P{name} member). Negate is the member-local negation from the
// \P / \p{^…} form; it is independent of the enclosing class's own Negate.
type PropRef struct {
	Name   string
	Negate bool
}

// Class is a character class: a set of byte ranges plus zero or more Unicode
// property references, optionally negated as a whole. When Fold is set
// (case-insensitive mode, /i), the class becomes rune-aware and membership is
// tested by simple case folding: a decoded input code point matches when it, or
// any code point in its simple-case-folding orbit (unicode.SimpleFold), falls in
// a range or satisfies a property. So (?i)[a-z] matches "A" and the Kelvin sign,
// and (?i)[α-ω] matches an uppercase Greek letter. Negate is applied last.
//
// A class is byte-oriented (its ranges test a single input byte) unless it
// contains a property reference or Fold is set, in which case the whole class
// becomes rune-aware: the VM decodes one UTF-8 code point and tests it against
// both the ranges (whose bounds, produced only from byte syntax, are all ASCII)
// and the properties. This is the same rune/byte boundary the standalone
// UnicodeProp node draws.
type Class struct {
	Ranges     []ClassRange
	RuneRanges []RuneClassRange
	Props      []PropRef
	Negate     bool
	Fold       bool
}

// UnicodeProp matches a single Unicode code point that belongs to (or, when
// Negate is set via \P{…} or \p{^…}, does not belong to) the named property.
// It is the engine's one rune-aware atom: the VM decodes one UTF-8 code point
// at the cursor and advances by that code point's byte length, whereas the
// byte-oriented atoms (Literal, AnyChar, Class without properties) consume a
// single byte.
type UnicodeProp struct {
	Name   string
	Negate bool
}

// AnchorKind enumerates the zero-width anchors supported in Phase 0.
type AnchorKind int

const (
	// AnchorBeginText matches at the start of the input (\A).
	AnchorBeginText AnchorKind = iota
	// AnchorEndText matches at the very end of the input (\z).
	AnchorEndText
	// AnchorEndTextOptNL matches at the end of the input, optionally before a
	// single trailing newline (\Z).
	AnchorEndTextOptNL
	// AnchorBeginLine matches at the start of the input or just after a
	// newline (^).
	AnchorBeginLine
	// AnchorEndLine matches at the end of the input or just before a newline
	// ($).
	AnchorEndLine
	// AnchorPrevMatch matches only at the position where the previous match or
	// the scan start began (\G).
	AnchorPrevMatch
	// AnchorWordBoundary matches at a position between a word character and a
	// non-word character or a string edge (\b). A word character is the same
	// notion Onigmo/MRI use for \b: in UTF8 mode a Unicode word code point
	// (letter, mark, decimal number, or connector punctuation — \p{Word}), and in
	// ASCII8BIT (/n) mode an ASCII word byte ([0-9A-Za-z_]). Note this is MRI's
	// own \b definition, which is Unicode-aware in UTF8 mode even though \w is
	// ASCII-only there — \b deliberately mirrors the oracle, not \w.
	AnchorWordBoundary
	// AnchorNonWordBoundary matches at any position that is NOT a word boundary
	// (\B), using the same word-character notion as AnchorWordBoundary.
	AnchorNonWordBoundary
)

// Anchor is a zero-width assertion.
type Anchor struct {
	Kind AnchorKind
}

// Concat matches its sub-expressions in sequence.
type Concat struct {
	Subs []Node
}

// Alternate matches any one of its alternatives, preferring the earliest
// (leftmost-first, as in Ruby/Onigmo).
type Alternate struct {
	Subs []Node
}

// Star is a quantifier with bounds. Min is the required count; Max is the
// maximum count, or -1 for unbounded. Greedy selects the matching preference:
// a greedy quantifier (the default, e.g. a*) tries the longest repetition first
// and gives back on backtracking, whereas a non-greedy/lazy one (a*?, written
// with a trailing '?') tries the shortest first and takes more only when forced.
// Both explore the same set of matches; they differ only in order, so under
// backtracking they can yield different leftmost-first results.
type Star struct {
	Sub      Node
	Min, Max int
	Greedy   bool
}

// Group is a parenthesised sub-expression. When Capture is true it records its
// span into the capture slot identified by Index (1-based). Name is set for a
// named capture (?<name>...), otherwise empty.
type Group struct {
	Sub     Node
	Capture bool
	Index   int
	Name    string
}

// Backref matches the same text previously captured by group Index
// (\1..\9, or \k<name>). When Fold is set (case-insensitive mode, /i), the
// comparison is ASCII case-insensitive.
type Backref struct {
	Index int
	Fold  bool
}

// Call is a subexpression call \g<…>. It re-runs the referenced group's
// sub-pattern at the current position and re-captures into that group's slot,
// exactly as Onigmo/Ruby does (the most recent execution of a group wins).
// Index is the absolute, 1-based group number the textual reference resolved to
// (0 means the whole pattern, \g<0>, i.e. recurse the entire regex). The various
// reference spellings — \g<name>, \g<n>, relative \g<+n>/\g<-n>, and \g<0> — are
// all resolved to this absolute Index in a post-parse pass, so a forward call
// (to a group defined later in the pattern) works. Calls may be recursive; the
// VM bounds recursion depth and total steps so a pathological grammar terminates.
type Call struct {
	Index int
}

// Atomic is an atomic (possessive) group (?>…). Its sub-pattern is matched once
// and then committed: every backtrack point created while matching Sub is
// discarded the moment Sub succeeds, so the engine never re-tries an alternate
// sub-match or a shorter repetition to make the rest of the pattern succeed. It
// is the non-backtrackable barrier shared with the possessive quantifiers: the
// parser lowers X*+ / X++ / X?+ / X{m,n}+ to an Atomic wrapping the equivalent
// greedy quantifier (X*+ is exactly (?>X*)), so both forms run on one mechanism.
// Captures made inside an atomic group persist (their most recent binding wins),
// exactly as in Onigmo/Ruby.
type Atomic struct {
	Sub Node
}

// Look is a zero-width lookaround assertion. Behind selects lookbehind over
// lookahead, and Negate selects the negative form. Sub is the sub-pattern run
// at (lookahead) or ending at (lookbehind) the current position; the outer
// position is never advanced. For lookbehind, Min and Max bound the number of
// bytes Sub can match (the parser rejects unbounded-width lookbehind).
type Look struct {
	Sub      Node
	Behind   bool
	Negate   bool
	Min, Max int
}

// Empty matches the empty string.
type Empty struct{}

func (*Literal) isNode()     {}
func (*FoldLiteral) isNode() {}
func (*AnyChar) isNode()     {}
func (*Class) isNode()       {}
func (*UnicodeProp) isNode() {}
func (*Anchor) isNode()      {}
func (*Concat) isNode()      {}
func (*Alternate) isNode()   {}
func (*Star) isNode()        {}
func (*Group) isNode()       {}
func (*Backref) isNode()     {}
func (*Call) isNode()        {}
func (*Atomic) isNode()      {}
func (*Look) isNode()        {}
func (*Empty) isNode()       {}
