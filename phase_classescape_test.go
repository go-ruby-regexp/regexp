// Copyright (c) the go-ruby-regexp/regexp authors
//
// SPDX-License-Identifier: BSD-3-Clause

package onigmo_test

import "testing"

// TestCharClassEscapedPunctLiteral covers Onigmo/Ruby's rule that a redundant
// escape of a non-alphanumeric character inside a class is the literal byte:
// /[\.\+~]/ matches "." "+" "~" (this unblocks real-world patterns such as
// apt/dpkg version regexps that write [\.\+~...]).
func TestCharClassEscapedPunctLiteral(t *testing.T) {
	re := mustCompile(t, `[\.\+~\/\*\?\(\)]`)
	for _, s := range []string{".", "+", "~", "/", "*", "?", "(", ")"} {
		if !re.MatchString(s) {
			t.Errorf(`[\.\+~\/\*\?\(\)] should match %q`, s)
		}
	}
	if re.MatchString("a") {
		t.Error("escaped-punct class should not match a letter")
	}
	// A lone escaped dot is the literal ".", not the any-char metacharacter.
	dot := mustCompile(t, `\A[\.]\z`)
	if !dot.MatchString(".") || dot.MatchString("a") {
		t.Error(`[\.] must be a literal dot`)
	}
}

// TestCharClassControlEscapes covers the \f \v \a \e control escapes inside a
// class (form-feed, vertical-tab, bell, escape), matching Onigmo/MRI.
func TestCharClassControlEscapes(t *testing.T) {
	re := mustCompile(t, `[\f\v\a\e]`)
	for _, s := range []string{"\f", "\v", "\a", "\x1b"} {
		if !re.MatchString(s) {
			t.Errorf(`[\f\v\a\e] should match %q`, s)
		}
	}
	if re.MatchString("x") {
		t.Error(`[\f\v\a\e] should not match "x"`)
	}
}
