package xrefgo

import (
	"strings"
)

type sqlHole struct {
	Line    int
	Offset  int
	Context string
}

// substituteSQLHoles uses PostgreSQL parameters as placeholders. A literal 1
// would turn `FROM shard_#{n}` into the invented table `shard_1`. PostgreSQL
// accepts '$' in identifiers, so glued substitutions must also be rejected
// before parsing. The actual Ruby expressions never enter the index.
func substituteSQLHoles(text string, firstLine int) (string, []sqlHole, bool) {
	var query strings.Builder
	holes := []sqlHole{}
	unsafe := false
	for pos := 0; pos < len(text); {
		start := strings.Index(text[pos:], "#{")
		if start < 0 {
			query.WriteString(text[pos:])
			break
		}
		start += pos
		if escapedRubyInterpolation(text, start) {
			query.WriteString(text[pos : start+2])
			pos = start + 2
			continue
		}
		query.WriteString(text[pos:start])
		end, ok := balancedRuby([]byte(text[start+1:]), '{', '}')
		if !ok {
			unsafe = true
			holes = append(holes, sqlHole{Line: firstLine + strings.Count(text[:start], "\n"), Offset: query.Len(), Context: "unclosed"})
			break
		}
		after := start + 1 + end
		context := sqlHoleContext(query.String(), text[after:])
		if context == "glue" || context == "identifier" {
			unsafe = true
		}
		holes = append(holes, sqlHole{Line: firstLine + strings.Count(text[:start], "\n"), Offset: query.Len(), Context: context})
		query.WriteString("$1")
		pos = after
	}
	return query.String(), holes, unsafe
}

func escapedRubyInterpolation(text string, start int) bool {
	slashes := 0
	for i := start - 1; i >= 0 && text[i] == '\\'; i-- {
		slashes++
	}
	return slashes%2 == 1
}

func sqlHoleContext(left, right string) string {
	single, double := sqlQuoteState(left)
	if single {
		return "quoted"
	}
	if double {
		return "identifier"
	}
	if len(left) > 0 && sqlIdentifierByte(left[len(left)-1]) || len(right) > 0 && sqlIdentifierByte(right[0]) {
		return "glue"
	}
	upper := strings.ToUpper(strings.Join(strings.Fields(left), " "))
	for _, suffix := range []string{"FROM", "JOIN", "INTO", "UPDATE", "TABLE", "FROM ONLY", "JOIN ONLY"} {
		if strings.HasSuffix(upper, suffix) {
			return "table"
		}
	}
	for _, suffix := range []string{"ORDER BY", "GROUP BY"} {
		if strings.HasSuffix(upper, suffix) {
			return "order"
		}
	}
	for _, suffix := range []string{"=", "<", ">", "LIKE", "ILIKE", "VALUES", "LIMIT", "OFFSET"} {
		if strings.HasSuffix(upper, suffix) {
			return "value"
		}
	}
	return "expression"
}

func sqlIdentifierByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$'
}

func sqlQuoteState(text string) (single, double bool) {
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\'':
			if double {
				continue
			}
			if single && i+1 < len(text) && text[i+1] == '\'' {
				i++
				continue
			}
			single = !single
		case '"':
			if single {
				continue
			}
			if double && i+1 < len(text) && text[i+1] == '"' {
				i++
				continue
			}
			double = !double
		}
	}
	return single, double
}
