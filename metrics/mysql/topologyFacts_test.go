package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Releem/mysqlconfigurer/models"
	drivermysql "github.com/go-sql-driver/mysql"
)

type factsConnector struct {
	query func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
}

func (c factsConnector) Connect(context.Context) (driver.Conn, error) { return factsConnection{c}, nil }
func (c factsConnector) Driver() driver.Driver                        { return factsDriver{} }

type factsDriver struct{}

func (factsDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type factsConnection struct{ factsConnector }

func (c factsConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c factsConnection) Close() error              { return nil }
func (c factsConnection) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c factsConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, query, args)
}

type factsRows struct {
	remaining int
	err       error
	closed    bool
}

func (r *factsRows) Columns() []string { return []string{"value", "null_value"} }
func (r *factsRows) Close() error      { r.closed = true; return nil }
func (r *factsRows) Next(dest []driver.Value) error {
	if r.remaining == 0 {
		if r.err != nil {
			return r.err
		}
		return io.EOF
	}
	r.remaining--
	dest[0], dest[1] = []byte("raw"), nil
	return nil
}

func TestTopologySQLScannerIsBoundedAndClosesRows(t *testing.T) {
	for _, test := range []struct {
		name      string
		count     int
		err       error
		wantError bool
	}{
		{"success", 1, nil, false}, {"empty", 0, nil, false},
		{"iteration error", 1, io.ErrUnexpectedEOF, true}, {"row limit", maxTopologyRows + 1, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := &factsRows{remaining: test.count, err: test.err}
			db := sql.OpenDB(factsConnector{query: func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > topologyQueryTimeout {
					t.Error("missing bounded query context")
				}
				if query != "SELECT ?" || len(args) != 1 || args[0].Value != "bound" {
					t.Error("query arguments lost")
				}
				return rows, nil
			}})
			previous := models.DB
			models.DB = db
			t.Cleanup(func() { models.DB = previous; db.Close() })
			got, err := queryTopologyStringRows("SELECT ?", "bound")
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v", err)
			}
			if test.wantError && got != nil {
				t.Fatal("incomplete rows published")
			}
			if !test.wantError && test.count == 1 && (got[0]["value"] != "raw" || got[0]["null_value"] != "") {
				t.Fatalf("scanner conversion: %v", got)
			}
			if !rows.closed {
				t.Fatal("rows not closed")
			}
		})
	}
}

func TestTopologyQueryCompatibility(t *testing.T) {
	for _, variables := range []map[string]string{{"version": "10.11-MariaDB"}, {"version_comment": "MariaDB Server"}} {
		if got := replicaStatusQueries(variables); len(got) != 4 || got[0] != "SHOW ALL REPLICAS STATUS" || got[1] != "SHOW ALL SLAVES STATUS" {
			t.Fatalf("MariaDB query order: %v", got)
		}
	}
	statements := replicaStatusQueries(map[string]string{"version": "8.0.40"})
	if len(statements) != 2 || statements[0] != "SHOW REPLICA STATUS" || statements[1] != "SHOW SLAVE STATUS" {
		t.Fatalf("MySQL query order: %v", statements)
	}
	calls := 0
	_, status := collectTopologyQuery(func(string, ...any) ([]map[string]string, error) {
		calls++
		if calls == 1 {
			return nil, &drivermysql.MySQLError{Number: 1064}
		}
		return nil, nil
	}, []string{"unsupported", "empty", "must not run"})
	if status != "ok" || calls != 2 {
		t.Fatalf("fallback/empty stop: %s %d", status, calls)
	}
	calls = 0
	_, status = collectTopologyQuery(func(string, ...any) ([]map[string]string, error) {
		calls++
		return nil, &drivermysql.MySQLError{Number: 1142}
	}, statements)
	if status != "error" || calls != 1 {
		t.Fatal("permission error triggered fallback")
	}
}

func TestTopologySQLQueryDeadlineCancelsDriver(t *testing.T) {
	db := sql.OpenDB(factsConnector{query: func(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	previous := models.DB
	models.DB = db
	t.Cleanup(func() { models.DB = previous; db.Close() })
	rows, err := queryTopologyStringRows("SELECT blocked")
	if !errors.Is(err, context.DeadlineExceeded) || rows != nil {
		t.Fatalf("deadline: %v %v", rows, err)
	}
}

func TestCollectTopologyFactsIsRawAndVersioned(t *testing.T) {
	query := func(sql string, args ...any) ([]map[string]string, error) {
		if strings.Contains(sql, "REPLICAS STATUS") {
			return []map[string]string{{"Master_Host": "source", "Slave_IO_Running": "No", "Master_User": "private-user", "Last_SQL_Error": "private sql"}}, nil
		}
		return nil, errors.New("unexpected query")
	}
	facts := collectTopologyFacts(map[string]string{"version": "10.11-MariaDB"}, query)
	if facts["Version"] != 1 {
		t.Fatalf("version: %v", facts)
	}
	sources := facts["Sources"].(map[string]string)
	if sources["ReplicaStatus"] != "ok" || sources["GroupMembers"] != "unsupported" || sources["InnoDBMetadata"] != "unsupported" {
		t.Fatalf("sources: %v", sources)
	}
	metrics := models.Metrics{}
	metrics.DB.TopologyFacts = facts
	data, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-user", "private sql", `"IsWriter"`, `"Role"`, `"Relations"`, `"Topology":`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("unexpected %s in %s", forbidden, data)
		}
	}
	if !strings.Contains(string(data), `"Slave_IO_Running":"No"`) {
		t.Fatal("raw status lost")
	}
}

func TestInnoDBFactsKeepRawTablesAcrossVersions(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			data, err := os.ReadFile("testdata/innodb_cluster/schema_" + version + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Columns map[string][]string            `json:"columns"`
				Rows    map[string][]map[string]string `json:"rows"`
				Version string                         `json:"schema_version"`
			}
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			failTable := ""
			query := func(statement string, _ ...any) ([]map[string]string, error) {
				if strings.Contains(statement, "information_schema.schemata") {
					return []map[string]string{{"SCHEMA_NAME": "mysql_innodb_cluster_metadata"}}, nil
				}
				if strings.Contains(statement, "information_schema.columns") {
					rows := []map[string]string{}
					for table, cols := range fixture.Columns {
						for _, col := range cols {
							rows = append(rows, map[string]string{"TABLE_NAME": table, "COLUMN_NAME": col})
						}
					}
					return rows, nil
				}
				match := regexp.MustCompile("FROM `mysql_innodb_cluster_metadata`.`([^`]+)`").FindStringSubmatch(statement)
				if len(match) != 2 {
					return nil, errors.New("unexpected query: " + statement)
				}
				if match[1] == failTable {
					return nil, errors.New("read failed")
				}
				return fixture.Rows[match[1]], nil
			}
			got, status := collectInnoDBFacts(query)
			if status != "ok" || got["SchemaVersion"] != fixture.Version {
				t.Fatalf("snapshot: %v %s", got, status)
			}
			tables := got["Tables"].(map[string][]map[string]string)
			if len(tables) != len(fixture.Rows) {
				t.Fatalf("lost tables: %v", tables)
			}
			for table, rows := range fixture.Rows {
				if len(tables[table]) != len(rows) {
					t.Errorf("lost raw rows in %s", table)
				}
			}
			failTable = "instances"
			if version == "v2" {
				failTable = "v2_instances"
			}
			partial, state := collectInnoDBFacts(query)
			if state != "error" || len(partial["Tables"].(map[string][]map[string]string)) < 2 {
				t.Fatal("partial metadata not marked incomplete")
			}
			delete(fixture.Columns, failTable)
			failTable = ""
			if _, state := collectInnoDBFacts(query); state != "error" {
				t.Fatal("missing metadata shape marked complete")
			}
			fixture.Rows["schema_version"][0]["major"] = "99"
			if _, state := collectInnoDBFacts(query); state != "error" {
				t.Fatal("unknown metadata version marked complete")
			}
		})
	}
}

func TestInnoDBFactsDistinguishHiddenSchema(t *testing.T) {
	for _, test := range []struct {
		number uint16
		want   string
	}{{1049, "unsupported"}, {1044, "error"}, {1142, "error"}} {
		_, status := collectInnoDBFacts(func(statement string, _ ...any) ([]map[string]string, error) {
			if strings.Contains(statement, "information_schema.schemata") {
				return nil, nil
			}
			return nil, &drivermysql.MySQLError{Number: test.number}
		})
		if status != test.want {
			t.Fatalf("error %d => %s want %s", test.number, status, test.want)
		}
	}
}

func TestTopologyQueryAbsenceAndFailureDiffer(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"empty", nil, "ok"},
		{"unsupported", &drivermysql.MySQLError{Number: 1064}, "unsupported"},
		{"denied", &drivermysql.MySQLError{Number: 1142}, "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, status := collectTopologyQuery(func(string, ...any) ([]map[string]string, error) { return []map[string]string{}, test.err }, []string{"first", "second"})
			if status != test.want {
				t.Fatalf("status %s want %s", status, test.want)
			}
		})
	}
}
