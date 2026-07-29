package mysql

import (
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	u "github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

func TestCollectExplainCachesConnectionFailurePerSchema(t *testing.T) {
	logger := *logging.Init("mysql-explain-connection-failure-test", false, false, io.Discard)
	defer logger.Close()

	now := float64(time.Now().Unix())
	digests := map[string]models.MetricGroupValue{
		"blocked1": {
			"schema_name": "blocked", "query_id": "1", "query_text": "SELECT 1",
			"sum_time_us": 3, "avg_time_us": 3, "LAST_SEEN": now,
		},
		"blocked2": {
			"schema_name": "blocked", "query_id": "2", "query_text": "SELECT 2",
			"sum_time_us": 2, "avg_time_us": 2, "LAST_SEEN": now,
		},
	}

	state := u.NewExplainCollectionState()
	defer state.Close()
	connects := 0
	state.Connect = func(*config.Config, logging.Logger, string) (*sql.DB, error) {
		connects++
		return nil, errors.New("dial tcp: connection refused")
	}

	CollectExplain(digests, "sum_time_us", logger, &config.Config{}, state)

	if connects != 1 {
		t.Fatalf("blocked schema connects = %d, want 1", connects)
	}
	want := "connection_failed: dial tcp: connection refused"
	for _, key := range []string{"blocked1", "blocked2"} {
		if digests[key]["explain_error"] != want {
			t.Fatalf("%s explain_error = %#v, want %q", key, digests[key]["explain_error"], want)
		}
	}
}
