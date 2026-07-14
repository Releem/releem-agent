package config

import (
	"io"
	"testing"

	logging "github.com/google/logger"
)

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
