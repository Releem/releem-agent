package mysql

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
	drivermysql "github.com/go-sql-driver/mysql"
)

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
