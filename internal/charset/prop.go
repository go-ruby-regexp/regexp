// Package charset classifies a single Unicode code point against the property
// names this engine recognises for the \p{…} / \P{…} construct. It is the one
// rune-aware piece of the otherwise byte-oriented engine: the parser uses
// Valid to reject unknown property names at compile time, and the VM uses Match
// to test a decoded rune.
//
// Two families of names are supported, matching the slice of Onigmo/Ruby this
// engine implements:
//
//   - General categories: the one-letter groups L, N, P, S, Z, C and the
//     two-letter letter/number/other subcategories Lu, Ll, Lt, Lm, Lo, Nd, Nl,
//     No, Cf. These map directly onto Go's unicode range tables.
//   - Onigmo POSIX-style aliases: Alpha, Alnum, Digit, Space, Upper, Lower and
//     Word. These follow Ruby's definitions (e.g. Space is the Unicode
//     White_Space property, which is broader than the Z general category;
//     Word is letter | mark | decimal-number | connector-punctuation), which
//     do not always coincide with a single general category.
//
// Names are matched case-sensitively, exactly as Onigmo treats them. Negation
// (\P{…} and \p{^…}) is handled by the caller, not here.
package charset

import "unicode"

// classify maps a recognised property name to its membership predicate. It
// returns nil for an unknown name.
func classify(name string) func(rune) bool {
	switch name {
	// General categories — one-letter groups.
	case "L":
		return unicode.IsLetter
	case "N":
		return unicode.IsNumber
	case "P":
		return unicode.IsPunct
	case "S":
		return unicode.IsSymbol
	case "Z":
		return func(r rune) bool { return unicode.Is(unicode.Z, r) }
	case "C":
		return func(r rune) bool { return unicode.Is(unicode.C, r) }
	case "Cf":
		return func(r rune) bool { return unicode.Is(unicode.Cf, r) }
	// General categories — letter and number subcategories.
	case "Lu":
		return func(r rune) bool { return unicode.Is(unicode.Lu, r) }
	case "Ll":
		return func(r rune) bool { return unicode.Is(unicode.Ll, r) }
	case "Lt":
		return func(r rune) bool { return unicode.Is(unicode.Lt, r) }
	case "Lm":
		return func(r rune) bool { return unicode.Is(unicode.Lm, r) }
	case "Lo":
		return func(r rune) bool { return unicode.Is(unicode.Lo, r) }
	case "Nd":
		return func(r rune) bool { return unicode.Is(unicode.Nd, r) }
	case "Nl":
		return func(r rune) bool { return unicode.Is(unicode.Nl, r) }
	case "No":
		return func(r rune) bool { return unicode.Is(unicode.No, r) }
	// Onigmo POSIX-style aliases.
	case "Alpha":
		return isAlpha
	case "Alnum":
		return func(r rune) bool { return isAlpha(r) || unicode.Is(unicode.Nd, r) }
	case "Digit":
		return func(r rune) bool { return unicode.Is(unicode.Nd, r) }
	case "Space":
		return unicode.IsSpace
	case "Upper":
		return unicode.IsUpper
	case "Lower":
		return unicode.IsLower
	case "Word":
		return isWord
	default:
		return nil
	}
}

// isAlpha is Onigmo's Alpha alias: the Unicode Alphabetic derived property,
// which is letters plus letter-numbers (Nl) plus the Other_Alphabetic
// characters (e.g. some combining marks), but not decimal digits or symbols.
func isAlpha(r rune) bool {
	return unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) || unicode.Is(unicode.Other_Alphabetic, r)
}

// isWord is Onigmo's Word alias: a letter, a mark, a decimal number, or a
// connector punctuation (so the underscore and combining marks are included),
// matching Ruby's \p{Word}.
func isWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.Is(unicode.Nd, r) || unicode.Is(unicode.Pc, r)
}

// Valid reports whether name is a property this engine recognises.
func Valid(name string) bool {
	return classify(name) != nil
}

// Match reports whether rune r is a member of the named property. negate flips
// the result (for \P{…} and \p{^…}). It returns false for an unknown name; the
// parser is expected to have rejected such names via Valid before compilation,
// so this is only a defensive fallback.
func Match(name string, negate bool, r rune) bool {
	pred := classify(name)
	if pred == nil {
		return false
	}
	return pred(r) != negate
}

// FoldEqual reports whether runes a and b are equal under simple (1:1) Unicode
// case folding — that is, whether they belong to the same simple-case-folding
// orbit. The orbit is the cycle Go's unicode.SimpleFold walks (e.g. k → K →
// Kelvin-sign → k, or Σ → ς → σ → Σ), so FoldEqual('k', 0x212A) and
// FoldEqual('É', 'é') are both true. This is the engine's rune-level /i model;
// full/special case folding (multi-character expansions such as ß→"ss" and
// locale-specific rules such as Turkish dotless-i) is deliberately out of scope.
func FoldEqual(a, b rune) bool {
	if a == b {
		return true
	}
	// Walk a's orbit; if b appears, they fold-match. SimpleFold cycles back to
	// the starting rune, so the loop is finite.
	for f := unicode.SimpleFold(a); f != a; f = unicode.SimpleFold(f) {
		if f == b {
			return true
		}
	}
	return false
}

// foldOrbit returns r together with every other rune in its simple-case-folding
// orbit, used to test a code point against a folded character-class range.
func foldOrbit(r rune) []rune {
	orbit := []rune{r}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		orbit = append(orbit, f)
	}
	return orbit
}

// FoldRangeContains reports whether code point r is in the inclusive range
// [lo,hi] under simple case folding: r matches if r, or any rune in r's
// simple-case-folding orbit, lies within the range. So FoldRangeContains tests
// "A" against the range "a".."z" (folding to lowercase) and the Kelvin sign
// against the same range (its orbit includes ASCII "k"). The class machinery uses
// this so (?i)[a-z] and (?i)[α-ω] match their opposite-case members.
func FoldRangeContains(r, lo, hi rune) bool {
	for _, f := range foldOrbit(r) {
		if f >= lo && f <= hi {
			return true
		}
	}
	return false
}
