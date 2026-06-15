// Package onigmo is a pure-Go (cgo-free) reimplementation of Onigmo, the
// regular-expression engine used by Ruby.
//
// Unlike Go's standard library regexp (RE2), it is a backtracking VM and so can
// support the Onigmo features RE2 omits — backreferences, lookahead/lookbehind,
// possessive quantifiers, atomic groups, named groups and subexpression calls —
// with Ruby's leftmost-first match semantics, hardened against catastrophic
// backtracking (ReDoS) by memoization and a deterministic time/step budget.
//
// Phase 0 is implemented: a greedy backtracking VM with leftmost-first match
// semantics covering literals and escapes, the dot metacharacter, character
// classes (ranges, negation, and the \d \D \w \W \s \S escapes), the anchors
// \A \z \Z ^ $, the greedy quantifiers * + ? {m} {m,} {m,n}, capturing and
// non-capturing groups, and alternation. Phase 1 adds named groups
// (?<name>...), backreferences (\1..\9 and \k<name>), and the non-greedy (lazy)
// quantifiers *? +? ?? {m,n}? (which match the fewest repetitions first, so
// <.+?> matches the shortest tag) and the possessive quantifiers *+ ++ ?+ and
// atomic groups (?>...) (which commit a matched span — once it matches, its
// backtrack points are discarded, so a++a and (?>a+)a never match "aaa"; a
// trailing + on a {m,n} brace is instead a stacked greedy repeat, as in
// Onigmo). Phase 2 adds the
// lookaround assertions — positive and negative lookahead (?=...) (?!...) and
// lookbehind (?<=...) (?<!...) — and the \G anchor (which pins a match to the
// scan start). Lookbehind bodies must be of constant width per alternative, as
// in Onigmo/Ruby; variable-width lookbehind is rejected. Phase 3 begins with
// POSIX bracket classes [[:name:]] (and negated [[:^name:]]) inside character
// classes, for the 14 standard names alpha, digit, alnum, upper, lower, space,
// blank, cntrl, graph, print, punct, xdigit, and word; and the inline options
// (?flags) / (?flags:...) (with the (?-flags) turn-off form) for the letters i
// (case-insensitive matching), m (dot-all: the dot also matches a newline), and
// x (extended/free-spacing: unescaped whitespace and # comments in the pattern
// are ignored, except inside a character class).
//
// Under /i, literals and character classes fold rune-aware using Go's
// unicode.SimpleFold (simple, 1:1 case folding): a folded character is decoded as
// a whole UTF-8 code point and matches any code point in its simple-case-folding
// orbit, so /É/i matches é, Greek and Cyrillic case pairs fold, and even ASCII
// /k/i reaches the Kelvin sign U+212A. A folded class is rune-aware too (so
// (?i)[a-z] matches A and the Kelvin sign, and (?i)[α-ω] an uppercase Greek
// letter), with multi-byte members and ranges and last-applied negation. A folded
// atom obeys the same rune/byte boundary as \p{…} (it never matches at a UTF-8
// continuation byte, keeps byte offsets, and is rejected inside a fixed-width
// lookbehind). Only simple folding is done: full/special folding (ß→"ss",
// Turkish dotless-i) is out of scope, and backreference folding stays ASCII-only.
//
// Phase 3 also adds Unicode property classes \p{name} / \P{name} (and the
// in-brace negation \p{^name}), the one rune-aware atom in the otherwise
// byte-oriented engine: it decodes a single UTF-8 code point and advances by
// its byte length. The supported names are the general categories L, N, P, S,
// Z, C and the letter/number subcategories Lu, Ll, Lt, Lm, Lo, Nd, plus the
// Onigmo POSIX-style aliases Alpha, Alnum, Digit, Space, Upper, Lower and Word
// (following Ruby's definitions). A property may also appear inside a character
// class ([\p{L}\d]), which makes that class rune-aware while its ASCII
// byte-range members keep working. Every other construct stays byte-oriented
// and byte-exact; a rune-aware atom never matches at a UTF-8 continuation byte,
// so the scan never tests a code point mid-character (as MRI, which positions by
// character). Note that match offsets remain byte offsets, whereas MRI reports
// character offsets, so the two agree on matched text but not on the numeric
// span on multi-byte input. (Rune-level Unicode case-folding for /i is described
// above.)
//
// Phase 3 also adds the hex-digit class \h (Onigmo's [0-9A-Fa-f]) and its
// byte-complement \H — usable standalone or inside a character class, and
// byte-oriented like \d/\w — and the linebreak escape \R, lowered to
// (?>\r\n|[\n\v\f\r\x{85}\x{2028}\x{2029}]): a CR-LF pair matches atomically as
// one unit (so \R\n never splits a CRLF) and a lone \n, \r, \v, \f, NEL, LS or
// PS also matches. \R is rune-aware (it carries the multi-byte NEL/LS/PS
// members), so like \p{…} it decodes a whole code point and is rejected inside a
// fixed-width lookbehind.
//
// Subexpression calls \g<…> are implemented: \g<name>, \g<n> (absolute group
// number), the relative forms \g<+n> / \g<-n>, and \g<0> (recurse the whole
// pattern). A call is a true re-execution of the referenced group's sub-pattern
// at the current position, and it re-captures into that group's slot, so the
// most recent execution wins — except that a group recursing into itself keeps
// its outermost binding, exactly as Onigmo/Ruby does (the engine saves and
// restores the open groups' captures across a call). Calls may be recursive and
// mutually recursive, so the balanced-parentheses idiom
// \A(?<bal>\((?:[^()]|\g<bal>)*\))\z works. Forward references (calling a group
// defined later) resolve in a post-parse pass. Recursion is bounded by a hard
// depth cap plus the step budget, so a pathological or non-terminating grammar
// (e.g. \A\g<0>\z, which Onigmo rejects statically) fails deterministically
// rather than exhausting the Go stack. A call has data/recursion-dependent
// width, so — like a backreference — it is rejected inside a fixed-width
// lookbehind (a documented divergence: MRI accepts the simple one-char case).
//
// ReDoS hardening (Phase 4) is in: for a pattern without a backreference or a
// subexpression call the VM memoizes the (instruction, position) split states it
// has explored and never re-explores one, so catastrophic patterns such as
// (a+)+$ run in polynomial rather than exponential time while producing the
// identical leftmost-first match. A deterministic step budget (and, for calls,
// the recursion-depth cap) remains as the backstop, joined by an optional
// wall-clock timeout (Ruby's Regexp.timeout equivalent): re.WithTimeout(d)
// returns a concurrency-safe copy that aborts a match exceeding d of real time,
// reporting no match. A pathological pattern is bounded by whichever of the
// budget or the deadline it reaches first.
//
// A transparent start-position prefilter (Phase 4) accelerates the search: the
// optimizer analyses the compiled program's leading path for a \A anchor, a
// required literal prefix, or a first-byte set — including the union over a
// leading alternation (foo|bar, a*b) — and uses it to skip start positions that
// cannot begin a match (a strings.Index or byte-set scan instead of invoking the
// VM at every offset). Every candidate it yields is still verified by the VM, so
// results are byte-identical to an unfiltered scan. See docs/plan-regexp.md for
// the full roadmap.
package onigmo
