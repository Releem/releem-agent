package phase2

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	alterOnlineDDLClausePattern = regexp.MustCompile(`(?i)^(?:ALGORITHM|LOCK)\b`)
	createIndexWaitPattern      = regexp.MustCompile(`(?i)\b(?:WAIT\s+\d+|NOWAIT)\b`)
	createIndexPTOSCPattern     = regexp.MustCompile("(?i)^\\s*CREATE\\s+((?:(?:UNIQUE|FULLTEXT|SPATIAL)\\s+)?INDEX\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?" + ddlIdentifierPattern + "(?:\\s+USING\\s+(?:BTREE|HASH|RTREE))?)\\s+ON\\s+(" + ddlQualifiedTablePattern + ")(?:\\s|\\(|$)")
)

func (e *Executor) executeWithPTOSC(options ExecuteOptions) error {
	// Perform dry-run first
	dryRunStage := e.beginStage(options, "ptosc_dry_run")
	if err := e.dryRunPTOSC(options); err != nil {
		dryRunStage.finish("failed", err.Error())
		return wrapExecutionStageError("ptosc_dry_run", err)
	}
	dryRunStage.finish("passed", "")

	// Execute actual change
	executionStage := e.beginStage(options, "ptosc_execution")
	err := e.runPTOSC(options)
	if err != nil {
		executionStage.finish("failed", err.Error())
		return wrapExecutionStageError("ptosc_execution", err)
	}
	executionStage.finish("passed", "")
	return nil
}

func (e *Executor) dryRunPTOSC(options ExecuteOptions) error {
	if options.Config == nil {
		return fmt.Errorf("config is required for pt-online-schema-change")
	}

	ptosc := options.Config.PTOSCPath
	if ptosc == "" {
		ptosc = "pt-online-schema-change"
	}

	host := options.Config.MysqlHost
	port := options.Config.MysqlPort
	user := options.Config.MysqlUser
	password := options.Config.MysqlPassword
	if host == "" {
		return fmt.Errorf("mysql_host is required for pt-online-schema-change")
	}

	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return err
	}

	alterSQL, err := buildPTOSCAlterSQL(options.SQL)
	if err != nil {
		return err
	}

	args := []string{
		"--dry-run",
		buildPTOSCDSN(host, port, user, password, tableInfo.Database, tableInfo.Table),
		fmt.Sprintf("--alter=%s", alterSQL),
	}

	if options.Debug {
		safeArgs := redactCommandArgs(args, password)
		e.debugf(options, "[DEBUG] pt-online-schema-change dry-run command: %s", ptosc)
		e.debugf(options, "[DEBUG] pt-online-schema-change dry-run args: %s", strings.Join(safeArgs, " "))
	}

	output, err := e.combinedOutput(ptosc, args...)
	e.logExternalCommandOutput(options, "pt-online-schema-change", "dry_run", output, err)

	if err != nil {
		return fmt.Errorf("pt-online-schema-change dry-run failed: %s", string(output))
	}

	return nil
}

func (e *Executor) runPTOSC(options ExecuteOptions) error {
	if options.Config == nil {
		return fmt.Errorf("config is required for pt-online-schema-change")
	}

	ptosc := options.Config.PTOSCPath
	if ptosc == "" {
		ptosc = "pt-online-schema-change"
	}

	host := options.Config.MysqlHost
	port := options.Config.MysqlPort
	user := options.Config.MysqlUser
	password := options.Config.MysqlPassword
	if host == "" {
		return fmt.Errorf("mysql_host is required for pt-online-schema-change")
	}

	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return err
	}

	alterSQL, err := buildPTOSCAlterSQL(options.SQL)
	if err != nil {
		return err
	}

	args := []string{
		"--execute",
		buildPTOSCDSN(host, port, user, password, tableInfo.Database, tableInfo.Table),
		fmt.Sprintf("--alter=%s", alterSQL),
	}

	if options.Debug {
		safeArgs := redactCommandArgs(args, password)
		e.debugf(options, "[DEBUG] pt-online-schema-change execute command: %s", ptosc)
		e.debugf(options, "[DEBUG] pt-online-schema-change execute args: %s", strings.Join(safeArgs, " "))
	}

	output, err := e.combinedOutput(ptosc, args...)
	e.logExternalCommandOutput(options, "pt-online-schema-change", "execute", output, err)

	if err != nil {
		return fmt.Errorf("pt-online-schema-change failed: %s", string(output))
	}

	return nil
}

func buildPTOSCDSN(host, port, user, password, database, table string) string {
	parts := make([]string, 0, 6)
	if strings.HasPrefix(host, "/") {
		parts = append(parts, "S="+host)
	} else {
		parts = append(parts, "h="+host, "P="+port)
	}
	parts = append(parts, "u="+user, "p="+password, "D="+database, "t="+table)
	return strings.Join(parts, ",")
}

func buildPTOSCAlterSQL(sql string) (string, error) {
	if containsExecutableSQLComment(sql) {
		return "", fmt.Errorf("executable SQL comments are not allowed in pt-online-schema-change input")
	}
	if containsAmbiguousBackslashQuote(sql) {
		return "", fmt.Errorf("backslash-escaped quotes are not allowed in pt-online-schema-change input because their meaning depends on sql_mode")
	}

	statement, _ := splitDDLStatementSuffix(sql)
	if statement == "" {
		return "", fmt.Errorf("empty SQL statement")
	}
	if strings.Contains(maskSQLStringsAndComments(statement, true), ";") {
		return "", fmt.Errorf("multiple SQL statements are not allowed in pt-online-schema-change input")
	}

	masked := maskSQLStringsAndComments(statement, false)
	if match := alterTableTargetPattern.FindStringSubmatchIndex(masked); len(match) >= 4 {
		modifiers := strings.Fields(masked[:match[2]])
		for i, modifier := range modifiers {
			if strings.EqualFold(modifier, "IGNORE") {
				return "", fmt.Errorf("ALTER IGNORE cannot be represented safely by pt-online-schema-change")
			}
			if strings.EqualFold(modifier, "IF") && i+1 < len(modifiers) && strings.EqualFold(modifiers[i+1], "EXISTS") {
				return "", fmt.Errorf("ALTER TABLE IF EXISTS cannot be represented safely by pt-online-schema-change")
			}
		}
		return stripAlterOnlineDDLClauses(statement[match[3]:])
	}

	match := createIndexPTOSCPattern.FindStringSubmatchIndex(masked)
	if len(match) < 6 {
		return "", fmt.Errorf("unsupported DDL for pt-online-schema-change; expected ALTER TABLE or CREATE INDEX")
	}
	definition := strings.TrimSpace(statement[match[2]:match[3]])
	tail := stripCreateIndexOnlineDDLClauses(statement[match[5]:])
	if tail == "" || !strings.HasPrefix(strings.TrimSpace(tail), "(") {
		return "", fmt.Errorf("invalid CREATE INDEX column definition")
	}
	if createIndexWaitPattern.MatchString(maskTopLevelSQL(tail)) {
		return "", fmt.Errorf("CREATE INDEX WAIT/NOWAIT cannot be represented safely by pt-online-schema-change")
	}
	return "ADD " + definition + " " + strings.TrimSpace(tail), nil
}

func stripAlterOnlineDDLClauses(body string) (string, error) {
	masked := maskSQLStringsAndComments(body, true)
	start := 0
	depth := 0
	kept := make([]string, 0, 4)

	appendClause := func(end int) {
		rawClause := strings.TrimSpace(body[start:end])
		maskedClause := strings.TrimSpace(masked[start:end])
		if rawClause != "" && !alterOnlineDDLClausePattern.MatchString(maskedClause) {
			kept = append(kept, rawClause)
		}
	}

	for i := range masked {
		switch masked[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				appendClause(i)
				start = i + 1
			}
		}
	}
	appendClause(len(body))

	if len(kept) == 0 {
		return "", fmt.Errorf("ALTER TABLE contains no change supported by pt-online-schema-change")
	}
	return strings.Join(kept, ", "), nil
}

func stripCreateIndexOnlineDDLClauses(tail string) string {
	masked := []byte(maskTopLevelSQL(tail))

	clausePattern := regexp.MustCompile(`(?i)\b(?:ALGORITHM|LOCK)\b\s*(?:=\s*|\s+)(?:DEFAULT|INPLACE|COPY|INSTANT|NOCOPY|NONE|SHARED|EXCLUSIVE)\b`)
	spans := clausePattern.FindAllStringIndex(string(masked), -1)
	if len(spans) == 0 {
		return strings.TrimSpace(tail)
	}

	var result strings.Builder
	last := 0
	for _, span := range spans {
		result.WriteString(tail[last:span[0]])
		last = span[1]
	}
	result.WriteString(tail[last:])
	return strings.TrimSpace(result.String())
}

func maskTopLevelSQL(sql string) string {
	masked := []byte(maskSQLStringsAndComments(sql, true))
	depth := 0
	for i := range masked {
		switch masked[i] {
		case '(':
			depth++
			masked[i] = ' '
		case ')':
			if depth > 0 {
				depth--
			}
			masked[i] = ' '
		default:
			if depth > 0 {
				masked[i] = ' '
			}
		}
	}
	return string(masked)
}
