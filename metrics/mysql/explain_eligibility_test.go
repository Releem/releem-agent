package mysql

import "testing"

func TestMySQLExplainableStatementsSelectAndWithOnly(t *testing.T) {
	for query, want := range map[string]bool{
		"SELECT * FROM orders":                             true,
		"  with recent AS (SELECT 1) SELECT * FROM recent": true,
		"/* app=checkout */ SELECT * FROM orders":          true,
		"-- trace\nSELECT 1":                               true,
		"INSERT INTO t(id) VALUES (1)":                     false,
		"UPDATE t SET x=1":                                 false,
		"DELETE FROM t":                                    false,
		"REPLACE INTO t VALUES (1)":                        false,
		"INSERT INTO t SELECT * FROM s":                    false,
		"EXPLAIN SELECT * FROM t":                          false,
		"":                                                 false,
	} {
		if got := isMySQLExplainableStatement(query); got != want {
			t.Fatalf("isMySQLExplainableStatement(%q) = %v, want %v", query, got, want)
		}
	}
}
