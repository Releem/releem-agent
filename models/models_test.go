package models

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMetricsJSONOmitsAbsentFailedDatabaseSchema(t *testing.T) {
	tests := []struct {
		name     string
		failures []string
	}{
		{name: "nil"},
		{name: "empty", failures: []string{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var metrics Metrics
			metrics.DB.FailedDatabaseSchema = test.failures

			payload, err := json.Marshal(metrics)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(payload, []byte(`"FailedDatabaseSchema"`)) {
				t.Fatalf("absent failure metadata must be omitted: %s", payload)
			}
		})
	}
}

func TestMetricsJSONIncludesFailedDatabaseSchemaWhenPresent(t *testing.T) {
	var metrics Metrics
	metrics.DB.FailedDatabaseSchema = []string{"information_schema_indexes"}

	payload, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(`"FailedDatabaseSchema":["information_schema_indexes"]`)) {
		t.Fatalf("failure metadata missing from payload: %s", payload)
	}
}
