package postgresql

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
)

type pgDatabaseLifecycleTestConnection struct {
	closed bool
}

func (connection *pgDatabaseLifecycleTestConnection) Query(string, ...interface{}) (*sql.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (connection *pgDatabaseLifecycleTestConnection) QueryRow(string, ...interface{}) *sql.Row {
	return nil
}

func (connection *pgDatabaseLifecycleTestConnection) Close() error {
	connection.closed = true
	return nil
}

func TestPgStatActivityQueryHandlesNullableConnectionFields(t *testing.T) {
	source, err := os.ReadFile("dbMetrics.go")
	if err != nil {
		t.Fatal(err)
	}

	query := string(source)
	if !strings.Contains(query, "COALESCE(host(client_addr), 'local')") {
		t.Fatal("client_address should not become NULL when client_addr is NULL")
	}

	if !strings.Contains(query, "CONCAT_WS(' ', wait_event_type::text, wait_event::text) AS wait_event") {
		t.Fatal("wait_event should preserve partial wait event data when one column is NULL")
	}

	if !strings.Contains(query, "WHERE state IS NOT NULL") {
		t.Fatal("pg_stat_activity query should filter rows with incomplete process state")
	}
}

func TestForEachPGDatabaseClosesImmediatelyAndContinuesAfterCollectionError(t *testing.T) {
	connections := map[string]*pgDatabaseLifecycleTestConnection{
		"broken":  {},
		"healthy": {},
	}
	var collected []string
	var logged []string

	forEachPGDatabase(
		[]string{"broken", "healthy"},
		func(database string) pgDatabaseConnection {
			if database == "healthy" && !connections["broken"].closed {
				t.Fatal("previous database connection must be closed before opening the next database")
			}
			return connections[database]
		},
		func(database string, _ pgDatabaseConnection) error {
			collected = append(collected, database)
			if database == "broken" {
				return errors.New("pg_stat_user_tables unavailable")
			}
			return nil
		},
		func(database string, err error) {
			logged = append(logged, database+": "+err.Error())
		},
	)

	if got := strings.Join(collected, ","); got != "broken,healthy" {
		t.Fatalf("collection should continue with the next database, got %s", got)
	}
	if !connections["broken"].closed || !connections["healthy"].closed {
		t.Fatalf("every database connection must be closed, got %#v", connections)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "broken: pg_stat_user_tables unavailable") {
		t.Fatalf("collection error should be logged once, got %#v", logged)
	}
}

func TestAddPGDatabaseTableCountDoesNotReusePreviousValueAfterError(t *testing.T) {
	var total uint64
	if err := addPGDatabaseTableCount(&total, func(value *uint64) error {
		*value = 100
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := addPGDatabaseTableCount(&total, func(*uint64) error {
		return errors.New("count failed")
	}); err == nil {
		t.Fatal("count error should be returned")
	}
	if total != 100 {
		t.Fatalf("failed database must not reuse the previous count, got %d", total)
	}
}

func TestDbMetricsUsesCapabilityInfoRelation(t *testing.T) {
	source, err := os.ReadFile("dbMetrics.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(source)
	if !strings.Contains(code, "capabilities.PgStatStatementsInfoRelation") ||
		!strings.Contains(code, `FROM "+capabilities.PgStatStatementsInfoRelation`) {
		t.Fatal("pg_stat_statements_info query must use the centrally detected extension relation")
	}
}
