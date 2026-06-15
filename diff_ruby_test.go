package onigmo_test

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	onigmo "github.com/go-onigmo/regexp"
)

// rubyCase is one (pattern, input) pair for differential testing against MRI.
type rubyCase struct {
	pattern string
	input   string
}

// diffCorpus is a substantial set of patterns and inputs exercising greedy
// backtracking, alternation, anchors, quantifiers, classes, and groups. Each is
// run through both this engine and the system Ruby and compared exactly.
var diffCorpus = []rubyCase{
	// Literals and escapes.
	{`abc`, "xxabcxx"},
	{`a\.c`, "a.c"},
	{`a\.c`, "abc"},
	{`\(\)`, "()"},
	{`\{\}`, "{}"},
	{`a\\b`, `a\b`},
	{`\n`, "x\ny"},
	{`\t`, "x\ty"},

	// Dot.
	{`a.c`, "a-c"},
	{`a.c`, "a\nc"},
	{`.`, "\n"},
	{`.+`, "hello\nworld"},

	// Character classes.
	{`[abc]`, "zzbzz"},
	{`[a-z]+`, "ABCdefGHI"},
	{`[^a-z]+`, "abcDEFabc"},
	{`[0-9]{2,4}`, "x12345y"},
	{`[]a]+`, "]a]ab"},
	{`[a^]+`, "^a^xa"},
	{`[\d]+`, "ab123cd"},
	{`[\w]+`, "  foo_bar  "},
	{`[^\d]+`, "12abc34"},

	// Perl class escapes.
	{`\d+`, "abc123def"},
	{`\D+`, "123abc456"},
	{`\w+`, "  hello_world  "},
	{`\W+`, "ab   cd"},
	{`\s+`, "a   b"},
	{`\S+`, "   word   "},

	// Anchors.
	{`\Aabc`, "abcdef"},
	{`\Aabc`, "xabcdef"},
	{`xyz\z`, "wwxyz"},
	{`xyz\Z`, "wwxyz\n"},
	{`xyz\Z`, "wwxyz"},
	{`^foo`, "bar\nfoobar"},
	{`bar$`, "foobar\nbaz"},
	{`^$`, "\n\n"},

	// Quantifiers (greedy).
	{`a*`, "aaab"},
	{`a+`, "baaab"},
	{`a?b`, "b"},
	{`a?b`, "ab"},
	{`a{3}`, "aaaaa"},
	{`a{2,}`, "aaaa"},
	{`a{2,3}`, "aaaaa"},
	{`ab*`, "abbbbc"},
	{`a.*b`, "axbxbxb"},
	{`a.*b.*c`, "axbxbxcxc"},

	// Groups and captures.
	{`(ab)+`, "ababab"},
	{`(a)(b)(c)`, "abc"},
	{`(?:ab)+`, "ababx"},
	{`(\d+)-(\d+)`, "year 2026-06 end"},
	{`(a(b(c)))`, "abc"},
	{`(ab|cd)+`, "abcdab"},

	// Alternation (leftmost-first).
	{`a|ab`, "ab"},
	{`ab|a`, "ab"},
	{`foo|foobar`, "foobar"},
	{`cat|dog|bird`, "I have a dog"},
	{`(foo|foob)ar`, "foobar"},

	// Backtracking interactions.
	{`a.*c`, "axbxcxc"},
	{`(a+)(a+)`, "aaaa"},
	{`(a*)*b`, "aaab"},
	{`(a|aa)+b`, "aaaab"},
	{`.*$`, "line"},

	// No-match cases.
	{`xyz`, "abc"},
	{`\d+`, "abc"},
	{`^abc$`, "xabc"},

	// Lookahead (Phase 2).
	{`foo(?=bar)`, "foobar"},
	{`foo(?=bar)`, "foobaz"},
	{`foo(?!bar)`, "foobaz"},
	{`foo(?!bar)`, "foobar"},
	{`\d+(?=px)`, "10px 20em"},
	{`\d+(?!px)`, "10px 20em"},
	{`a(?=b(?=c))`, "abc"},
	{`a(?=b(?=c))`, "abd"},
	{`(?=(\d+))\d`, "x42y"},
	{`q(?=u)i?`, "quit"},

	// Lookbehind (Phase 2): fixed/bounded width only.
	{`(?<=foo)bar`, "foobar"},
	{`(?<=foo)bar`, "xxxbar"},
	{`(?<!foo)bar`, "xxxbar"},
	{`(?<!foo)bar`, "foobar"},
	{`(?<=\$)\d+`, "price $42 here"},
	{`(?<=ab|c)d`, "abd"},
	{`(?<=ab|c)d`, "cd"},
	{`(?<=a.c)d`, "abcd"},
	{`(?<=\d{3})x`, "123x"},
	{`(?<!\d)\d`, "a1b2"},

	// \G (Phase 2): anchors to the scan origin for a single match.
	{`\Gabc`, "abcdef"},
	{`\Gabc`, "xabcdef"},
	{`\G\d+`, "123abc"},
	{`\G\d+`, "abc123"},

	// POSIX bracket classes (Phase 3).
	{`[[:alpha:]]+`, "ab12cd"},
	{`[[:digit:]]+`, "x42y"},
	{`[[:alnum:]]+`, "  a1b2  "},
	{`[[:upper:]]+`, "abCDef"},
	{`[[:lower:]]+`, "ABcdEF"},
	{`[[:space:]]+`, "x \t\ny"},
	{`[[:blank:]]+`, "x \ty"},
	{`[[:xdigit:]]+`, "ghFF00zz"},
	{`[[:punct:]]+`, "a!@#b"},
	{`[[:word:]]+`, "  foo_bar  "},
	{`[[:graph:]]+`, "  ab!  "},
	{`[[:print:]]+`, "\tab \n"},
	{`[[:^digit:]]+`, "12ab34"},
	{`[^[:space:]]+`, "  hi there "},
	{`[x[:digit:]]+`, "x1y2"},

	// Case-insensitive matching via inline (?i) (Phase 3).
	{`(?i)abc`, "xxABCyy"},
	{`(?i)ABC`, "abc"},
	{`(?i:abc)d`, "ABCd"},
	{`(?i:abc)d`, "ABCD"},
	{`a(?i)bc`, "aBC"},
	{`a(?i)bc`, "ABC"},
	{`(?i)a(?-i)b`, "AB"},
	{`(a(?i)b)c`, "aBc"},
	{`(?i)a|b`, "B"},
	{`a(?i)|b`, "B"},
	{`a|(?i)b|c`, "C"},
	{`(?i)[a-z]+`, "ABCdef"},
	{`(?i)[^a-z]+`, "ABC123"},
	{`(?i)[m-p]`, "Q"},
	{`(?i)(ab)\1`, "AbAB"},
	{`(ab)(?i)\1`, "abAB"},
	{`(?i)(?<g>ab)\k<g>`, "ABab"},

	// ReDoS memoization (Phase 4): patterns whose naive backtracking is
	// exponential must still produce Ruby-exact spans. The inputs end in a byte
	// that defeats the final anchor/atom so the engine explores the worst case.
	{`(a+)+$`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa!"},
	{`(a|aa)+$`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa!"},
	{`(a*)*$`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa!"},
	{`(.*)*$`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa!"},
	{`(a+)+b`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	{`(a+)+b`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"},

	// Unicode property classes \p{…} on ASCII input (Phase 3). Here byte and
	// character offsets coincide, so the exact-span comparison applies.
	{`\p{L}+`, "abc123"},
	{`\p{Lu}+`, "ABcd"},
	{`\p{Ll}+`, "ABcd"},
	{`\p{Nd}+`, "x42y"},
	{`\p{N}+`, "x42y"},
	{`\p{P}+`, "a!?,b"},
	{`\p{S}+`, "a+=<b"},
	{`\P{L}+`, "abc123!"},
	{`\p{^Nd}+`, "12ab"},
	{`\p{Alpha}+`, "abc12"},
	{`\p{Alnum}+`, "ab12 cd"},
	{`\p{Digit}+`, "a12b"},
	{`\p{Upper}+`, "abCDef"},
	{`\p{Lower}+`, "ABcdEF"},
	{`\p{Word}+`, "foo_bar baz"},
	{`[\p{L}\d]+`, "ab12!cd"},
	{`[^\p{L}]+`, "ab12!cd"},
	{`[\p{Lu}x]+`, "xXAby"},

	// Subexpression calls \g<…> (this phase). A call re-runs and re-captures the
	// referenced group's sub-pattern; these are ASCII, so byte and character
	// offsets coincide and the exact-span comparison applies.
	{`(\d+)-\g<1>`, "12-34"},                            // absolute number, re-captures
	{`(\d)\g<1>`, "12"},                                 // adjacent call
	{`(\d)\g<1>`, "1"},                                  // call needs a second char: no match
	{`(\w)\g<1>`, "ab"},                                 // word
	{`(a|b)\g<1>`, "ab"},                                // call re-runs the alternation
	{`(?<two>\d)\g<two>`, "34"},                         // named call
	{`\g<two>(?<two>\d+)`, "123"},                       // forward named reference
	{`\g<+1>(\d)`, "55"},                                // relative forward (needs two)
	{`\g<+1>(\d)`, "5"},                                 // relative forward: one char, no match
	{`(\d)\g<-1>`, "12"},                                // relative backward
	{`(a)(b)\g<-2>\g<-1>`, "abab"},                      // two relative backward calls
	{`(x)(\d)\g<2>`, "x12"},                             // call one of several groups
	{`(\d)\g<1>\1`, "122"},                              // backref sees the call's re-capture
	{`(\d)\g<1>\1`, "121"},                              // re-capture makes the backref fail
	{`(?<x>\d)\g<x>+`, "1234"},                          // a quantified call
	{`(?=(\d)\g<1>)\d+`, "12x"},                         // call inside a lookahead
	{`foo(?=\g<1>)(bar)`, "foobar"},                     // forward call inside a lookahead
	{`\((?:[^()]|\g<0>)*\)`, "((x))"},                   // \g<0> whole-pattern recursion
	{`\((?:[^()]|\g<0>)*\)`, "(()"},                     // unbalanced: no match
	{`(?<bal>\((?:[^()]|\g<bal>)*\))`, "(a(b)c)"},       // balanced parens, named recursion
	{`\A(?<bal>\((?:[^()]|\g<bal>)*\))\z`, "((()))"},    // deep balanced parens, anchored
	{`\A(?<bal>\((?:[^()]|\g<bal>)*\))\z`, "(()"},       // unbalanced, anchored: no match
	{`\A(?<e>(?:[^<>]|<\g<e>>)*)\z`, "a<b<c>d>e"},      // balanced angle brackets grammar
	{`\A(?<bal>\((?:[^()]|\g<bal>)*\))\z`, "((((((((((x))))))))))"}, // deep nesting still matches
	// A sub-capture inside a recursive group keeps its *deepest* binding (it is not
	// active at the recursive call sites), while the recursive group keeps its
	// *outermost* binding — both must match MRI's exact spans.
	{`\A(?<b>\((?<inner>[^()]*)(?:\g<b>)?[^()]*\))\z`, "(a(b)c)"},
	// Mutual recursion between two named groups.
	{`\A(?<a>x(?:\g<b>)?)(?<b>y(?:\g<a>)?)\z`, "xyx"},
	// A recursive arithmetic-expression grammar.
	{`\A(?<term>(?<num>\d+)|\((?<expr>\g<term>(?:\+\g<term>)*)\))\z`, "(1+2+3)"},
}

// diffUnicodeCorpus exercises \p{…} on genuinely multi-byte UTF-8 input. MRI
// reports match offsets in characters whereas this engine reports them in
// bytes, so these are compared by the matched substrings (whole match plus each
// capture), which are representation-independent, rather than by raw offsets.
var diffUnicodeCorpus = []rubyCase{
	{`\p{L}+`, "héllo123"},
	{`\p{L}`, "123éxy"},
	{`\p{Lu}`, "héllo Wörld"},
	{`\p{Ll}+`, "Héllo"},
	{`\p{Lo}+`, "abc中文def"},
	{`\p{N}+`, "café42"},
	{`\p{Nd}+`, "²³45"},
	{`\P{Alpha}+`, "héllo123"},
	{`\p{^L}+`, "héllo123"},
	{`\p{Alpha}+`, "héllo123"},
	{`\p{Alnum}+`, "héllo123!"},
	{`\p{Word}+`, "naïve_42 x"},
	{`\p{Space}+`, "a \tb"},
	{`\p{Z}+`, "a  b"},
	{`\p{P}+`, "café—dash"},
	{`[\p{L}\d]+`, "héllo3!"},
	{`[^\p{L}]+`, "héllo123x"},
	{`(\p{Lu})(\p{Ll}+)`, "Héllo"},

	// Rune-level case folding under (?i): a multi-byte letter literal folds to its
	// Unicode case partner via simple (1:1) case folding. Only literal folds are
	// exercised here — they are stable long-standing Onigmo behaviour across MRI
	// versions; multi-byte character-class folding is version-sensitive in MRI, so
	// it is asserted only by the oracle-independent unit tests in
	// phase3_runefold_test.go (which encode the correct modern behaviour).
	{`(?i)É`, "éxy"},
	{`(?i)é`, "ÉXY"},
	{`(?i)café`, "CAFÉ here"},
	{`(?i)naïve`, "a NAÏVE one"},
	{`(?i)Σ`, "σ end"},     // Greek sigma (Σ ↔ σ)
	{`(?i)σ`, "ς end"},     // and the final-sigma orbit member ς
	{`(?i)Б`, "б end"},     // Cyrillic
	{`(?i)Ωμέγα`, "ωμέγα"}, // a whole folded Greek word
	{`(?i)Δ+`, "δδΔx"},
	{`(?i)(é)`, "É"}, // captured folded literal
}

// runRuby returns Ruby's span report for one case: begin0,end0 then each
// capture's begin,end. A leading "nil" line means no match. It returns ok=false
// if Ruby is unavailable.
func runRuby(t *testing.T, c rubyCase) (string, bool) {
	t.Helper()
	script := `
m = Regexp.new(ARGV[0]).match(ARGV[1])
if m.nil?
  puts "nil"
else
  parts = [m.begin(0), m.end(0)]
  (1..(m.size - 1)).each do |i|
    if m.begin(i).nil?
      parts << -1 << -1
    else
      parts << m.begin(i) << m.end(i)
    end
  end
  puts parts.join(",")
end
`
	out, err := exec.Command("ruby", "-e", script, c.pattern, c.input).Output()
	if err != nil {
		t.Fatalf("ruby failed for /%s/ on %q: %v", c.pattern, c.input, err)
	}
	return strings.TrimSpace(string(out)), true
}

// runRubyStrings returns Ruby's matched substrings for one case: the whole
// match then each capture (a non-participating capture renders as "\x00"). A
// leading "nil" means no match. These are compared instead of offsets for
// multi-byte inputs, where MRI reports character offsets and this engine reports
// byte offsets but the matched text is identical.
func runRubyStrings(t *testing.T, c rubyCase) string {
	t.Helper()
	script := `
m = Regexp.new(ARGV[0]).match(ARGV[1])
if m.nil?
  puts "nil"
else
  parts = [m[0]]
  (1..(m.size - 1)).each { |i| parts << (m[i].nil? ? "\x00" : m[i]) }
  puts parts.join("\x01")
end
`
	out, err := exec.Command("ruby", "-e", script, c.pattern, c.input).Output()
	if err != nil {
		t.Fatalf("ruby failed for /%s/ on %q: %v", c.pattern, c.input, err)
	}
	return strings.TrimRight(string(out), "\n")
}

// engineStrings renders this engine's matched substrings in the format
// runRubyStrings uses.
func engineStrings(re *onigmo.Regexp, input string) string {
	m := re.Match(input)
	if m == nil {
		return "nil"
	}
	parts := []string{m.Str(0)}
	for i := 1; i <= m.NGroups(); i++ {
		if m.Begin(i) < 0 {
			parts = append(parts, "\x00")
		} else {
			parts = append(parts, m.Str(i))
		}
	}
	return strings.Join(parts, "\x01")
}

// engineSpans renders this engine's spans in the same format runRuby uses.
func engineSpans(re *onigmo.Regexp, input string) string {
	m := re.Match(input)
	if m == nil {
		return "nil"
	}
	parts := []string{strconv.Itoa(m.Begin(0)), strconv.Itoa(m.End(0))}
	for i := 1; i <= m.NGroups(); i++ {
		parts = append(parts, strconv.Itoa(m.Begin(i)), strconv.Itoa(m.End(i)))
	}
	return strings.Join(parts, ",")
}

func TestDifferentialAgainstRuby(t *testing.T) {
	// The oracle shells out to `ruby -e` with patterns/inputs containing
	// newlines and regex metacharacters; argument quoting and CRLF handling on
	// Windows make this unreliable, so restrict it to Unix (Linux/macOS CI
	// exercise the full corpus).
	if runtime.GOOS == "windows" {
		t.Skip("differential oracle shells out to ruby; skipped on Windows")
	}
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not on PATH; skipping differential test")
	}
	for _, c := range diffCorpus {
		re, err := onigmo.Compile(c.pattern)
		if err != nil {
			t.Errorf("compile /%s/: %v", c.pattern, err)
			continue
		}
		got := engineSpans(re, c.input)
		want, _ := runRuby(t, c)
		if got != want {
			t.Errorf("/%s/ on %q: engine=%s ruby=%s", c.pattern, c.input, got, want)
		}
	}
}

// TestDifferentialUnicodeAgainstRuby compares matched substrings (not offsets)
// on multi-byte UTF-8 input, where MRI's character offsets and this engine's
// byte offsets differ by representation only.
func TestDifferentialUnicodeAgainstRuby(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("differential oracle shells out to ruby; skipped on Windows")
	}
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not on PATH; skipping differential test")
	}
	for _, c := range diffUnicodeCorpus {
		re, err := onigmo.Compile(c.pattern)
		if err != nil {
			t.Errorf("compile /%s/: %v", c.pattern, err)
			continue
		}
		got := engineStrings(re, c.input)
		want := runRubyStrings(t, c)
		if got != want {
			t.Errorf("/%s/ on %q: engine=%q ruby=%q", c.pattern, c.input, got, want)
		}
	}
}
