package mysql

import (
	"strings"
	"testing"
)

func TestMysqlSchemaTableQueryCollectsAutoIncrementValue(t *testing.T) {
	if !strings.Contains(mysqlTableSchemaSelectQuery, "AUTO_INCREMENT") {
		t.Fatalf("mysql table schema query should collect AUTO_INCREMENT for exhaustion checks: %s", mysqlTableSchemaSelectQuery)
	}
}

func TestMysqlSchemaIndexQueryCollectsVisibility(t *testing.T) {
	if !strings.Contains(mysqlIndexSchemaSelectQuery, "IS_VISIBLE") {
		t.Fatalf("mysql index schema query should collect IS_VISIBLE for index identity checks: %s", mysqlIndexSchemaSelectQuery)
	}
}
