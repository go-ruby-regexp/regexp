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

// Literal matches a single byte exactly. When Fold is set (case-insensitive
// mode, /i) and B is an ASCII letter, the byte of the opposite case matches as
// well.
type Literal struct {
	B    byte
	Fold bool
}

// AnyChar matches any byte except a newline (the dot metacharacter).
type AnyChar struct{}

// ClassRange is a single inclusive byte range inside a character class. A
// single character c is represented as the range c..c.
type ClassRange struct {
	Lo, Hi byte
}

// Class is a character class: a set of byte ranges, optionally negated. When
// Fold is set (case-insensitive mode, /i), membership is tested against both an
// input byte and its ASCII-case counterpart before Negate is applied.
type Class struct {
	Ranges []ClassRange
	Negate bool
	Fold   bool
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

// Star is a greedy quantifier with bounds. Min is the required count; Max is
// the maximum count, or -1 for unbounded.
type Star struct {
	Sub      Node
	Min, Max int
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

func (*Literal) isNode()   {}
func (*AnyChar) isNode()   {}
func (*Class) isNode()     {}
func (*Anchor) isNode()    {}
func (*Concat) isNode()    {}
func (*Alternate) isNode() {}
func (*Star) isNode()      {}
func (*Group) isNode()     {}
func (*Backref) isNode()   {}
func (*Look) isNode()      {}
func (*Empty) isNode()     {}
