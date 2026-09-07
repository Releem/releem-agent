package tasks

import (
	"reflect"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
)

func TestLocalDynamicApplyQueries(t *testing.T) {
	tests := []struct {
		name            string
		databaseType    string
		recommendations models.MetricGroupValue
		current         models.MetricGroupValue
		want            []localDynamicApplyOperation
	}{
		{
			name:         "mysql sets only changed value",
			databaseType: "mysql",
			recommendations: models.MetricGroupValue{
				"max_connections":   "200",
				"thread_cache_size": "10",
			},
			current: models.MetricGroupValue{
				"max_connections":   "100",
				"thread_cache_size": "10",
			},
			want: []localDynamicApplyOperation{
				{query: "set global max_connections=200", parameterNames: []string{"max_connections"}},
			},
		},
		{
			name:         "postgres reloads once when dynamic value changed",
			databaseType: "postgresql",
			recommendations: models.MetricGroupValue{
				"work_mem":       "16MB",
				"shared_buffers": "256MB",
			},
			current: models.MetricGroupValue{
				"work_mem":       map[string]interface{}{"setting": "8MB"},
				"shared_buffers": map[string]interface{}{"setting": "128MB"},
			},
			want: []localDynamicApplyOperation{
				{
					query:          "SELECT pg_reload_conf()",
					parameterNames: []string{"shared_buffers", "work_mem"},
				},
			},
		},
		{
			name:         "postgres does not reload unchanged config",
			databaseType: "postgresql",
			recommendations: models.MetricGroupValue{
				"max_connections": "100",
			},
			current: models.MetricGroupValue{
				"max_connections": map[string]interface{}{"setting": "100"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := localDynamicApplyQueries(tt.databaseType, tt.recommendations, tt.current)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("localDynamicApplyQueries(%q, %v, %v) = %v, want %v", tt.databaseType, tt.recommendations, tt.current, got, tt.want)
			}
		})
	}
}
