// Package sqltoken implements SQL-context-aware handling of tegola
// interpolation tokens — the !BBOX!-style placeholders that SQL providers
// substitute when building queries.
//
// Tokens are meaningful only in ordinary SQL code. A token that appears
// inside a string literal, a quoted identifier, or a comment is SQL text,
// not a substitution site: it must be left verbatim so queries can quote or
// document token-looking text (for example a label '!BBOX!' or a comment
// mentioning !ZOOM!). This package scans SQL into code and protected
// segments and rewrites, detects, or strips tokens in code segments only.
//
// Protected contexts (tokens inside them are never touched):
//
//   - single-quoted string literals, where a doubled quote and a backslash
//     both escape a quote
//   - double-quoted identifiers, with doubled-quote and backslash escaping
//   - backtick identifiers (MySQL), with doubled-backtick and backslash
//     escaping
//   - bracket identifiers (T-SQL/Access), where a doubled closing bracket
//     escapes the bracket
//   - line comments: -- ... and MySQL # ... (through end of line)
//   - block comments: /* ... */ including nesting (PostgreSQL semantics)
//   - PostgreSQL dollar-quoted strings: $tag$ ... $tag$ and $$ ... $$
//     where tag is empty or an identifier-like word (so $1-style
//     placeholders are not dollar quotes)
//
// Everything else is code and eligible for token substitution. Text in
// protected contexts is passed through byte-for-byte: tokens there are not
// even uppercased. Constructs that are never terminated extend to the end
// of the input (fail toward protection).
//
// Caveats (deliberate, documented behavior):
//
//   - '#' always starts a line comment, per the MySQL rule. PostgreSQL
//     operators such as #>, #>> and #- are therefore treated as comment
//     starts: tokens appearing after them on the same line are left
//     verbatim. Queries that combine those operators with !TOKEN!
//     parameters should place the token on its own line.
//   - Matching is token-shaped (see TokenRegexp) rather than raw substring
//     replacement. Malformed edges such as !BOX!xtra! — one token to the
//     regexp, two substrings to a naive replacer — are handled as a single
//     token.
package sqltoken

import (
	"regexp"
	"strings"
)

// TokenRegexp matches a single tegola interpolation token. Token names
// consist of letters, digits, underscores, and dashes between two
// exclamation marks, e.g. !BBOX! or !ID_FIELD!.
var TokenRegexp = regexp.MustCompile(`![a-zA-Z0-9_-]+!`)

// dollarTagRegexp matches the opening delimiter of a PostgreSQL
// dollar-quoted string at the start of the input: $tag$ or $$.
var dollarTagRegexp = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

// Kind classifies a region of an SQL string.
type Kind int

const (
	// Code is ordinary SQL text; tokens here are substitution sites.
	Code Kind = iota
	// StringLiteral is a single-quoted string literal.
	StringLiteral
	// QuotedIdentifier is a double-quoted, backtick-quoted, or bracketed
	// identifier.
	QuotedIdentifier
	// Comment is a line comment (--, #) or a block comment.
	Comment
	// DollarQuoted is a PostgreSQL dollar-quoted string.
	DollarQuoted
)

// Protected reports whether tokens inside this kind of region must be left
// verbatim.
func (k Kind) Protected() bool { return k != Code }

// Segment is a contiguous region of an SQL string and its classification.
type Segment struct {
	Kind       Kind
	Start, End int // byte offsets; End is exclusive
}

// Scan classifies sql into consecutive segments covering the entire input.
// Adjacent segments never share a boundary gap: for i > 0, segs[i].Start ==
// segs[i-1].End, and the union of the segments is the whole string.
func Scan(sql string) []Segment {
	var segs []Segment
	codeStart := 0
	i := 0
	n := len(sql)

	addCode := func(end int) {
		if end > codeStart {
			segs = append(segs, Segment{Kind: Code, Start: codeStart, End: end})
		}
	}
	add := func(k Kind, start, end int) {
		addCode(start)
		segs = append(segs, Segment{Kind: k, Start: start, End: end})
		codeStart = end
		i = end
	}

	for i < n {
		c := sql[i]
		switch {
		case c == '\'':
			add(StringLiteral, i, scanQuoted(sql, i, '\'', true))
		case c == '"':
			add(QuotedIdentifier, i, scanQuoted(sql, i, '"', true))
		case c == '`':
			add(QuotedIdentifier, i, scanQuoted(sql, i, '`', true))
		case c == '[':
			add(QuotedIdentifier, i, scanQuoted(sql, i, ']', false))
		case c == '-' && i+1 < n && sql[i+1] == '-':
			add(Comment, i, scanLineComment(sql, i))
		case c == '#':
			// MySQL line comment. See the package docs for the
			// interaction with PostgreSQL #-operators.
			add(Comment, i, scanLineComment(sql, i))
		case c == '/' && i+1 < n && sql[i+1] == '*':
			add(Comment, i, scanBlockComment(sql, i))
		case c == '$':
			if end, ok := scanDollarQuote(sql, i); ok {
				add(DollarQuoted, i, end)
			} else {
				i++
			}
		default:
			i++
		}
	}
	addCode(n)
	return segs
}

// scanQuoted consumes a quoted region starting at sql[start] == quote and
// returns the index just past the closing quote. A doubled quote is an
// escaped quote; backslash escapes the next byte when backslash is set. An
// unterminated region extends to the end of sql.
func scanQuoted(sql string, start int, quote byte, backslash bool) int {
	i := start + 1
	for i < len(sql) {
		switch sql[i] {
		case '\\':
			if backslash && i+1 < len(sql) {
				i += 2
				continue
			}
			i++
		case quote:
			if i+1 < len(sql) && sql[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		default:
			i++
		}
	}
	return len(sql)
}

// scanLineComment consumes a -- or # comment starting at sql[start] and
// returns the index of the end-of-line (or end of sql).
func scanLineComment(sql string, start int) int {
	for i := start; i < len(sql); i++ {
		if sql[i] == '\n' || sql[i] == '\r' {
			return i
		}
	}
	return len(sql)
}

// scanBlockComment consumes a /* ... */ comment starting at sql[start] and
// returns the index just past the closing delimiter. Block comments nest,
// following PostgreSQL semantics. An unterminated comment extends to the
// end of sql.
func scanBlockComment(sql string, start int) int {
	depth := 1
	i := start + 2
	for i < len(sql) {
		switch {
		case sql[i] == '/' && i+1 < len(sql) && sql[i+1] == '*':
			depth++
			i += 2
		case sql[i] == '*' && i+1 < len(sql) && sql[i+1] == '/':
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(sql)
}

// scanDollarQuote consumes a PostgreSQL dollar-quoted string starting at
// sql[start] == '$' and returns its end and true. It reports ok == false
// when the text at start is not a dollar-quote delimiter (for example the
// $1 placeholder form).
func scanDollarQuote(sql string, start int) (end int, ok bool) {
	open := dollarTagRegexp.FindString(sql[start:])
	if open == "" {
		return 0, false
	}
	rest := sql[start+len(open):]
	if idx := strings.Index(rest, open); idx >= 0 {
		return start + len(open) + idx + len(open), true
	}
	// Unterminated: protect to the end of the input.
	return len(sql), true
}

// Token is a token-shaped match inside a code segment.
type Token struct {
	Start, End int // byte offsets; End is exclusive
	Text       string
}

// CodeTokens returns every token-shaped match in code segments of sql, in
// order of appearance. Tokens in protected contexts are not returned.
func CodeTokens(sql string) []Token {
	var toks []Token
	for _, seg := range Scan(sql) {
		if seg.Kind != Code {
			continue
		}
		for _, loc := range TokenRegexp.FindAllStringIndex(sql[seg.Start:seg.End], -1) {
			s := seg.Start + loc[0]
			e := seg.Start + loc[1]
			toks = append(toks, Token{Start: s, End: e, Text: sql[s:e]})
		}
	}
	return toks
}

// MapTokens rebuilds sql with fn applied to each code-context token. The
// text of protected contexts is copied verbatim. fn receives the token text
// and returns its replacement; returning the input token keeps it verbatim.
func MapTokens(sql string, fn func(token string) string) string {
	var b strings.Builder
	last := 0
	for _, tok := range CodeTokens(sql) {
		b.WriteString(sql[last:tok.Start])
		b.WriteString(fn(tok.Text))
		last = tok.End
	}
	b.WriteString(sql[last:])
	return b.String()
}

// ReplaceToken replaces exact matches of token in code context with value.
// Occurrences of token in protected contexts are left verbatim.
func ReplaceToken(sql, token, value string) string {
	return MapTokens(sql, func(t string) string {
		if t == token {
			return value
		}
		return t
	})
}

// StripTokens removes every code-context token from sql. Token-looking text
// in protected contexts is preserved.
func StripTokens(sql string) string {
	return MapTokens(sql, func(string) string { return "" })
}

// ContainsToken reports whether sql contains any of tokens in code context,
// matching token text exactly.
func ContainsToken(sql string, tokens ...string) bool {
	for _, tok := range CodeTokens(sql) {
		for _, want := range tokens {
			if tok.Text == want {
				return true
			}
		}
	}
	return false
}

// ContainsTokenFold is ContainsToken with case-insensitive token matching.
func ContainsTokenFold(sql string, tokens ...string) bool {
	for _, tok := range CodeTokens(sql) {
		for _, want := range tokens {
			if strings.EqualFold(tok.Text, want) {
				return true
			}
		}
	}
	return false
}

// SpanHasProtectedToken reports whether any token-shaped text within the
// byte range [start, end) of sql lies in a protected context. It guards
// regex-based rewrites that may consume quoted or commented regions: a
// rewrite span carrying a protected token must not be applied.
func SpanHasProtectedToken(sql string, start, end int) bool {
	if start < 0 {
		start = 0
	}
	if end > len(sql) {
		end = len(sql)
	}
	for _, seg := range Scan(sql) {
		if !seg.Kind.Protected() || seg.End <= start || seg.Start >= end {
			continue
		}
		s := seg.Start
		if s < start {
			s = start
		}
		e := seg.End
		if e > end {
			e = end
		}
		if TokenRegexp.MatchString(sql[s:e]) {
			return true
		}
	}
	return false
}
