package postgresql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
)

func TestPgStatStatementsSeparatesExecutionLevelsWithoutUserID(t *testing.T) {
	for _, profile := range []postgresQueryProfile{postgresQueryLightweight, postgresQueryFull} {
		capabilities := PostgresCapabilitySnapshot{TimingColumn: "total_exec_time", PgStatStatementsRelation: `"public"."pg_stat_statements"`, PgStatStatementsColumns: map[string]struct{}{"toplevel": {}}}
		query := pgStatStatementsQuery(capabilities, profile)
		if !strings.Contains(query, "GROUP BY s.dbid, d.datname, s.queryid, s.toplevel") {
			t.Errorf("pgStatStatementsQuery(profile=%d) = %s, want groups separated by dbid/queryid/toplevel", profile, query)
		}
		if strings.Contains(query, "s.userid") {
			t.Errorf("pgStatStatementsQuery(profile=%d) includes userid; want roles aggregated", profile)
		}
	}
}

func TestPostgresQueryIdentityIsStableAndSeparatesExecutionContext(t *testing.T) {
	identity := postgresQueryIdentity("16384", "-3947659967107627740", sql.NullBool{Bool: true, Valid: true})
	if identity != "9435790011664841955" {
		t.Errorf("postgresQueryIdentity(16384, -3947659967107627740, sql.NullBool{Bool:true,Valid:true}) = %s, want 9435790011664841955", identity)
	}
	if identity == postgresQueryIdentity("16384", "-3947659967107627740", sql.NullBool{Bool: false, Valid: true}) {
		t.Fatal("nested and standalone statements share an identity")
	}
	if identity == postgresQueryIdentity("16385", "-3947659967107627740", sql.NullBool{Bool: true, Valid: true}) {
		t.Fatal("different database OIDs share an identity")
	}
	if identity != postgresQueryIdentity("16384", normalizePgQueryID("-3947659967107627740"), sql.NullBool{Bool: true, Valid: true}) {
		t.Fatal("signed and unsigned forms of the same PostgreSQL ID differ")
	}
}

func TestPostgresQueryProfilesPreserveSeparateStatementRows(t *testing.T) {
	for _, hasTopLevel := range []bool{false, true} {
		capabilities := PostgresCapabilitySnapshot{TimingColumn: "total_exec_time", HasRows: true, PgStatStatementsRelation: `"public"."pg_stat_statements"`, PgStatStatementsColumns: map[string]struct{}{}}
		if hasTopLevel {
			capabilities.PgStatStatementsColumns["toplevel"] = struct{}{}
		}
		recorder := &pgQueryIdentityDriver{query: func(query string) (driver.Rows, error) {
			topLevel := true
			if !hasTopLevel && strings.Contains(query, "s.toplevel") {
				t.Errorf("legacy query references absent toplevel column: %s", query)
			}
			full := strings.Contains(query, "min(s.query)")
			columns := []string{"datname", "dbid", "pg_queryid", "toplevel"}
			if full {
				columns = append(columns, "query")
			}
			columns = append(columns, "calls", "total_exec_time_us", "mean_exec_time_us")
			if full {
				columns = append(columns, "rows")
			}
			values := [][]driver.Value{}
			for _, nested := range []bool{false, true} {
				if !hasTopLevel && nested {
					continue
				}
				topLevel = !nested
				var level driver.Value
				if hasTopLevel {
					level = topLevel
				}
				row := []driver.Value{"app", "16384", "-42", level}
				if full {
					text := "SELECT id FROM orders"
					if nested {
						text = "EXPLAIN ANALYZE SELECT id FROM orders"
					}
					row = append(row, text)
				}
				row = append(row, int64(3), float64(9000), float64(3000))
				if full {
					row = append(row, int64(12))
				}
				values = append(values, row)
			}
			return &pgExplainRows{columns: columns, rows: values}, nil
		}}
		db, err := openPgTestDB(recorder)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		stats, err := collectPostgresQueryStats(context.Background(), db, capabilities)
		if err != nil {
			t.Fatal(err)
		}
		details, err := collectPostgresQueryDetails(context.Background(), db, capabilities)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if hasTopLevel {
			want = 2
		}
		if len(stats) != want || len(details) != want || len(postgresQueryDetailMap(details)) != want {
			t.Fatalf("hasTopLevel=%v: stats=%d details=%d map=%d, want %d", hasTopLevel, len(stats), len(details), len(postgresQueryDetailMap(details)), want)
		}
		for i, stat := range stats {
			detail := details[i]
			if stat.QueryID != detail.QueryID || stat.PgQueryID != "-42" || stat.PgQueryID != detail.PgQueryID || stat.TopLevel != detail.TopLevel {
				t.Fatalf("identity mismatch: stats=%+v detail=%+v", stat, detail)
			}
			if stat.Calls != 3 || detail.Calls != 3 {
				t.Fatalf("statistics were mixed: stats=%+v detail=%+v", stat, detail)
			}
			var level interface{}
			if stat.TopLevel.Valid {
				level = stat.TopLevel.Bool
			}
			if stat.TopLevel.Valid != hasTopLevel {
				t.Fatalf("hasTopLevel=%v: got level=%+v", hasTopLevel, stat.TopLevel)
			}
			for _, metric := range []models.MetricGroupValue{stat.metricGroupValue(), detail.metricGroupValue()} {
				if metric["pg_queryid"] != "-42" || metric["toplevel"] != level || metric["queryid"] != stat.QueryID {
					t.Errorf("identity metadata not preserved: %#v", metric)
				}
			}
		}
	}
}

type pgQueryIdentityDriver struct {
	query func(string) (driver.Rows, error)
}

func (d *pgQueryIdentityDriver) Open(string) (driver.Conn, error) {
	return &pgQueryIdentityConn{query: d.query}, nil
}

type pgQueryIdentityConn struct {
	pgBaseLightweightConn
	query func(string) (driver.Rows, error)
}

func (c *pgQueryIdentityConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	return c.query(query)
}

// This optional integration test reads an existing PGSS entry; it never resets
// statistics or creates a workload on the target database.
func TestPostgresQueryIdentityIntegration(t *testing.T) {
	sourceID := os.Getenv("RELEEM_TEST_POSTGRES_QUERYID")
	if sourceID == "" {
		t.Skip("RELEEM_TEST_POSTGRES_QUERYID is not set")
	}
	db := openIntegrationPostgres(t)
	logger := logging.Init("query-identity-integration", false, false, io.Discard)
	defer logger.Close()
	caps, err := NewPostgresCapabilities(*logger).Resolve(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := caps.PgStatStatementsColumns["toplevel"]; !ok {
		t.Skip("PGSS has no toplevel column")
	}
	var databaseID, datname string
	if err := db.QueryRow("SELECT oid::text, datname FROM pg_database WHERE datname=current_database()").Scan(&databaseID, &datname); err != nil {
		t.Fatal(err)
	}
	full, err := collectPostgresQueryDetails(context.Background(), db, caps)
	if err != nil {
		t.Fatal(err)
	}
	light, err := collectPostgresQueryStats(context.Background(), db, caps)
	if err != nil {
		t.Fatal(err)
	}
	details := []PostgresQueryDetail{}
	ids := map[string]sql.NullBool{}
	for _, row := range full {
		if row.Datname != datname || row.PgQueryID != sourceID {
			continue
		}
		details = append(details, row)
		ids[row.QueryID] = row.TopLevel
		if row.QueryID != postgresQueryIdentity(databaseID, sourceID, row.TopLevel) {
			t.Fatalf("unexpected identity: %+v", row)
		}
		found := false
		for _, stat := range light {
			if stat.Datname == datname && stat.QueryID == row.QueryID && stat.PgQueryID == sourceID && stat.TopLevel == row.TopLevel {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("full statement missing from lightweight profile: %+v", row)
		}
		t.Logf("pg_queryid=%s toplevel=%v queryid=%s calls=%d", row.PgQueryID, row.TopLevel, row.QueryID, row.Calls)
	}
	if len(details) != 2 || len(ids) != 2 || len(postgresQueryDetailMap(details)) != 2 {
		t.Fatalf("source query %s: got %d rows, %d IDs, %d map entries; want two separate execution levels", sourceID, len(details), len(ids), len(postgresQueryDetailMap(details)))
	}
	// Simulate an older extension's projection on the same live data: one
	// aggregated unknown-level row, without claiming that it was standalone.
	legacyCaps := caps
	legacyCaps.PgStatStatementsColumns = nil
	legacy, err := collectPostgresQueryDetails(context.Background(), db, legacyCaps)
	if err != nil {
		t.Fatal(err)
	}
	unknownRows := 0
	for _, row := range legacy {
		if row.Datname != datname || row.PgQueryID != sourceID {
			continue
		}
		unknownRows++
		if row.TopLevel.Valid || row.metricGroupValue()["toplevel"] != nil {
			t.Fatalf("legacy row claims a known level: %+v", row)
		}
		if _, exists := ids[row.QueryID]; exists {
			t.Fatal("unknown-level identity matches a known-level identity")
		}
	}
	if unknownRows != 1 {
		t.Fatalf("legacy source query %s: rows=%d, want one aggregated unknown-level row", sourceID, unknownRows)
	}
	levels := map[sql.NullBool]bool{}
	for _, top := range ids {
		levels[top] = true
	}
	if len(levels) != 2 {
		t.Fatalf("source query %s: got levels %v, want true and false", sourceID, levels)
	}
}

func TestLegacyPostgresExecutionLevelIsUnknown(t *testing.T) {
	caps := PostgresCapabilitySnapshot{TimingColumn: "total_time", PgStatStatementsRelation: `"public"."pg_stat_statements"`}
	query := pgStatStatementsQuery(caps, postgresQueryFull)
	if !strings.Contains(query, "NULL::boolean AS toplevel") {
		t.Fatalf("legacy query claims a known execution level: %s", query)
	}
}

func TestUnknownPostgresExecutionLevelHasSeparateIdentity(t *testing.T) {
	unknown := postgresQueryIdentity("16384", "42", sql.NullBool{})
	for _, top := range []bool{false, true} {
		if unknown == postgresQueryIdentity("16384", "42", sql.NullBool{Bool: top, Valid: true}) {
			t.Fatalf("unknown level shares identity with %v", top)
		}
	}
}
