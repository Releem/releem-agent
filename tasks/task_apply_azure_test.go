package tasks

import (
	"encoding/json"
	"math"
	"testing"
)

type azureConfigString string

func TestMySQLConfigValueToStringPreservesAzureFormattingPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value interface{}
		want  string
	}{
		{name: "nil", value: nil, want: ""},
		{name: "trimmed string", value: " 100 ", want: "100"},
		{name: "true", value: true, want: "ON"},
		{name: "false", value: false, want: "OFF"},
		{name: "int", value: int(-1), want: "-1"},
		{name: "int8", value: int8(-8), want: "-8"},
		{name: "int16", value: int16(-16), want: "-16"},
		{name: "int32", value: int32(-32), want: "-32"},
		{name: "int64", value: int64(math.MinInt64), want: "-9223372036854775808"},
		{name: "uint", value: uint(1), want: "1"},
		{name: "uint8", value: uint8(8), want: "8"},
		{name: "uint16", value: uint16(16), want: "16"},
		{name: "uint32", value: uint32(32), want: "32"},
		{name: "uint64", value: uint64(math.MaxUint64), want: "18446744073709551615"},
		{name: "integral float32", value: float32(100), want: "100"},
		{name: "fractional float32", value: float32(1.234567), want: "1.234567"},
		{name: "integral float64", value: float64(-100), want: "-100"},
		{name: "fractional float64", value: 1.23456789012345, want: "1.23456789012345"},
		{name: "JSON number fallback", value: json.Number("9007199254740993"), want: "9007199254740993"},
		{name: "named string fallback", value: azureConfigString(" custom "), want: "custom"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := mysqlConfigValueToString(test.value)
			if got != test.want {
				t.Errorf("mysqlConfigValueToString(%T(%v)) = %q, want %q", test.value, test.value, got, test.want)
			}
		})
	}
}
