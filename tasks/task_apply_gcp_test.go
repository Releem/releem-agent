package tasks

import (
	"reflect"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
	"google.golang.org/api/sqladmin/v1"
)

func TestFilterGCPRecommendationsByApplyMode(t *testing.T) {
	recommendations := models.MetricGroupValue{
		"dynamic_flag": "1",
		"restart_flag": "2",
		"unknown_flag": "3",
	}
	flags := []*sqladmin.Flag{
		{Name: "dynamic_flag", RequiresRestart: false},
		{Name: "restart_flag", RequiresRestart: true},
	}

	dynamic, unknown := filterGCPRecommendationsByApplyMode(recommendations, flags, "dynamic")
	if want := (models.MetricGroupValue{"dynamic_flag": "1"}); !reflect.DeepEqual(dynamic, want) {
		t.Errorf("filterGCPRecommendationsByApplyMode(dynamic) = %v, want %v", dynamic, want)
	}
	if want := []string{"unknown_flag"}; !reflect.DeepEqual(unknown, want) {
		t.Errorf("filterGCPRecommendationsByApplyMode(dynamic) unknown = %v, want %v", unknown, want)
	}

	full, unknown := filterGCPRecommendationsByApplyMode(recommendations, flags, "full")
	if !reflect.DeepEqual(full, recommendations) {
		t.Errorf("filterGCPRecommendationsByApplyMode(full) = %v, want %v", full, recommendations)
	}
	if len(unknown) != 0 {
		t.Errorf("filterGCPRecommendationsByApplyMode(full) unknown = %v, want none", unknown)
	}
}
