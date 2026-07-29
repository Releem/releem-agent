package mysql

import "strings"

func isMySQLExplainableStatement(queryText string) bool {
	switch mysqlLeadingCommand(queryText) {
	case "select", "with":
		return true
	default:
		return false
	}
}

func mysqlLeadingCommand(query string) string {
	i, ok := skipMySQLWhitespaceAndComments(query, 0)
	if !ok || i >= len(query) {
		return ""
	}
	start := i
	for i < len(query) && ((query[i] >= 'a' && query[i] <= 'z') || (query[i] >= 'A' && query[i] <= 'Z')) {
		i++
	}
	return strings.ToLower(query[start:i])
}

func skipMySQLWhitespaceAndComments(query string, start int) (int, bool) {
	i := start
	for {
		for i < len(query) && isMySQLWhitespace(query[i]) {
			i++
		}
		if i+1 >= len(query) {
			return i, true
		}
		if query[i:i+2] == "--" {
			i += 2
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		}
		if query[i:i+2] == "/*" {
			var ok bool
			i, ok = skipMySQLBlockComment(query, i)
			if !ok {
				return i, false
			}
			continue
		}
		return i, true
	}
}

func isMySQLWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f' || ch == '\v'
}

func skipMySQLBlockComment(query string, start int) (int, bool) {
	i := start + 2
	for i+1 < len(query) {
		if query[i] == '*' && query[i+1] == '/' {
			return i + 2, true
		}
		i++
	}
	return len(query), false
}
