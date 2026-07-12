package phase2

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	ddlIdentifierPattern         = "(?:`(?:``|[^`])+`|\"(?:\"\"|[^\"])+\"|[A-Za-z0-9_$\\x{0080}-\\x{FFFF}]+)"
	ddlQualifiedTablePattern     = ddlIdentifierPattern + "(?:\\s*\\.\\s*" + ddlIdentifierPattern + ")?"
	alterTableTargetPattern      = regexp.MustCompile("(?i)^\\s*ALTER\\s+(?:(?:ONLINE|IGNORE)\\s+){0,2}TABLE\\s+(?:IF\\s+EXISTS\\s+)?(" + ddlQualifiedTablePattern + ")(?:\\s|$)")
	createIndexTargetPattern     = regexp.MustCompile("(?i)^\\s*CREATE\\s+(?:OR\\s+REPLACE\\s+)?(?:(?:UNIQUE|FULLTEXT|SPATIAL)\\s+)?INDEX\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?" + ddlIdentifierPattern + "(?:\\s+USING\\s+(?:BTREE|HASH|RTREE))?\\s+ON\\s+(" + ddlQualifiedTablePattern + ")(?:\\s|\\(|$)")
	createIndexPTOSCPattern      = regexp.MustCompile("(?i)^\\s*CREATE\\s+((?:(?:UNIQUE|FULLTEXT|SPATIAL)\\s+)?INDEX\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?" + ddlIdentifierPattern + "(?:\\s+USING\\s+(?:BTREE|HASH|RTREE))?)\\s+ON\\s+(" + ddlQualifiedTablePattern + ")(?:\\s|\\(|$)")
	tableReferencePattern        = regexp.MustCompile("^\\s*(" + ddlIdentifierPattern + ")(?:\\s*\\.\\s*(" + ddlIdentifierPattern + "))?\\s*$")
	leadingTableReferencePattern = regexp.MustCompile("^\\s*(" + ddlQualifiedTablePattern + ")(?:\\s|\\(|$)")
)

// TableInfo represents table information
type TableInfo struct {
	Database string
	Table    string
}

// ParseTableName parses a table name that may include database name
func ParseTableName(tableName string, getCurrentDB func() (string, error)) (TableInfo, error) {
	database, table, qualified, err := parseTableReference(tableName)
	if err != nil {
		return TableInfo{}, err
	}
	if qualified {
		return TableInfo{Database: database, Table: table}, nil
	}

	db, err := getCurrentDB()
	if err != nil {
		return TableInfo{}, err
	}
	return TableInfo{Database: db, Table: table}, nil
}

func parseTableReference(reference string) (database, table string, qualified bool, err error) {
	match := tableReferencePattern.FindStringSubmatch(reference)
	if len(match) != 3 {
		return "", "", false, fmt.Errorf("invalid table reference %q", reference)
	}
	if match[2] == "" {
		return "", unquoteDDLIdentifier(match[1]), false, nil
	}
	return unquoteDDLIdentifier(match[1]), unquoteDDLIdentifier(match[2]), true, nil
}

func unquoteDDLIdentifier(identifier string) string {
	if len(identifier) < 2 {
		return identifier
	}
	switch identifier[0] {
	case '`':
		return strings.ReplaceAll(identifier[1:len(identifier)-1], "``", "`")
	case '"':
		return strings.ReplaceAll(identifier[1:len(identifier)-1], `""`, `"`)
	default:
		return identifier
	}
}

// ExtractAlterStatement extracts the ALTER statement part from SQL
func ExtractAlterStatement(sql string) string {
	sql = strings.TrimSpace(sql)
	masked := maskSQLStringsAndComments(sql, false)
	match := alterTableTargetPattern.FindStringSubmatchIndex(masked)
	if len(match) >= 4 {
		return strings.TrimSpace(sql[match[3]:])
	}

	return sql
}

// maskSQLStringsAndComments keeps byte offsets stable while hiding content
// that must not be interpreted as SQL syntax.
func maskSQLStringsAndComments(sql string, maskQuotedIdentifiers bool) string {
	return maskSQL(sql, true, maskQuotedIdentifiers)
}

func maskSQLComments(sql string) string {
	return maskSQL(sql, false, false)
}

func maskSQL(sql string, maskStrings, maskQuotedIdentifiers bool) string {
	masked, _, _ := scanSQL(sql, maskStrings, maskQuotedIdentifiers)
	return masked
}

func containsExecutableSQLComment(sql string) bool {
	_, executableComment, _ := scanSQL(sql, false, false)
	return executableComment
}

func containsAmbiguousBackslashQuote(sql string) bool {
	_, _, ambiguousBackslashQuote := scanSQL(sql, false, false)
	return ambiguousBackslashQuote
}

func scanSQL(sql string, maskStrings, maskQuotedIdentifiers bool) (string, bool, bool) {
	masked := []byte(sql)
	var quote byte
	lineComment := false
	blockComment := false
	executableComment := false
	ambiguousBackslashQuote := false

	mask := func(i int) {
		if masked[i] != '\n' && masked[i] != '\r' {
			masked[i] = ' '
		}
	}

	for i := 0; i < len(masked); i++ {
		if lineComment {
			if masked[i] == '\n' || masked[i] == '\r' {
				lineComment = false
				continue
			}
			mask(i)
			continue
		}
		if blockComment {
			if masked[i] == '*' && i+1 < len(masked) && masked[i+1] == '/' {
				mask(i)
				mask(i + 1)
				i++
				blockComment = false
				continue
			}
			mask(i)
			continue
		}
		if quote != 0 {
			current := masked[i]
			maskQuote := maskStrings && (quote == '\'' || maskQuotedIdentifiers)
			if maskQuote {
				mask(i)
			}
			if current == '\\' && quote != '`' && i+1 < len(masked) {
				if masked[i+1] == quote {
					ambiguousBackslashQuote = true
				}
				i++
				if maskQuote {
					mask(i)
				}
				continue
			}
			if current == quote {
				if i+1 < len(masked) && masked[i+1] == quote {
					i++
					if maskQuote {
						mask(i)
					}
					continue
				}
				quote = 0
			}
			continue
		}
		if isMySQLWhitespaceOrControl(masked[i]) {
			if masked[i] != '\n' && masked[i] != '\r' {
				masked[i] = ' '
			}
			continue
		}

		switch masked[i] {
		case '\'', '"':
			quote = masked[i]
			if maskStrings && (quote == '\'' || maskQuotedIdentifiers) {
				mask(i)
			}
		case '`':
			quote = masked[i]
			if maskStrings && maskQuotedIdentifiers {
				mask(i)
			}
		case '#':
			lineComment = true
			mask(i)
		case '/':
			if i+1 < len(masked) && masked[i+1] == '*' {
				if i+2 < len(masked) && masked[i+2] == '!' {
					executableComment = true
				}
				if i+3 < len(masked) && (masked[i+2] == 'M' || masked[i+2] == 'm') && masked[i+3] == '!' {
					executableComment = true
				}
				blockComment = true
				mask(i)
				mask(i + 1)
				i++
			}
		case '-':
			if i+1 < len(masked) && masked[i+1] == '-' &&
				(i+2 == len(masked) || isMySQLWhitespaceOrControl(masked[i+2])) {
				lineComment = true
				mask(i)
				mask(i + 1)
				i++
			}
		}
	}

	return string(masked), executableComment, ambiguousBackslashQuote
}

func isMySQLWhitespaceOrControl(b byte) bool {
	return b <= ' ' || b == 0x7f
}

// CanUseOnlineDDL checks if the SQL statement can use Online DDL
// This is a simplified check - MySQL/MariaDB would actually validate this
func CanUseOnlineDDL(sql string) bool {
	upperSQL := strings.ToUpper(sql)

	// Some operations always require copy
	// Note: MODIFY can be used with or without COLUMN keyword
	notSupported := []string{
		"ADD FULLTEXT",
		"ADD SPATIAL",
		"DROP PRIMARY KEY",
		"MODIFY COLUMN",
		"MODIFY ", // MODIFY without COLUMN (with space to avoid false matches)
		"CHANGE COLUMN",
		"CHANGE ", // CHANGE without COLUMN
	}

	for _, op := range notSupported {
		if strings.Contains(upperSQL, op) {
			return false
		}
	}

	return true
}
