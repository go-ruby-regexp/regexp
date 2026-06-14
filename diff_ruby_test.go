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
