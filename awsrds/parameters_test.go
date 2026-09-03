package awsrds

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
)

type parameterCall struct {
	scope  string
	group  string
	marker string
}

type parameterClientFake struct {
	instancePages map[string]*rds.DescribeDBParametersOutput
	clusterPages  map[string]*rds.DescribeDBClusterParametersOutput
	instanceErrs  map[string]error
	clusterErrs   map[string]error
	calls         []parameterCall
}

func (f *parameterClientFake) DescribeDBParameters(_ context.Context, input *rds.DescribeDBParametersInput, _ ...func(*rds.Options)) (*rds.DescribeDBParametersOutput, error) {
	marker := aws.ToString(input.Marker)
	f.calls = append(f.calls, parameterCall{
		scope:  "instance",
		group:  aws.ToString(input.DBParameterGroupName),
		marker: marker,
	})
	return f.instancePages[marker], f.instanceErrs[marker]
}

func (f *parameterClientFake) DescribeDBClusterParameters(_ context.Context, input *rds.DescribeDBClusterParametersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClusterParametersOutput, error) {
	marker := aws.ToString(input.Marker)
	f.calls = append(f.calls, parameterCall{
		scope:  "cluster",
		group:  aws.ToString(input.DBClusterParameterGroupName),
		marker: marker,
	})
	return f.clusterPages[marker], f.clusterErrs[marker]
}

func (f *parameterClientFake) DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	return nil, errors.New("parameterClientFake: DescribeDBInstances not implemented")
}

func (f *parameterClientFake) DescribeDBClusters(context.Context, *rds.DescribeDBClustersInput, ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	return nil, errors.New("parameterClientFake: DescribeDBClusters not implemented")
}

func (f *parameterClientFake) DescribeGlobalClusters(context.Context, *rds.DescribeGlobalClustersInput, ...func(*rds.Options)) (*rds.DescribeGlobalClustersOutput, error) {
	return nil, errors.New("parameterClientFake: DescribeGlobalClusters not implemented")
}

func (f *parameterClientFake) ModifyDBParameterGroup(context.Context, *rds.ModifyDBParameterGroupInput, ...func(*rds.Options)) (*rds.ModifyDBParameterGroupOutput, error) {
	return nil, errors.New("parameterClientFake: ModifyDBParameterGroup not implemented")
}

func (f *parameterClientFake) ModifyDBClusterParameterGroup(context.Context, *rds.ModifyDBClusterParameterGroupInput, ...func(*rds.Options)) (*rds.ModifyDBClusterParameterGroupOutput, error) {
	return nil, errors.New("parameterClientFake: ModifyDBClusterParameterGroup not implemented")
}

func TestListParametersPaginatesAndIndexesLiveFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scope     Scope
		group     string
		client    *parameterClientFake
		wantCalls []parameterCall
		want      map[string]ParameterInfo
	}{
		{
			name:  "instance parameter group",
			scope: ScopeInstance,
			group: "orders-instance-custom",
			client: &parameterClientFake{instancePages: map[string]*rds.DescribeDBParametersOutput{
				"": {
					Parameters: []types.Parameter{{
						ParameterName:        aws.String("max_connections"),
						ApplyType:            aws.String("dynamic"),
						IsModifiable:         aws.Bool(true),
						SupportedEngineModes: []string{"provisioned", "serverless"},
					}},
					Marker: aws.String("instance-next"),
				},
				"instance-next": {
					Parameters: []types.Parameter{{
						ParameterName: aws.String("innodb_log_buffer_size"),
						ApplyType:     aws.String("static"),
						IsModifiable:  aws.Bool(false),
					}},
				},
			}},
			wantCalls: []parameterCall{
				{scope: "instance", group: "orders-instance-custom"},
				{scope: "instance", group: "orders-instance-custom", marker: "instance-next"},
			},
			want: map[string]ParameterInfo{
				"max_connections": {
					Name:                 "max_connections",
					ApplyType:            "dynamic",
					IsModifiable:         true,
					SupportedEngineModes: []string{"provisioned", "serverless"},
				},
				"innodb_log_buffer_size": {
					Name:         "innodb_log_buffer_size",
					ApplyType:    "static",
					IsModifiable: false,
				},
			},
		},
		{
			name:  "cluster parameter group",
			scope: ScopeCluster,
			group: "orders-cluster-custom",
			client: &parameterClientFake{clusterPages: map[string]*rds.DescribeDBClusterParametersOutput{
				"": {
					Parameters: []types.Parameter{{
						ParameterName:        aws.String("time_zone"),
						ApplyType:            aws.String("dynamic"),
						IsModifiable:         aws.Bool(true),
						SupportedEngineModes: []string{"provisioned"},
					}},
					Marker: aws.String("cluster-next"),
				},
				"cluster-next": {
					Parameters: []types.Parameter{{
						ParameterName: aws.String("binlog_format"),
						ApplyType:     aws.String("static"),
						IsModifiable:  aws.Bool(true),
					}},
				},
			}},
			wantCalls: []parameterCall{
				{scope: "cluster", group: "orders-cluster-custom"},
				{scope: "cluster", group: "orders-cluster-custom", marker: "cluster-next"},
			},
			want: map[string]ParameterInfo{
				"time_zone": {
					Name:                 "time_zone",
					ApplyType:            "dynamic",
					IsModifiable:         true,
					SupportedEngineModes: []string{"provisioned"},
				},
				"binlog_format": {
					Name:         "binlog_format",
					ApplyType:    "static",
					IsModifiable: true,
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ListParameters(context.Background(), tt.client, tt.group, tt.scope)
			if err != nil {
				t.Fatalf("ListParameters() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ListParameters() = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.client.calls, tt.wantCalls) {
				t.Fatalf("calls = %#v, want %#v", tt.client.calls, tt.wantCalls)
			}
		})
	}
}

func TestListParametersRejectsRepeatedMarkers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		scope  Scope
		client *parameterClientFake
	}{
		{
			name:  "instance marker",
			scope: ScopeInstance,
			client: &parameterClientFake{instancePages: map[string]*rds.DescribeDBParametersOutput{
				"":       {Marker: aws.String("repeat")},
				"repeat": {Marker: aws.String("repeat")},
			}},
		},
		{
			name:  "cluster marker",
			scope: ScopeCluster,
			client: &parameterClientFake{clusterPages: map[string]*rds.DescribeDBClusterParametersOutput{
				"":       {Marker: aws.String("repeat")},
				"repeat": {Marker: aws.String("repeat")},
			}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ListParameters(context.Background(), tt.client, "custom", tt.scope)
			if err == nil || !strings.Contains(err.Error(), `repeated marker "repeat"`) {
				t.Fatalf("ListParameters() error = %v, want repeated-marker error", err)
			}
			if len(tt.client.calls) != 2 {
				t.Fatalf("calls = %d, want 2 before repeated marker is rejected", len(tt.client.calls))
			}
		})
	}
}

func TestListParametersWrapsPageErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("AWS describe failed")
	client := &parameterClientFake{
		instancePages: map[string]*rds.DescribeDBParametersOutput{"": {Marker: aws.String("next")}},
		instanceErrs:  map[string]error{"next": wantErr},
	}

	_, err := ListParameters(context.Background(), client, "orders-custom", ScopeInstance)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ListParameters() error = %v, want wrapped %v", err, wantErr)
	}
}

type simpleParameter struct {
	name   string
	value  string
	method types.ApplyMethod
}

func TestBuildApplyPlanRoutesAndFiltersLiveParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		input               BuildApplyPlanInput
		wantInstance        []simpleParameter
		wantCluster         []simpleParameter
		wantInstanceSkipped []SkippedVariable
		wantClusterSkipped  []SkippedVariable
	}{
		{
			name: "instance membership wins over cluster membership",
			input: planInput(
				map[string]ParameterInfo{
					"max_connections": liveParameter("max_connections", "dynamic", true, ScopeInstance),
				},
				map[string]ParameterInfo{
					"max_connections": liveParameter("max_connections", "static", true, ScopeCluster),
				},
				map[string]interface{}{"max_connections": "250"},
			),
			wantInstance: []simpleParameter{{
				name: "max_connections", value: "250", method: types.ApplyMethodImmediate,
			}},
		},
		{
			name: "cluster-only static parameter routes to current writer",
			input: planInput(
				nil,
				map[string]ParameterInfo{
					"binlog_format": liveParameter("binlog_format", "static", true, ScopeCluster),
				},
				map[string]interface{}{"binlog_format": "ROW"},
			),
			wantCluster: []simpleParameter{{
				name: "binlog_format", value: "ROW", method: types.ApplyMethodPendingReboot,
			}},
		},
		{
			name: "refreshed reader role skips cluster-only parameter from stale task",
			input: func() BuildApplyPlanInput {
				input := planInput(nil, map[string]ParameterInfo{
					"binlog_format": liveParameter("binlog_format", "dynamic", true, ScopeCluster),
				}, map[string]interface{}{"binlog_format": "ROW"})
				input.Metadata.IsClusterWriter = false
				return input
			}(),
			wantClusterSkipped: []SkippedVariable{{
				Name: "binlog_format", Reason: SkipNotClusterWriter,
			}},
		},
		{
			// BuildApplyPlan no longer validates ConfiguredClusterGroup itself
			// (that precondition now lives solely in the caller); an
			// unconfigured group no longer skips an otherwise-eligible
			// cluster-only parameter.
			name: "missing configured cluster group does not skip cluster-only parameter",
			input: func() BuildApplyPlanInput {
				input := planInput(nil, map[string]ParameterInfo{
					"binlog_format": liveParameter("binlog_format", "dynamic", true, ScopeCluster),
				}, map[string]interface{}{"binlog_format": "ROW"})
				input.ConfiguredClusterGroup = ""
				return input
			}(),
			wantCluster: []simpleParameter{{
				name: "binlog_format", value: "ROW", method: types.ApplyMethodImmediate,
			}},
		},
		{
			name: "parameter absent from both live groups",
			input: planInput(
				nil,
				nil,
				map[string]interface{}{"query_cache_size": "0"},
			),
			wantInstanceSkipped: []SkippedVariable{{
				Name: "query_cache_size", Reason: SkipAbsent,
			}},
		},
		{
			name: "unmodifiable live parameter",
			input: planInput(
				map[string]ParameterInfo{
					"max_connections": liveParameter("max_connections", "dynamic", false, ScopeInstance),
				},
				nil,
				map[string]interface{}{"max_connections": "250"},
			),
			wantInstanceSkipped: []SkippedVariable{{
				Name: "max_connections", Reason: SkipUnmodifiable,
			}},
		},
		{
			// BuildApplyPlan no longer rejects a default (AWS-managed)
			// configured group itself; that precondition now lives solely in
			// the caller (task_apply_aws.go's validateAWS*Group), which
			// already blocks a default group before BuildApplyPlan runs.
			name: "default instance and cluster groups are not rejected by BuildApplyPlan itself",
			input: func() BuildApplyPlanInput {
				input := planInput(
					map[string]ParameterInfo{
						"max_connections": liveParameter("max_connections", "dynamic", true, ScopeInstance),
					},
					map[string]ParameterInfo{
						"binlog_format": liveParameter("binlog_format", "dynamic", true, ScopeCluster),
					},
					map[string]interface{}{"max_connections": "250", "binlog_format": "ROW"},
				)
				input.ConfiguredInstanceGroup = "default.aurora-mysql8.0"
				input.ConfiguredClusterGroup = "default.aurora-mysql8.0"
				input.Metadata.DBParameterGroup = input.ConfiguredInstanceGroup
				input.Metadata.DBClusterParameterGroup = input.ConfiguredClusterGroup
				return input
			}(),
			wantInstance: []simpleParameter{{
				name: "max_connections", value: "250", method: types.ApplyMethodImmediate,
			}},
			wantCluster: []simpleParameter{{
				name: "binlog_format", value: "ROW", method: types.ApplyMethodImmediate,
			}},
		},
		{
			name: "unsupported live engine mode",
			input: func() BuildApplyPlanInput {
				input := planInput(map[string]ParameterInfo{
					"max_connections": liveParameterWithModes("max_connections", "dynamic", true, ScopeInstance, "serverless"),
				}, nil, map[string]interface{}{"max_connections": "250"})
				input.Metadata.EngineMode = "provisioned"
				return input
			}(),
			wantInstanceSkipped: []SkippedVariable{{
				Name: "max_connections", Reason: SkipUnsupportedEngineMode,
			}},
		},
		{
			name: "empty live engine mode list adds no restriction",
			input: planInput(
				map[string]ParameterInfo{
					"max_connections": liveParameter("max_connections", "dynamic", true, ScopeInstance),
				},
				nil,
				map[string]interface{}{"max_connections": "250"},
			),
			wantInstance: []simpleParameter{{
				name: "max_connections", value: "250", method: types.ApplyMethodImmediate,
			}},
		},
		{
			// BuildApplyPlan no longer excludes Aurora Serverless v2
			// self-managed parameters (isServerlessManaged was removed): the
			// platform's recommendation engine is responsible for not
			// recommending them for a serverless instance.
			name: "Aurora MySQL Serverless instance submits every eligible parameter",
			input: func() BuildApplyPlanInput {
				input := planInput(
					map[string]ParameterInfo{
						"innodb_buffer_pool_size": liveParameter("innodb_buffer_pool_size", "dynamic", true, ScopeInstance),
						"innodb_purge_threads":    liveParameter("innodb_purge_threads", "dynamic", true, ScopeInstance),
						"table_definition_cache":  liveParameter("table_definition_cache", "dynamic", true, ScopeInstance),
						"table_open_cache":        liveParameter("table_open_cache", "dynamic", true, ScopeInstance),
						"thread_cache_size":       liveParameter("thread_cache_size", "dynamic", true, ScopeInstance),
					},
					nil,
					map[string]interface{}{
						"innodb_buffer_pool_size": "1073741824",
						"innodb_purge_threads":    "4",
						"table_definition_cache":  "2000",
						"table_open_cache":        "4000",
						"thread_cache_size":       "100",
					},
				)
				input.Metadata.IsServerlessV2 = true
				return input
			}(),
			wantInstance: []simpleParameter{
				{name: "innodb_buffer_pool_size", value: "1073741824", method: types.ApplyMethodImmediate},
				{name: "innodb_purge_threads", value: "4", method: types.ApplyMethodImmediate},
				{name: "table_definition_cache", value: "2000", method: types.ApplyMethodImmediate},
				{name: "table_open_cache", value: "4000", method: types.ApplyMethodImmediate},
				{name: "thread_cache_size", value: "100", method: types.ApplyMethodImmediate},
			},
		},
		{
			// Same removal as the Aurora MySQL Serverless case above:
			// shared_buffers is no longer excluded by BuildApplyPlan itself.
			name: "Aurora PostgreSQL Serverless instance submits shared_buffers too",
			input: func() BuildApplyPlanInput {
				input := planInput(
					map[string]ParameterInfo{
						"shared_buffers": liveParameter("shared_buffers", "static", true, ScopeInstance),
						"work_mem":       liveParameter("work_mem", "dynamic", true, ScopeInstance),
					},
					nil,
					map[string]interface{}{"shared_buffers": "134217728", "work_mem": "4194304"},
				)
				input.Metadata.Engine = "aurora-postgresql"
				input.Metadata.IsServerlessV2 = true
				return input
			}(),
			wantInstance: []simpleParameter{
				{name: "shared_buffers", value: "16384", method: types.ApplyMethodPendingReboot},
				{name: "work_mem", value: "4096", method: types.ApplyMethodImmediate},
			},
		},
		{
			name: "pending-reboot-only mode defers dynamic and static parameters",
			input: func() BuildApplyPlanInput {
				input := planInput(
					map[string]ParameterInfo{
						"dynamic_parameter": liveParameter("dynamic_parameter", "dynamic", true, ScopeInstance),
						"static_parameter":  liveParameter("static_parameter", "static", true, ScopeInstance),
					},
					nil,
					map[string]interface{}{"dynamic_parameter": "1", "static_parameter": "2"},
				)
				input.PendingRebootOnly = true
				return input
			}(),
			wantInstance: []simpleParameter{
				{name: "dynamic_parameter", value: "1", method: types.ApplyMethodPendingReboot},
				{name: "static_parameter", value: "2", method: types.ApplyMethodPendingReboot},
			},
		},
		{
			name: "unchanged values compare after safe normalization",
			input: func() BuildApplyPlanInput {
				input := planInput(
					map[string]ParameterInfo{
						"max_connections":            liveParameter("max_connections", "dynamic", true, ScopeInstance),
						"innodb_max_dirty_pages_pct": liveParameter("innodb_max_dirty_pages_pct", "dynamic", true, ScopeInstance),
					},
					nil,
					map[string]interface{}{
						"max_connections":            json.Number("250"),
						"innodb_max_dirty_pages_pct": "75.9000",
					},
				)
				input.CurrentValues = map[string]interface{}{
					"max_connections":            "250",
					"innodb_max_dirty_pages_pct": json.Number("75"),
				}
				return input
			}(),
			wantInstanceSkipped: []SkippedVariable{
				{Name: "innodb_max_dirty_pages_pct", Reason: SkipUnchanged},
				{Name: "max_connections", Reason: SkipUnchanged},
			},
		},
		{
			name: "string JSON number and dirty-page percentage normalize deterministically",
			input: planInput(
				map[string]ParameterInfo{
					"max_connections":            liveParameter("max_connections", "dynamic", true, ScopeInstance),
					"table_open_cache":           liveParameter("table_open_cache", "static", true, ScopeInstance),
					"innodb_max_dirty_pages_pct": liveParameter("innodb_max_dirty_pages_pct", "dynamic", true, ScopeInstance),
				},
				nil,
				map[string]interface{}{
					"table_open_cache":           json.Number("4000.0"),
					"innodb_max_dirty_pages_pct": json.Number("75.900"),
					"max_connections":            "250",
				},
			),
			wantInstance: []simpleParameter{
				{name: "innodb_max_dirty_pages_pct", value: "75", method: types.ApplyMethodImmediate},
				{name: "max_connections", value: "250", method: types.ApplyMethodImmediate},
				{name: "table_open_cache", value: "4000.0", method: types.ApplyMethodPendingReboot},
			},
		},
		{
			// BuildApplyPlan no longer validates the cluster group itself, so
			// a mismatched attached group does not skip the parameter either
			// (see the doc comments above on the other group-validation
			// subtests in this table).
			name: "cluster group mismatch is not enforced by BuildApplyPlan itself",
			input: func() BuildApplyPlanInput {
				input := planInput(
					map[string]ParameterInfo{
						"max_connections": liveParameter("max_connections", "dynamic", true, ScopeInstance),
					},
					map[string]ParameterInfo{
						"binlog_format": liveParameter("binlog_format", "dynamic", true, ScopeCluster),
					},
					map[string]interface{}{"max_connections": "250", "binlog_format": "ROW"},
				)
				input.Metadata.DBClusterParameterGroup = "attached-cluster-custom"
				return input
			}(),
			wantInstance: []simpleParameter{{
				name: "max_connections", value: "250", method: types.ApplyMethodImmediate,
			}},
			wantCluster: []simpleParameter{{
				name: "binlog_format", value: "ROW", method: types.ApplyMethodImmediate,
			}},
		},
		{
			// Instance scope still wins over cluster scope purely by name
			// membership (routing priority is unrelated to group
			// validation), but a mismatched attached instance group no
			// longer skips the parameter either, for the same reason as
			// above.
			name: "instance scope still wins by membership though group mismatch is not enforced",
			input: func() BuildApplyPlanInput {
				input := planInput(
					map[string]ParameterInfo{
						"max_connections": liveParameter("max_connections", "dynamic", true, ScopeInstance),
					},
					map[string]ParameterInfo{
						"max_connections": liveParameter("max_connections", "dynamic", true, ScopeCluster),
					},
					map[string]interface{}{"max_connections": "250"},
				)
				input.Metadata.DBParameterGroup = "attached-instance-custom"
				return input
			}(),
			wantInstance: []simpleParameter{{
				name: "max_connections", value: "250", method: types.ApplyMethodImmediate,
			}},
		},
		{
			name: "ordinary RDS MySQL keeps instance-only behavior",
			input: func() BuildApplyPlanInput {
				input := planInput(
					map[string]ParameterInfo{
						"max_connections": liveParameter("max_connections", "dynamic", true, ScopeInstance),
					},
					nil,
					map[string]interface{}{"max_connections": "250"},
				)
				input.Metadata.Engine = "mysql"
				input.Metadata.EngineMode = ""
				input.Metadata.DBClusterParameterGroup = ""
				input.Metadata.IsClusterWriter = false
				input.ConfiguredClusterGroup = ""
				return input
			}(),
			wantInstance: []simpleParameter{{
				name: "max_connections", value: "250", method: types.ApplyMethodImmediate,
			}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan, result := BuildApplyPlan(tt.input)
			if got := simpleParameters(plan.Instance.Parameters); !reflect.DeepEqual(got, tt.wantInstance) {
				t.Errorf("instance parameters = %#v, want %#v", got, tt.wantInstance)
			}
			if got := simpleParameters(plan.Cluster.Parameters); !reflect.DeepEqual(got, tt.wantCluster) {
				t.Errorf("cluster parameters = %#v, want %#v", got, tt.wantCluster)
			}
			if !reflect.DeepEqual(result.Instance.Skipped, nonNilSkips(tt.wantInstanceSkipped)) {
				t.Errorf("instance skips = %#v, want %#v", result.Instance.Skipped, nonNilSkips(tt.wantInstanceSkipped))
			}
			if !reflect.DeepEqual(result.Cluster.Skipped, nonNilSkips(tt.wantClusterSkipped)) {
				t.Errorf("cluster skips = %#v, want %#v", result.Cluster.Skipped, nonNilSkips(tt.wantClusterSkipped))
			}
			if plan.Instance.Group != tt.input.ConfiguredInstanceGroup || result.Instance.Group != tt.input.ConfiguredInstanceGroup {
				t.Errorf("instance groups = plan %q result %q, want %q", plan.Instance.Group, result.Instance.Group, tt.input.ConfiguredInstanceGroup)
			}
			if plan.Cluster.Group != tt.input.ConfiguredClusterGroup || result.Cluster.Group != tt.input.ConfiguredClusterGroup {
				t.Errorf("cluster groups = plan %q result %q, want %q", plan.Cluster.Group, result.Cluster.Group, tt.input.ConfiguredClusterGroup)
			}
		})
	}
}

func TestBuildApplyPlanPostgreSQLCurrentValueUnitSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		parameter      ParameterInfo
		current        interface{}
		wantParameters []simpleParameter
		wantSkipped    []SkippedVariable
	}{
		{
			name: "live AWS value is already native",
			parameter: ParameterInfo{
				Name:              "work_mem",
				ApplyType:         "dynamic",
				IsModifiable:      true,
				ParameterValue:    "4096",
				HasParameterValue: true,
			},
			current:     json.Number("4096"),
			wantSkipped: []SkippedVariable{{Name: "work_mem", Reason: SkipUnchanged}},
		},
		{
			name:        "DB metrics fallback is already AWS native",
			parameter:   liveParameter("work_mem", "dynamic", true, ScopeInstance),
			current:     json.Number("4096"),
			wantSkipped: []SkippedVariable{{Name: "work_mem", Reason: SkipUnchanged}},
		},
		{
			name:           "invalid DB metrics fallback does not suppress recommendation",
			parameter:      liveParameter("work_mem", "dynamic", true, ScopeInstance),
			current:        []byte("4096"),
			wantParameters: []simpleParameter{{name: "work_mem", value: "4096", method: types.ApplyMethodImmediate}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := planInput(
				map[string]ParameterInfo{"work_mem": tt.parameter},
				nil,
				map[string]interface{}{"work_mem": json.Number("4194304")},
			)
			input.Metadata.Engine = "postgres"
			input.Metadata.DBClusterParameterGroup = ""
			input.ConfiguredClusterGroup = ""
			input.CurrentValues = map[string]interface{}{"work_mem": tt.current}

			plan, result := BuildApplyPlan(input)
			if got := simpleParameters(plan.Instance.Parameters); !reflect.DeepEqual(got, tt.wantParameters) {
				t.Fatalf("instance parameters = %#v, want %#v", got, tt.wantParameters)
			}
			if !reflect.DeepEqual(result.Instance.Skipped, nonNilSkips(tt.wantSkipped)) {
				t.Fatalf("instance skips = %#v, want %#v", result.Instance.Skipped, nonNilSkips(tt.wantSkipped))
			}
		})
	}
}

func TestBuildApplyPlanPostgreSQLUnitConversionRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value interface{}
	}{
		{name: "non-integral", value: json.Number("4194304.5")},
		{name: "not unit aligned", value: json.Number("4194305")},
		{name: "non-numeric", value: "four megabytes"},
		{name: "overflow", value: json.Number("18446744073709551616")},
		{name: "negative", value: json.Number("-1024")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := planInput(
				map[string]ParameterInfo{
					"work_mem": liveParameter("work_mem", "dynamic", true, ScopeInstance),
				},
				nil,
				map[string]interface{}{"work_mem": tt.value},
			)
			input.Metadata.Engine = "postgres"
			input.Metadata.DBClusterParameterGroup = ""
			input.ConfiguredClusterGroup = ""

			plan, result := BuildApplyPlan(input)
			if len(plan.Instance.Parameters) != 0 {
				t.Fatalf("instance parameters = %#v, want no unsafe conversion", simpleParameters(plan.Instance.Parameters))
			}
			want := []SkippedVariable{{Name: "work_mem", Reason: SkipInvalidValue}}
			if !reflect.DeepEqual(result.Instance.Skipped, want) {
				t.Fatalf("instance skips = %#v, want %#v", result.Instance.Skipped, want)
			}
		})
	}
}

func TestBuildApplyPlanMySQLDoesNotConvertPostgreSQLNamedParameter(t *testing.T) {
	t.Parallel()

	input := planInput(
		map[string]ParameterInfo{
			"work_mem": liveParameter("work_mem", "dynamic", true, ScopeInstance),
		},
		nil,
		map[string]interface{}{"work_mem": json.Number("4194304")},
	)
	input.Metadata.Engine = "mysql"
	input.Metadata.DBClusterParameterGroup = ""
	input.ConfiguredClusterGroup = ""

	plan, result := BuildApplyPlan(input)
	want := []simpleParameter{{name: "work_mem", value: "4194304", method: types.ApplyMethodImmediate}}
	if got := simpleParameters(plan.Instance.Parameters); !reflect.DeepEqual(got, want) {
		t.Fatalf("instance parameters = %#v, want unchanged MySQL value %#v", got, want)
	}
	if len(result.Instance.Skipped) != 0 {
		t.Fatalf("instance skips = %#v, want none", result.Instance.Skipped)
	}
}

func TestBuildApplyPlanPreservesJSONNumberPrecision(t *testing.T) {
	t.Parallel()

	const (
		largeInteger         = "9223372036854775809"
		highPrecisionDecimal = "0.123456789012345678901234567890"
	)
	input := planInput(
		map[string]ParameterInfo{
			"large_integer":           liveParameter("large_integer", "dynamic", true, ScopeInstance),
			"precise_decimal":         liveParameter("precise_decimal", "dynamic", true, ScopeInstance),
			"unchanged_decimal":       liveParameter("unchanged_decimal", "dynamic", true, ScopeInstance),
			"unchanged_large_integer": liveParameter("unchanged_large_integer", "dynamic", true, ScopeInstance),
		},
		nil,
		map[string]interface{}{
			"large_integer":           json.Number(largeInteger),
			"precise_decimal":         json.Number(highPrecisionDecimal),
			"unchanged_decimal":       json.Number(highPrecisionDecimal),
			"unchanged_large_integer": json.Number(largeInteger),
		},
	)
	input.CurrentValues = map[string]interface{}{
		"unchanged_decimal":       highPrecisionDecimal,
		"unchanged_large_integer": largeInteger,
	}

	plan, result := BuildApplyPlan(input)
	wantParameters := []simpleParameter{
		{name: "large_integer", value: largeInteger, method: types.ApplyMethodImmediate},
		{name: "precise_decimal", value: highPrecisionDecimal, method: types.ApplyMethodImmediate},
	}
	if got := simpleParameters(plan.Instance.Parameters); !reflect.DeepEqual(got, wantParameters) {
		t.Fatalf("instance parameters = %#v, want exact JSON number tokens %#v", got, wantParameters)
	}
	wantSkips := []SkippedVariable{
		{Name: "unchanged_decimal", Reason: SkipUnchanged},
		{Name: "unchanged_large_integer", Reason: SkipUnchanged},
	}
	if !reflect.DeepEqual(result.Instance.Skipped, wantSkips) {
		t.Fatalf("instance skips = %#v, want %#v", result.Instance.Skipped, wantSkips)
	}
}

func TestParameterApplyResultSerializesWithoutValues(t *testing.T) {
	t.Parallel()

	_, plannedResult := BuildApplyPlan(planInput(
		map[string]ParameterInfo{
			"password_parameter": liveParameter("password_parameter", "dynamic", true, ScopeInstance),
		},
		nil,
		map[string]interface{}{"password_parameter": []byte("super-secret-value")},
	))
	if want := []SkippedVariable{{Name: "password_parameter", Reason: SkipInvalidValue}}; !reflect.DeepEqual(plannedResult.Instance.Skipped, want) {
		t.Fatalf("invalid value skips = %#v, want %#v", plannedResult.Instance.Skipped, want)
	}
	plannedJSON, err := json.Marshal(plannedResult)
	if err != nil {
		t.Fatalf("json.Marshal(planned result) error = %v", err)
	}
	if strings.Contains(string(plannedJSON), "super-secret-value") {
		t.Fatalf("serialized result contains a parameter value: %s", plannedJSON)
	}
}

func TestIsDefaultParameterGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		group string
		want  bool
	}{
		{name: "empty group", group: "", want: false},
		{name: "custom group", group: "orders-default-custom", want: false},
		{name: "AWS default group", group: "default.aurora-postgresql15", want: true},
		{name: "AWS default group is case insensitive", group: "DEFAULT.AURORA-MYSQL8.0", want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsDefaultParameterGroup(tt.group); got != tt.want {
				t.Fatalf("IsDefaultParameterGroup(%q) = %v, want %v", tt.group, got, tt.want)
			}
		})
	}
}

func TestNormalizeParameterValueSupportedTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		parameter string
		value     interface{}
		want      string
		wantError bool
	}{
		{name: "string", value: "ON", want: "ON"},
		{name: "JSON integer", value: json.Number("9223372036854775809"), want: "9223372036854775809"},
		{name: "JSON decimal", value: json.Number("0.1250"), want: "0.1250"},
		{name: "float64", value: float64(12.5), want: "12.5"},
		{name: "float32", value: float32(12.5), want: "12.5"},
		{name: "int", value: int(-1), want: "-1"},
		{name: "int8", value: int8(-2), want: "-2"},
		{name: "int16", value: int16(-3), want: "-3"},
		{name: "int32", value: int32(-4), want: "-4"},
		{name: "int64", value: int64(-5), want: "-5"},
		{name: "uint", value: uint(1), want: "1"},
		{name: "uint8", value: uint8(2), want: "2"},
		{name: "uint16", value: uint16(3), want: "3"},
		{name: "uint32", value: uint32(4), want: "4"},
		{name: "uint64", value: uint64(18446744073709551615), want: "18446744073709551615"},
		{name: "dirty pages percentage truncates", parameter: "innodb_max_dirty_pages_pct", value: "75.999", want: "75"},
		{name: "empty JSON number", value: json.Number(""), wantError: true},
		{name: "JSON number with whitespace", value: json.Number(" 1"), wantError: true},
		{name: "JSON non-number literal", value: json.Number("true"), wantError: true},
		{name: "float64 NaN", value: math.NaN(), wantError: true},
		{name: "float64 infinity", value: math.Inf(1), wantError: true},
		{name: "float32 NaN", value: float32(math.NaN()), wantError: true},
		{name: "float32 infinity", value: float32(math.Inf(-1)), wantError: true},
		{name: "invalid dirty pages percentage", parameter: "innodb_max_dirty_pages_pct", value: "seventy-five", wantError: true},
		{name: "unsupported type", value: []byte("1"), wantError: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeParameterValue(tt.parameter, tt.value)
			if tt.wantError {
				if err == nil {
					t.Fatalf("normalizeParameterValue(%q, %#v) = %q, want error", tt.parameter, tt.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeParameterValue(%q, %#v) error = %v", tt.parameter, tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeParameterValue(%q, %#v) = %q, want %q", tt.parameter, tt.value, got, tt.want)
			}
		})
	}
}

func planInput(instance, cluster map[string]ParameterInfo, recommendations map[string]interface{}) BuildApplyPlanInput {
	return BuildApplyPlanInput{
		Metadata: Metadata{
			Engine:                  "aurora-mysql",
			EngineMode:              "provisioned",
			DBParameterGroup:        "orders-instance-custom",
			DBClusterParameterGroup: "orders-cluster-custom",
			IsClusterWriter:         true,
		},
		ConfiguredInstanceGroup: "orders-instance-custom",
		ConfiguredClusterGroup:  "orders-cluster-custom",
		InstanceParameters:      instance,
		ClusterParameters:       cluster,
		Recommendations:         recommendations,
		CurrentValues:           map[string]interface{}{},
	}
}

func liveParameter(name, applyType string, modifiable bool, _ Scope) ParameterInfo {
	return ParameterInfo{
		Name:         name,
		ApplyType:    applyType,
		IsModifiable: modifiable,
	}
}

func liveParameterWithModes(name, applyType string, modifiable bool, scope Scope, modes ...string) ParameterInfo {
	parameter := liveParameter(name, applyType, modifiable, scope)
	parameter.SupportedEngineModes = modes
	return parameter
}

func simpleParameters(parameters []types.Parameter) []simpleParameter {
	if len(parameters) == 0 {
		return nil
	}
	simple := make([]simpleParameter, 0, len(parameters))
	for _, parameter := range parameters {
		simple = append(simple, simpleParameter{
			name:   aws.ToString(parameter.ParameterName),
			value:  aws.ToString(parameter.ParameterValue),
			method: parameter.ApplyMethod,
		})
	}
	return simple
}

func nonNilSkips(skips []SkippedVariable) []SkippedVariable {
	if skips == nil {
		return []SkippedVariable{}
	}
	return skips
}
