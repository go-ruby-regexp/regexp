package vm

import (
	"testing"

	"github.com/go-ruby-regexp/regexp/internal/compile"
	"github.com/go-ruby-regexp/regexp/internal/syntax"
)

// TestDFAMatchAtVsVM cross-checks the anchored-at-pos DFA primitive against the
// backtracking VM anchored at the same position, for every position of several
// lexer-shaped inputs. The two engines must report byte-identical [begin,end)
// spans (begin==pos on a match).
func TestDFAMatchAtVsVM(t *testing.T) {
	pats := []string{
		`[a-zA-Z_][a-zA-Z0-9_]*`, `[0-9]+`, `\s+`, `[-+*/;]`, `[A-Za-z0-9_]+`,
		`\S+`, `a+`, `(?:ab)+`, `[abc]*`, `\d`, `.`, `^`, `\Afoo`, `foo|bar`,
		`\bword\b`, `[^ ]+`, `x?y`, `α+`, `[α-ω]+`,
	}
	inputs := []string{
		"foo123 + bar456 - baz789 * qux000 / quux ; ",
		"   leading   spaces",
		"word boundaries here word",
		"αβγ δεζ ηθι",
		"",
		"a",
		"xyz",
	}
	for _, pat := range pats {
		prog, err := compileProg(pat)
		if err != nil {
			t.Fatalf("compile %q: %v", pat, err)
		}
		dfa := forceDFA(prog)
		if dfa == nil {
			continue // outside the DFA subset; VM-only, nothing to cross-check here
		}
		for _, in := range inputs {
			for pos := 0; pos <= len(in); pos++ {
				vb, ve, vok := vmMatchAtBounds(prog, in, pos)
				db, de, dok := dfa.MatchAt(in, prog.Enc, pos)
				if vok != dok || (vok && (vb != db || ve != de)) {
					t.Fatalf("pat=%q in=%q pos=%d: VM=(%d,%d,%v) DFA=(%d,%d,%v)",
						pat, in, pos, vb, ve, vok, db, de, dok)
				}
			}
		}
	}
}

func compileProg(pat string) (*compile.Program, error) {
	res, err := syntax.ParseEnc(pat, compile.UTF8)
	if err != nil {
		return nil, err
	}
	return compile.CompileEnc(res, compile.UTF8), nil
}

func vmMatchAtBounds(prog *compile.Program, in string, pos int) (int, int, bool) {
	caps, ok, err := MatchAt(prog, in, pos, DefaultBudget)
	if err != nil || !ok {
		return 0, 0, false
	}
	return caps[0], caps[1], true
}
