package config

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	logging "github.com/google/logger"
)

func TestLoadConfigFromStringParsesAwsRDSClusterParameterGroup(t *testing.T) {
	logger := *logging.Init("config-test", true, false, io.Discard)

	cfg, err := LoadConfigFromString(`aws_rds_cluster_parameter_group="aurora-cluster-custom"`, logger)
	if err != nil {
		t.Fatalf("LoadConfigFromString() error = %v", err)
	}

	if cfg.AwsRDSClusterParameterGroup != "aurora-cluster-custom" {
		t.Fatalf("AwsRDSClusterParameterGroup = %q, want %q", cfg.AwsRDSClusterParameterGroup, "aurora-cluster-custom")
	}
}

func TestLoadConfigFromStringAcceptsOldConfigWithoutAwsRDSClusterParameterGroup(t *testing.T) {
	logger := *logging.Init("config-test", true, false, io.Discard)

	cfg, err := LoadConfigFromString(`aws_rds_parameter_group="releem-agent"`, logger)
	if err != nil {
		t.Fatalf("LoadConfigFromString() error = %v", err)
	}

	if cfg.AwsRDSClusterParameterGroup != "" {
		t.Fatalf("AwsRDSClusterParameterGroup = %q, want empty for old configuration", cfg.AwsRDSClusterParameterGroup)
	}
}

func TestConfigJSONIncludesAwsRDSClusterParameterGroup(t *testing.T) {
	serialized, err := json.Marshal(Config{AwsRDSClusterParameterGroup: "aurora-cluster-custom"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	if !strings.Contains(string(serialized), `"AwsRDSClusterParameterGroup":"aurora-cluster-custom"`) {
		t.Fatalf("json.Marshal() = %s, want AwsRDSClusterParameterGroup field", serialized)
	}
}

func TestLoadConfigFromStringEnableExecDDLDefaultsToFalse(t *testing.T) {
	logger := *logging.Init("config-test", true, false, io.Discard)

	cfg, err := LoadConfigFromString("", logger)
	if err != nil {
		t.Fatalf("LoadConfigFromString() error = %v", err)
	}

	if cfg.EnableExecDDL {
		t.Fatal("EnableExecDDL = true, want false by default")
	}
}

func TestLoadConfigFromStringEnableExecDDLCanBeEnabled(t *testing.T) {
	logger := *logging.Init("config-test", true, false, io.Discard)

	cfg, err := LoadConfigFromString("enable_exec_ddl=true", logger)
	if err != nil {
		t.Fatalf("LoadConfigFromString() error = %v", err)
	}

	if !cfg.EnableExecDDL {
		t.Fatal("EnableExecDDL = false, want true")
	}
}

func TestLoadConfigFromStringTableSizeCacheDefaults(t *testing.T) {
	logger := *logging.Init("config-test", true, false, io.Discard)

	cfg, err := LoadConfigFromString("", logger)
	if err != nil {
		t.Fatalf("LoadConfigFromString() error = %v", err)
	}

	if cfg.TableSizeCacheTableThreshold != 10000 {
		t.Fatalf("TableSizeCacheTableThreshold = %d, want 10000", cfg.TableSizeCacheTableThreshold)
	}
	if cfg.TableSizeCacheRAMMultiplier != 4 {
		t.Fatalf("TableSizeCacheRAMMultiplier = %d, want 4", cfg.TableSizeCacheRAMMultiplier)
	}
	if cfg.TableSizeCacheTTL != 604800 {
		t.Fatalf("TableSizeCacheTTL = %d, want 604800", cfg.TableSizeCacheTTL)
	}
}

func TestLoadConfigFromStringTableSizeCacheValues(t *testing.T) {
	tests := []struct {
		name           string
		data           string
		wantThreshold  int64
		wantMultiplier int64
		wantTTL        time.Duration
	}{
		{
			name: "explicit values",
			data: `
table_size_cache_table_threshold=2500
table_size_cache_ram_multiplier=6
table_size_cache_ttl_seconds=172800
`,
			wantThreshold:  2500,
			wantMultiplier: 6,
			wantTTL:        172800,
		},
		{
			name: "zero values",
			data: `
table_size_cache_table_threshold=0
table_size_cache_ram_multiplier=0
table_size_cache_ttl_seconds=0
`,
			wantThreshold:  10000,
			wantMultiplier: 4,
			wantTTL:        604800,
		},
		{
			name: "negative values",
			data: `
table_size_cache_table_threshold=-1
table_size_cache_ram_multiplier=-1
table_size_cache_ttl_seconds=-1
`,
			wantThreshold:  10000,
			wantMultiplier: 4,
			wantTTL:        604800,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := *logging.Init("config-test", true, false, io.Discard)
			cfg, err := LoadConfigFromString(tt.data, logger)
			if err != nil {
				t.Fatalf("LoadConfigFromString() error = %v", err)
			}

			if cfg.TableSizeCacheTableThreshold != tt.wantThreshold {
				t.Fatalf("TableSizeCacheTableThreshold = %d, want %d", cfg.TableSizeCacheTableThreshold, tt.wantThreshold)
			}
			if cfg.TableSizeCacheRAMMultiplier != tt.wantMultiplier {
				t.Fatalf("TableSizeCacheRAMMultiplier = %d, want %d", cfg.TableSizeCacheRAMMultiplier, tt.wantMultiplier)
			}
			if cfg.TableSizeCacheTTL != tt.wantTTL {
				t.Fatalf("TableSizeCacheTTL = %d, want %d", cfg.TableSizeCacheTTL, tt.wantTTL)
			}
		})
	}
}
