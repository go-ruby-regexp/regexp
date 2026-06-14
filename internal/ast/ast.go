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

// Literal matches a single byte exactly.
type Literal struct {
	B byte
}

// AnyChar matches any byte except a newline (the dot metacharacter).
type AnyChar struct{}

// ClassRange is a single inclusive byte range inside a character class. A
// single character c is represented as the range c..c.
type ClassRange struct {
	Lo, Hi byte
}

// Class is a character class: a set of byte ranges, optionally negated.
type Class struct {
	Ranges []ClassRange
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
// span into the capture slot identified by Index (1-based).
type Group struct {
	Sub     Node
	Capture bool
	Index   int
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
func (*Empty) isNode()     {}
