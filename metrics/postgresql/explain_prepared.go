package postgresql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func executePreparedExplain(db *sql.DB, queryId string, queryText string) (explain string, returnErr error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	stmtName := "releem_" + queryId

	if _, err = conn.ExecContext(ctx, "SET plan_cache_mode = force_generic_plan"); err != nil {
		return "", err
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, "RESET plan_cache_mode"); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("reset plan_cache_mode: %w", err)
		}
	}()

	query := fmt.Sprintf("PREPARE %s AS %s", stmtName, normalizePgStatStatementsTypedParameters(queryText))
	if _, err = conn.ExecContext(ctx, query); err != nil {
		return "", err
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, "DEALLOCATE PREPARE "+stmtName); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("deallocate prepared statement: %w", err)
		}
	}()

	var paramsCount int
	if err = conn.QueryRowContext(ctx, "SELECT COALESCE(cardinality(parameter_types), 0) FROM pg_prepared_statements WHERE name = $1", stmtName).Scan(&paramsCount); err != nil {
		return "", err
	}

	executeQuery := "EXPLAIN (FORMAT JSON) EXECUTE " + stmtName
	if paramsCount > 0 {
		nullParams := strings.TrimRight(strings.Repeat("NULL,", paramsCount), ",")
		executeQuery += "(" + nullParams + ")"
	}
	if err = conn.QueryRowContext(ctx, executeQuery).Scan(&explain); err != nil {
		return "", err
	}
	return explain, nil
}

func normalizePgStatStatementsTypedParameters(query string) string {
	var normalized strings.Builder
	normalized.Grow(len(query))
	lastWritten := 0

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
				break
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
					i = len(query)
					continue
				}
				i = contentStart + closingOffset + len(tag)
				continue
			}
		}

		if !isPgIdentifierStart(query[i]) {
			i++
			continue
		}
		wordStart := i
		for i < len(query) && isPgIdentifierPart(query[i]) {
			i++
		}
		dataType := strings.ToLower(query[wordStart:i])
		if dataType != "date" && dataType != "timestamp" && dataType != "interval" {
			continue
		}

		parameterStart := i
		for parameterStart < len(query) && isPgWhitespace(query[parameterStart]) {
			parameterStart++
		}
		if parameterStart+1 >= len(query) || query[parameterStart] != '$' || query[parameterStart+1] < '0' || query[parameterStart+1] > '9' {
			continue
		}
		parameterEnd := parameterStart + 2
		for parameterEnd < len(query) && query[parameterEnd] >= '0' && query[parameterEnd] <= '9' {
			parameterEnd++
		}

		normalized.WriteString(query[lastWritten:wordStart])
		normalized.WriteString(query[parameterStart:parameterEnd])
		normalized.WriteString("::")
		normalized.WriteString(dataType)
		lastWritten = parameterEnd
		i = parameterEnd
	}

	if lastWritten == 0 {
		return query
	}
	normalized.WriteString(query[lastWritten:])
	return normalized.String()
}

func shouldRetryExplainWithoutPrepare(err error) bool {
	if err == nil {
		return false
	}
	if hasPgErrorCode(err, "42P18") ||
		hasPgErrorCode(err, "42883") ||
		hasPgErrorCode(err, "42704") {
		return true
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "could not determine data type of parameter") ||
		strings.Contains(errText, "indeterminate_datatype") ||
		strings.Contains(errText, "cannot determine data type of") ||
		strings.Contains(errText, "operator does not exist")
}

func executeDirectExplainWithFallbackParameters(db *sql.DB, queryText string) (string, error) {
	withNulls := replacePgParametersWithNull(queryText)
	if withNulls == queryText {
		return "", fmt.Errorf("no parameter placeholders to replace")
	}

	var explain string
	if err := executeDirectExplain(db, withNulls, &explain); err != nil {
		return "", err
	}
	return explain, nil
}

func replacePgParametersWithNull(query string) string {
	var replaced strings.Builder
	replaced.Grow(len(query))
	lastWritten := 0

	for i := 0; i < len(query); {
		if i+1 < len(query) && query[i:i+2] == "--" {
			i += 2
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(query) && query[i:i+2] == "/*" {
			next, ok := skipPgBlockComment(query, i)
			if !ok {
				return query
			}
			i = next
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
					return query
				}
				i = contentStart + closingOffset + len(tag)
				continue
			}

			if i+1 < len(query) && query[i+1] >= '0' && query[i+1] <= '9' {
				parameterStart := i
				parameterEnd := i + 1
				for parameterEnd < len(query) && query[parameterEnd] >= '0' && query[parameterEnd] <= '9' {
					parameterEnd++
				}
				replaced.WriteString(query[lastWritten:parameterStart])
				replaced.WriteString("NULL")
				lastWritten = parameterEnd
				i = parameterEnd
				continue
			}
		}

		i++
	}

	if lastWritten == 0 {
		return query
	}
	replaced.WriteString(query[lastWritten:])
	return replaced.String()
}
