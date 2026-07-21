package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	logging "github.com/google/logger"
	"github.com/lib/pq"
)

func ExecuteExplain(db *sql.DB, queryId string, queryText string, supportsParameterizedExplain bool, logger logging.Logger) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database connection is nil")
	}
	if !isPgExplainableStatement(queryText) {
		return "", fmt.Errorf("unsupported PostgreSQL statement for EXPLAIN")
	}

	if containsUnquotedPgParameter(queryText) {
		if !supportsParameterizedExplain {
			return "", fmt.Errorf("parameterized EXPLAIN requires PostgreSQL 12 or newer")
		}
		explain, err := executePreparedExplain(db, queryId, queryText)
		if err != nil {
			logger.Error("Explain prepared statement error: ", err)
			if isExplainPermissionError(err) {
				return explain, errors.New("need_grant_permission")
			}
		}
		return explain, err
	}

	var explain string
	err := executeDirectExplain(db, queryText, &explain)
	if err == nil {
		return explain, nil
	}
	logger.Error("Explain Error: ", err)
	if isExplainPermissionError(err) {
		return explain, errors.New("need_grant_permission")
	}
	return explain, err
}

func executeDirectExplain(db *sql.DB, queryText string, explain *string) error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return tx.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+queryText).Scan(explain)
}

func isUndefinedRelationError(err error) bool {
	if err == nil {
		return false
	}
	if hasPgErrorCode(err, "42P01") {
		return true
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "relation") && strings.Contains(errText, "does not exist")
}

func isPgExplainableStatement(queryText string) bool {
	switch pgLeadingCommand(queryText) {
	case "select", "with":
		return true
	default:
		return false
	}
}

func pgLeadingCommand(query string) string {
	i, ok := skipPgWhitespaceAndComments(query, 0)
	if !ok || i >= len(query) {
		return ""
	}
	start := i
	for i < len(query) && ((query[i] >= 'a' && query[i] <= 'z') || (query[i] >= 'A' && query[i] <= 'Z')) {
		i++
	}
	return strings.ToLower(query[start:i])
}

func skipPgWhitespaceAndComments(query string, start int) (int, bool) {
	i := start
	for {
		for i < len(query) && isPgWhitespace(query[i]) {
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
			i, ok = skipPgBlockComment(query, i)
			if !ok {
				return i, false
			}
			continue
		}
		return i, true
	}
}

func isPgWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f'
}

func skipPgBlockComment(query string, start int) (int, bool) {
	depth := 1
	for i := start + 2; i < len(query); {
		if i+1 < len(query) && query[i:i+2] == "/*" {
			depth++
			i += 2
			continue
		}
		if i+1 < len(query) && query[i:i+2] == "*/" {
			depth--
			i += 2
			if depth == 0 {
				return i, true
			}
			continue
		}
		i++
	}
	return len(query), false
}

// func normalizePgAbsentValue(value string) string {
// 	if strings.TrimSpace(strings.ToLower(value)) == "null" {
// 		return ""
// 	}
// 	return value
// }

func isExplainPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if hasPgErrorCode(err, "42501") {
		return true
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "select command denied to user") ||
		strings.Contains(errText, "access denied for user") ||
		strings.Contains(errText, "permission denied")
}

func hasPgErrorCode(err error, code pq.ErrorCode) bool {
	var pgErr *pq.Error
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func containsUnquotedPgParameter(query string) bool {
	for i := 0; i < len(query); {
		if i+1 < len(query) && query[i:i+2] == "--" {
			i += 2
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(query) && query[i:i+2] == "/*" {
			var ok bool
			i, ok = skipPgBlockComment(query, i)
			if !ok {
				return false
			}
			continue
		}
		switch query[i] {
		case '\'':
			i = skipPgSingleQuotedString(query, i)
			continue
		case '"':
			i = skipPgDoubleQuotedIdentifier(query, i)
			continue
		case '$':
			if tag, ok := pgDollarQuoteTag(query, i); ok {
				contentStart := i + len(tag)
				closingOffset := strings.Index(query[contentStart:], tag)
				if closingOffset < 0 {
					return false
				}
				i = contentStart + closingOffset + len(tag)
				continue
			}
			if i+1 < len(query) && query[i+1] >= '0' && query[i+1] <= '9' {
				return true
			}
		}
		i++
	}
	return false
}

func skipPgSingleQuotedString(query string, start int) int {
	escapeBackslash := pgEscapeStringPrefix(query, start)
	for i := start + 1; i < len(query); {
		if escapeBackslash && query[i] == '\\' {
			i += 2
			continue
		}
		if query[i] == '\'' {
			if i+1 < len(query) && query[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(query)
}

func pgEscapeStringPrefix(query string, quotePosition int) bool {
	if quotePosition > 0 && (query[quotePosition-1] == 'e' || query[quotePosition-1] == 'E') {
		return quotePosition == 1 || !isPgIdentifierPart(query[quotePosition-2])
	}
	if quotePosition > 1 && query[quotePosition-1] == '&' && (query[quotePosition-2] == 'u' || query[quotePosition-2] == 'U') {
		return quotePosition == 2 || !isPgIdentifierPart(query[quotePosition-3])
	}
	return false
}

func skipPgDoubleQuotedIdentifier(query string, start int) int {
	for i := start + 1; i < len(query); {
		if query[i] == '"' {
			if i+1 < len(query) && query[i+1] == '"' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(query)
}

func pgDollarQuoteTag(query string, start int) (string, bool) {
	if start+1 >= len(query) || query[start] != '$' {
		return "", false
	}
	if query[start+1] == '$' {
		return "$$", true
	}
	if !isPgIdentifierStart(query[start+1]) {
		return "", false
	}
	i := start + 2
	for i < len(query) && isPgDollarTagPart(query[i]) {
		i++
	}
	if i >= len(query) || query[i] != '$' {
		return "", false
	}
	return query[start : i+1], true
}

func isPgIdentifierStart(ch byte) bool {
	return ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch >= 0x80
}

func isPgIdentifierPart(ch byte) bool {
	return isPgIdentifierStart(ch) || (ch >= '0' && ch <= '9') || ch == '$'
}

func isPgDollarTagPart(ch byte) bool {
	return isPgIdentifierStart(ch) || (ch >= '0' && ch <= '9')
}
