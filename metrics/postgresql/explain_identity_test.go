package postgresql

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/Releem/mysqlconfigurer/config"
	u "github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
	"io"
	"testing"
)

func TestExplainReusesPlanAcrossExecutionLevels(t *testing.T) {
	logger := *logging.Init("review-dedup", false, false, io.Discard)
	defer logger.Close()
	rows := []PostgresQueryDetail{}
	for _, top := range []bool{true, false} {
		rows = append(rows, PostgresQueryDetail{PostgresQueryStats: PostgresQueryStats{Datname: "app", QueryID: postgresQueryIdentity("16384", "42", sql.NullBool{Bool: top, Valid: true}), PgQueryID: "42", TopLevel: sql.NullBool{Bool: top, Valid: true}, TotalExecTimeUS: 1000}, Query: "SELECT 1"})
	}
	attempts := 0
	recorder := &pgExplainRecordingDriver{explainHandler: func(string) (driver.Rows, error) {
		attempts++
		return &pgExplainRows{columns: []string{"QUERY PLAN"}, rows: [][]driver.Value{{"{}"}}}, nil
	}}
	db, err := openPgTestDB(recorder)
	if err != nil {
		t.Fatal(err)
	}
	state := u.NewExplainCollectionState()
	defer state.Close()
	state.Connect = func(*config.Config, logging.Logger, string) (*sql.DB, error) { return db, nil }
	details := postgresQueryDetailMap(rows)
	successful := collectExplainDetails(details, "total_exec_time_us", true, logger, &config.Config{}, state)
	t.Logf("identical SQL, pg_queryid=42, two execution levels: EXPLAIN calls=%d quota consumed=%d", attempts, successful)
	if attempts != 1 || successful != 1 {
		t.Fatalf("EXPLAIN calls=%d quota=%d; want one shared plan attempt", attempts, successful)
	}
	for _, row := range details {
		if row.Explain != "{}" {
			t.Errorf("level %v did not receive shared plan: %+v", row.TopLevel, row)
		}
	}
	if got := collectExplainDetails(details, "mean_exec_time_us", true, logger, &config.Config{}, state); got != 0 || attempts != 1 {
		t.Fatalf("second ranking repeated EXPLAIN: success=%d attempts=%d", got, attempts)
	}
}

func TestExplainSharedResultsAndQuotaAcrossRankings(t *testing.T) {
	for _, tc := range []struct {
		name, plan string
		fail       bool
		want       int
	}{{"success", "{}", false, 1}, {"error", "", true, 0}, {"empty", "", false, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			logger := logging.Init("shared-explain", false, false, io.Discard)
			defer logger.Close()
			calls := 0
			recorder := &pgExplainRecordingDriver{explainHandler: func(string) (driver.Rows, error) {
				calls++
				if tc.fail {
					return nil, errors.New("controlled explain failure")
				}
				return &pgExplainRows{columns: []string{"QUERY PLAN"}, rows: [][]driver.Value{{tc.plan}}}, nil
			}}
			db, err := openPgTestDB(recorder)
			if err != nil {
				t.Fatal(err)
			}
			state := u.NewExplainCollectionState()
			defer state.Close()
			state.Connect = func(*config.Config, logging.Logger, string) (*sql.DB, error) { return db, nil }
			input := []PostgresQueryDetail{}
			for _, top := range []bool{true, false} {
				level := sql.NullBool{Bool: top, Valid: true}
				input = append(input, PostgresQueryDetail{PostgresQueryStats: PostgresQueryStats{Datname: "app", QueryID: postgresQueryIdentity("16384", "42", level), PgQueryID: "42", TopLevel: level}, Query: "SELECT 1"})
			}
			details := postgresQueryDetailMap(input)
			got := collectExplainDetails(details, "total_exec_time_us", true, *logger, &config.Config{}, state)
			if got != tc.want || calls != 1 {
				t.Fatalf("first ranking successful=%d calls=%d, want %d/1", got, calls, tc.want)
			}
			if got := collectExplainDetails(details, "mean_exec_time_us", true, *logger, &config.Config{}, state); got != 0 || calls != 1 {
				t.Fatalf("second ranking successful=%d calls=%d, want 0/1", got, calls)
			}
			for _, detail := range details {
				if detail.Explain != tc.plan || (detail.ExplainError != "") != tc.fail {
					t.Errorf("sibling did not receive shared result: %+v", detail)
				}
			}
		})
	}
}

func TestExplainDifferentRepresentativeSQLIsNotShared(t *testing.T) {
	logger := logging.Init("different-explain", false, false, io.Discard)
	defer logger.Close()
	calls := 0
	recorder := &pgExplainRecordingDriver{explainHandler: func(string) (driver.Rows, error) {
		calls++
		return &pgExplainRows{columns: []string{"QUERY PLAN"}, rows: [][]driver.Value{{"{}"}}}, nil
	}}
	db, err := openPgTestDB(recorder)
	if err != nil {
		t.Fatal(err)
	}
	state := u.NewExplainCollectionState()
	defer state.Close()
	state.Connect = func(*config.Config, logging.Logger, string) (*sql.DB, error) { return db, nil }
	details := map[string]PostgresQueryDetail{
		"first":  {PostgresQueryStats: PostgresQueryStats{Datname: "app", QueryID: "one", PgQueryID: "42"}, Query: "SELECT 1"},
		"second": {PostgresQueryStats: PostgresQueryStats{Datname: "app", QueryID: "two", PgQueryID: "42"}, Query: "SELECT 2"},
	}
	if got := collectExplainDetails(details, "total_exec_time_us", true, *logger, &config.Config{}, state); got != 2 || calls != 2 {
		t.Fatalf("distinct SQL: successful=%d calls=%d, want 2/2", got, calls)
	}
}

func TestExplainSharesResultsWithSiblingsBeyondQuota(t *testing.T) {
	logger := logging.Init("shared-quota", false, false, io.Discard)
	defer logger.Close()
	calls := 0
	recorder := &pgExplainRecordingDriver{explainHandler: func(string) (driver.Rows, error) {
		calls++
		return &pgExplainRows{columns: []string{"QUERY PLAN"}, rows: [][]driver.Value{{"{}"}}}, nil
	}}
	db, err := openPgTestDB(recorder)
	if err != nil {
		t.Fatal(err)
	}
	state := u.NewExplainCollectionState()
	defer state.Close()
	state.Connect = func(*config.Config, logging.Logger, string) (*sql.DB, error) { return db, nil }
	input := []PostgresQueryDetail{}
	for i := 0; i < 101; i++ {
		for _, top := range []bool{true, false} {
			native := fmt.Sprint(i)
			level := sql.NullBool{Bool: top, Valid: true}
			input = append(input, PostgresQueryDetail{PostgresQueryStats: PostgresQueryStats{Datname: "app", PgQueryID: native, QueryID: postgresQueryIdentity("16384", native, level), TopLevel: level, TotalExecTimeUS: float64(101 - i)}, Query: fmt.Sprintf("SELECT id FROM table_%d", i)})
		}
	}
	details := postgresQueryDetailMap(input)
	got := collectExplainDetails(details, "total_exec_time_us", true, *logger, &config.Config{}, state)
	withPlans := 0
	for _, row := range details {
		if row.Explain != "" {
			withPlans++
		}
	}
	if got != 100 || calls != 100 || withPlans != 200 {
		t.Fatalf("quota: success=%d calls=%d rows with plans=%d; want 100/100/200", got, calls, withPlans)
	}
}
