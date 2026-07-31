package config

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

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
