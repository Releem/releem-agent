package utils

import (
	"database/sql"
	"errors"
	"testing"
)

func TestExplainCollectionStateCachesFailedDatabase(t *testing.T) {
	state := NewExplainCollectionState()
	attempts := 0
	connect := func() (*sql.DB, error) {
		attempts++
		return nil, errors.New("dial tcp: connection refused")
	}

	if db, err := state.Connection("app", connect); db != nil || err == nil {
		t.Fatalf("failed connection should return nil db and error")
	}
	if db, err := state.Connection("app", connect); db != nil || err == nil {
		t.Fatalf("repeated failed connection should return nil db and error")
	}
	if attempts != 1 {
		t.Fatalf("failed database should be attempted once, got %d", attempts)
	}
	if got := state.FailedReason("app"); got != "dial tcp: connection refused" {
		t.Fatalf("FailedReason = %q", got)
	}
	if got := ConnectionFailedExplainError(state.FailedReason("app")); got != "connection_failed: dial tcp: connection refused" {
		t.Fatalf("ConnectionFailedExplainError = %q", got)
	}
}

func TestExplainCollectionStateTryBeginAttemptDedupes(t *testing.T) {
	state := NewExplainCollectionState()
	if !state.TryBeginAttempt("app42") {
		t.Fatal("first attempt should be allowed")
	}
	if state.TryBeginAttempt("app42") {
		t.Fatal("duplicate attempt should be rejected")
	}
	if !state.TryBeginAttempt("app43") {
		t.Fatal("different key should be allowed")
	}
}
