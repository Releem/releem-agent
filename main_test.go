package main

import (
	"reflect"
	"testing"
)

func TestParseConfigurationApplyMode(t *testing.T) {
	tests := []struct {
		name       string
		getConfig  bool
		args       []string
		wantMode   string
		wantRemain []string
		wantErr    bool
	}{
		{name: "legacy config download defaults to full", getConfig: true, wantMode: "full"},
		{name: "dynamic config download", getConfig: true, args: []string{"dynamic"}, wantMode: "dynamic"},
		{name: "full config download", getConfig: true, args: []string{"full"}, wantMode: "full"},
		{name: "invalid config mode", getConfig: true, args: []string{"partial"}, wantErr: true},
		{name: "service command remains untouched", args: []string{"status"}, wantRemain: []string{"status"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMode, gotRemain, err := parseConfigurationApplyMode(tt.getConfig, tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseConfigurationApplyMode(%t, %v) error = %v, wantErr %t", tt.getConfig, tt.args, err, tt.wantErr)
			}
			if gotMode != tt.wantMode {
				t.Errorf("parseConfigurationApplyMode(%t, %v) mode = %q, want %q", tt.getConfig, tt.args, gotMode, tt.wantMode)
			}
			if !reflect.DeepEqual(gotRemain, tt.wantRemain) {
				t.Errorf("parseConfigurationApplyMode(%t, %v) remaining = %v, want %v", tt.getConfig, tt.args, gotRemain, tt.wantRemain)
			}
		})
	}
}

func TestShouldRunOneShotMode(t *testing.T) {
	tests := []struct {
		name              string
		setConfig         bool
		getConfig         bool
		initialConfig     bool
		agentEvent        string
		agentTask         string
		serviceCommandLen int
		want              bool
	}{
		{
			name:              "runs daemon when no one-shot flags are set",
			serviceCommandLen: 0,
			want:              false,
		},
		{
			name:              "runs daemon management commands through service manager",
			setConfig:         true,
			serviceCommandLen: 1,
			want:              false,
		},
		{
			name:              "runs generate config directly",
			setConfig:         true,
			serviceCommandLen: 0,
			want:              true,
		},
		{
			name:              "runs download config directly",
			getConfig:         true,
			serviceCommandLen: 0,
			want:              true,
		},
		{
			name:              "runs initial config directly",
			initialConfig:     true,
			serviceCommandLen: 0,
			want:              true,
		},
		{
			name:              "runs event directly",
			agentEvent:        "config_applied",
			serviceCommandLen: 0,
			want:              true,
		},
		{
			name:              "runs named task directly",
			agentTask:         "queries_optimization",
			serviceCommandLen: 0,
			want:              true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRunOneShotMode(tt.serviceCommandLen, tt.setConfig, tt.getConfig, tt.initialConfig, tt.agentEvent, tt.agentTask)
			if got != tt.want {
				t.Fatalf("shouldRunOneShotMode() = %v, want %v", got, tt.want)
			}
		})
	}
}
