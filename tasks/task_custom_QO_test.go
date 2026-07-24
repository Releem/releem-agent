package tasks

import (
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/config"
	logging "github.com/google/logger"
)

func TestExecuteQueryExplainReturnsConnectionErrorWithoutNilHandlePanic(t *testing.T) {
	wantErr := errors.New("connection failed")
	logger := *logging.Init("custom-query-explain-test", false, false, io.Discard)
	defer logger.Close()

	explain, err := executeQueryExplain(
		func(*config.Config, logging.Logger, string) (*sql.DB, error) {
			return nil, wantErr
		},
		&config.Config{},
		logger,
		"app",
		"SELECT 1",
	)

	if explain != "" {
		t.Fatalf("explain = %q, want empty", explain)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestExecuteQueryExplainRejectsNilHandleWithoutConnectorError(t *testing.T) {
	logger := *logging.Init("custom-query-explain-test", false, false, io.Discard)
	defer logger.Close()

	explain, err := executeQueryExplain(
		func(*config.Config, logging.Logger, string) (*sql.DB, error) {
			return nil, nil
		},
		&config.Config{},
		logger,
		"app",
		"SELECT 1",
	)

	if explain != "" {
		t.Fatalf("explain = %q, want empty", explain)
	}
	if err == nil || !strings.Contains(err.Error(), "nil database handle") {
		t.Fatalf("error = %v, want nil database handle error", err)
	}
}
