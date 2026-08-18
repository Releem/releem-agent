package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	logging "github.com/google/logger"
)

type awsApplyGatherer struct {
	current map[string]interface{}
	calls   int
}

func (g *awsApplyGatherer) GetMetrics(metrics *models.Metrics) error {
	g.calls++
	metrics.DB.Conf.Variables = g.current
	return nil
}

type awsApplyRepeater struct {
	recommendations string
	calls           int
}

func (r *awsApplyRepeater) ProcessMetrics(_ models.MetricContext, _ models.Metrics, mode models.ModeType) (string, error) {
	if mode == (models.ModeType{Name: "Configurations", Type: "GetJson"}) {
		r.calls++
		return r.recommendations, nil
	}
	return "", nil
}

type awsModifyCall struct {
	group      string
	parameters []types.Parameter
}

type awsApplyClientFake struct {
	instanceOutput         *rds.DescribeDBInstancesOutput
	clusterOutput          *rds.DescribeDBClustersOutput
	instancePages          map[string]*rds.DescribeDBParametersOutput
	clusterPages           map[string]*rds.DescribeDBClusterParametersOutput
	instanceDescribeErrors map[string]error
	clusterDescribeErrors  map[string]error

	describeInstanceCalls  int
	describeClusterCalls   int
	instanceDescribeGroups []string
	clusterDescribeGroups  []string
	instanceModifyCalls    []awsModifyCall
	clusterModifyCalls     []awsModifyCall

	instanceModifyErrors map[int]error
	clusterModifyErrors  map[int]error
}

func (f *awsApplyClientFake) DescribeDBInstances(_ context.Context, _ *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	f.describeInstanceCalls++
	return f.instanceOutput, nil
}

func (f *awsApplyClientFake) DescribeDBClusters(_ context.Context, _ *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	f.describeClusterCalls++
	return f.clusterOutput, nil
}

func (f *awsApplyClientFake) DescribeDBParameters(_ context.Context, input *rds.DescribeDBParametersInput, _ ...func(*rds.Options)) (*rds.DescribeDBParametersOutput, error) {
	group := aws.ToString(input.DBParameterGroupName)
	f.instanceDescribeGroups = append(f.instanceDescribeGroups, group)
	if err := f.instanceDescribeErrors[group]; err != nil {
		return nil, err
	}
	if page := f.instancePages[aws.ToString(input.Marker)]; page != nil {
		return page, nil
	}
	return &rds.DescribeDBParametersOutput{}, nil
}

func (f *awsApplyClientFake) DescribeDBClusterParameters(_ context.Context, input *rds.DescribeDBClusterParametersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClusterParametersOutput, error) {
	group := aws.ToString(input.DBClusterParameterGroupName)
	f.clusterDescribeGroups = append(f.clusterDescribeGroups, group)
	if err := f.clusterDescribeErrors[group]; err != nil {
		return nil, err
	}
	if page := f.clusterPages[aws.ToString(input.Marker)]; page != nil {
		return page, nil
	}
	return &rds.DescribeDBClusterParametersOutput{}, nil
}

func (f *awsApplyClientFake) ModifyDBParameterGroup(_ context.Context, input *rds.ModifyDBParameterGroupInput, _ ...func(*rds.Options)) (*rds.ModifyDBParameterGroupOutput, error) {
	call := awsModifyCall{
		group:      aws.ToString(input.DBParameterGroupName),
		parameters: append([]types.Parameter(nil), input.Parameters...),
	}
	f.instanceModifyCalls = append(f.instanceModifyCalls, call)
	callNumber := len(f.instanceModifyCalls)
	if err := f.instanceModifyErrors[callNumber]; err != nil {
		return nil, err
	}
	return &rds.ModifyDBParameterGroupOutput{}, nil
}

func (f *awsApplyClientFake) ModifyDBClusterParameterGroup(_ context.Context, input *rds.ModifyDBClusterParameterGroupInput, _ ...func(*rds.Options)) (*rds.ModifyDBClusterParameterGroupOutput, error) {
	call := awsModifyCall{
		group:      aws.ToString(input.DBClusterParameterGroupName),
		parameters: append([]types.Parameter(nil), input.Parameters...),
	}
	f.clusterModifyCalls = append(f.clusterModifyCalls, call)
	callNumber := len(f.clusterModifyCalls)
	if err := f.clusterModifyErrors[callNumber]; err != nil {
		return nil, err
	}
	return &rds.ModifyDBClusterParameterGroupOutput{}, nil
}

func TestDecodeAWSRecommendationsRequiresSingleJSONDocument(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "trailing junk", raw: `{"max_connections":"200"} garbage`},
		{name: "second JSON value", raw: `{"max_connections":"200"}{"work_mem":"4096"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if recommendations, err := decodeAWSRecommendations(tt.raw); err == nil {
				t.Fatalf("decodeAWSRecommendations(%q) = %#v, nil; want trailing-data error", tt.raw, recommendations)
			}
		})
	}

	if recommendations, err := decodeAWSRecommendations("  {\"max_connections\":\"200\"} \n\t"); err != nil || recommendations["max_connections"] != "200" {
		t.Fatalf("decodeAWSRecommendations(valid whitespace) = %#v, %v", recommendations, err)
	}
}

func TestApplyConfAwsRdsRequiresInstanceParameterGroupInSync(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		wantExit  int
		wantLists bool
	}{
		{name: "in sync", status: "in-sync", wantExit: awsApplyExitSuccess, wantLists: true},
		{name: "applying", status: "applying", wantExit: awsApplyExitParameterGroupNotInSync},
		{name: "pending reboot", status: "pending-reboot", wantExit: awsApplyExitParameterGroupNotInSync},
		{name: "missing status", status: "", wantExit: awsApplyExitParameterGroupNotInSync},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, "instance-custom", "cluster-custom")
			client.instanceOutput.DBInstances[0].DBParameterGroups[0].ParameterApplyStatus = aws.String(tt.status)
			client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("instance_value", "dynamic")},
			}}
			client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("cluster_value", "dynamic")},
			}}
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, taskStatus, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"instance_value":"2","cluster_value":"3"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{
					"instance_value": "1",
					"cluster_value":  "1",
				}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", "cluster-custom"),
				AWSApplyAll,
			)

			if exitCode != tt.wantExit {
				t.Fatalf("ApplyConfAwsRds() exit = %d, want %d; output %s", exitCode, tt.wantExit, output)
			}
			if tt.wantLists {
				if taskStatus != awsApplyTaskStatusSuccess || len(client.instanceDescribeGroups) != 1 || len(client.clusterDescribeGroups) != 1 {
					t.Fatalf("ready apply status/lists = %d/%#v/%#v", taskStatus, client.instanceDescribeGroups, client.clusterDescribeGroups)
				}
				if len(client.instanceModifyCalls) != 1 || len(client.clusterModifyCalls) != 1 {
					t.Fatalf("ready modify calls = instance %d cluster %d, want 1 each", len(client.instanceModifyCalls), len(client.clusterModifyCalls))
				}
				return
			}

			if taskStatus != awsApplyTaskStatusFailure {
				t.Fatalf("blocked task status = %d, want %d", taskStatus, awsApplyTaskStatusFailure)
			}
			if len(client.instanceDescribeGroups) != 0 || len(client.clusterDescribeGroups) != 0 || len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
				t.Fatalf("blocked apply reached parameter APIs: instance lists %#v, cluster lists %#v, instance modifies %d, cluster modifies %d", client.instanceDescribeGroups, client.clusterDescribeGroups, len(client.instanceModifyCalls), len(client.clusterModifyCalls))
			}
			result := decodeAWSApplyResult(t, output)
			if len(result.Instance.Failed) != 1 || len(result.Cluster.Failed) != 0 {
				t.Fatalf("blocked result = %#v, want one instance-scope failure", result)
			}
			failure := result.Instance.Failed[0]
			if failure.Error != "parameter-group-not-in-sync" ||
				failure.DBInstanceIdentifier == nil || *failure.DBInstanceIdentifier != "orders-1" ||
				failure.ParameterGroup == nil || *failure.ParameterGroup != "instance-custom" ||
				failure.ParameterGroupStatus == nil || *failure.ParameterGroupStatus != tt.status {
				t.Fatalf("blocked readiness diagnostic = %#v, want instance=%q group=%q status=%q", failure, "orders-1", "instance-custom", tt.status)
			}
		})
	}
}

func TestApplyConfAwsRdsRequiresWriterClusterParameterGroupInSync(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		wantExit  int
		wantLists bool
	}{
		{name: "in sync", status: "in-sync", wantExit: awsApplyExitSuccess, wantLists: true},
		{name: "applying", status: "applying", wantExit: awsApplyExitParameterGroupNotInSync},
		{name: "pending reboot", status: "pending-reboot", wantExit: awsApplyExitParameterGroupNotInSync},
		{name: "missing status", status: "", wantExit: awsApplyExitParameterGroupNotInSync},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, "instance-custom", "cluster-custom")
			client.clusterOutput.DBClusters[0].DBClusterMembers[0].DBClusterParameterGroupStatus = aws.String(tt.status)
			client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("instance_value", "dynamic")},
			}}
			client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("cluster_value", "dynamic")},
			}}
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, taskStatus, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"instance_value":"2","cluster_value":"3"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{
					"instance_value": "1",
					"cluster_value":  "1",
				}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", "cluster-custom"),
				AWSApplyAll,
			)

			if exitCode != tt.wantExit {
				t.Fatalf("ApplyConfAwsRds() exit = %d, want %d; output %s", exitCode, tt.wantExit, output)
			}
			if tt.wantLists {
				if taskStatus != awsApplyTaskStatusSuccess || len(client.instanceDescribeGroups) != 1 || len(client.clusterDescribeGroups) != 1 {
					t.Fatalf("ready apply status/lists = %d/%#v/%#v", taskStatus, client.instanceDescribeGroups, client.clusterDescribeGroups)
				}
				return
			}

			if taskStatus != awsApplyTaskStatusFailure {
				t.Fatalf("blocked task status = %d, want %d", taskStatus, awsApplyTaskStatusFailure)
			}
			if len(client.instanceDescribeGroups) != 0 || len(client.clusterDescribeGroups) != 0 || len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
				t.Fatalf("blocked apply reached parameter APIs: instance lists %#v, cluster lists %#v, instance modifies %d, cluster modifies %d", client.instanceDescribeGroups, client.clusterDescribeGroups, len(client.instanceModifyCalls), len(client.clusterModifyCalls))
			}
			result := decodeAWSApplyResult(t, output)
			if len(result.Instance.Failed) != 0 || len(result.Cluster.Failed) != 1 {
				t.Fatalf("blocked result = %#v, want one cluster-scope failure", result)
			}
			failure := result.Cluster.Failed[0]
			if failure.Error != "parameter-group-not-in-sync" ||
				failure.DBClusterIdentifier == nil || *failure.DBClusterIdentifier != "orders-cluster" ||
				failure.ParameterGroup == nil || *failure.ParameterGroup != "cluster-custom" ||
				failure.ParameterGroupStatus == nil || *failure.ParameterGroupStatus != tt.status {
				t.Fatalf("blocked readiness diagnostic = %#v, want cluster=%q group=%q status=%q", failure, "orders-cluster", "cluster-custom", tt.status)
			}
		})
	}
}

func TestApplyConfAwsRdsRefreshesDiscoveryRoutesScopesAndBatchesTwentyPlusOne(t *testing.T) {
	instanceParameters := make([]types.Parameter, 0, 21)
	recommendations := make(map[string]string, 22)
	current := make(map[string]interface{}, 22)
	for index := 0; index < 21; index++ {
		name := fmt.Sprintf("instance_%02d", index)
		instanceParameters = append(instanceParameters, modifiableAWSParameter(name, "dynamic"))
		recommendations[name] = "2"
		current[name] = "1"
	}
	recommendations["cluster_static"] = "ROW"
	current["cluster_static"] = "MIXED"

	client := auroraApplyClient(true, "instance-custom", "cluster-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {Parameters: instanceParameters}}
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("cluster_static", "static")},
	}}
	repeater := &awsApplyRepeater{recommendations: mustJSON(t, recommendations)}
	gatherer := &awsApplyGatherer{current: current}
	waited := awsApplyModifiedScopes{}
	installAWSApplyTestDependencies(t, client, func(scopes awsApplyModifiedScopes) { waited = scopes })

	exitCode, status, output := ApplyConfAwsRds(
		repeater,
		[]models.MetricsGatherer{gatherer},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", "cluster-custom"),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
	}
	if gatherer.calls != 1 || repeater.calls != 1 {
		t.Fatalf("metrics/recommendation calls = %d/%d, want one collection and one recommendation", gatherer.calls, repeater.calls)
	}
	if client.describeInstanceCalls != 1 || client.describeClusterCalls != 1 {
		t.Fatalf("discovery calls = instance %d cluster %d, want refreshed once", client.describeInstanceCalls, client.describeClusterCalls)
	}
	if got := batchSizes(client.instanceModifyCalls); !reflect.DeepEqual(got, []int{20, 1}) {
		t.Fatalf("instance batch sizes = %v, want [20 1]", got)
	}
	if got := batchSizes(client.clusterModifyCalls); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("cluster batch sizes = %v, want [1]", got)
	}
	for _, call := range client.instanceModifyCalls {
		if call.group != "instance-custom" {
			t.Fatalf("instance modify group = %q, want instance-custom", call.group)
		}
		for _, parameter := range call.parameters {
			if parameter.ApplyMethod != types.ApplyMethodImmediate {
				t.Fatalf("instance apply method = %q, want immediate", parameter.ApplyMethod)
			}
		}
	}
	if client.clusterModifyCalls[0].group != "cluster-custom" || client.clusterModifyCalls[0].parameters[0].ApplyMethod != types.ApplyMethodPendingReboot {
		t.Fatalf("cluster modify call = %#v, want cluster-custom pending-reboot", client.clusterModifyCalls[0])
	}
	if waited != (awsApplyModifiedScopes{Instance: true, Cluster: true}) {
		t.Fatalf("waited scopes = %#v, want both modified scopes", waited)
	}

	result := decodeAWSApplyResult(t, output)
	if len(result.Instance.Applied) != 21 || !reflect.DeepEqual(result.Cluster.Applied, []string{"cluster_static"}) {
		t.Fatalf("applied result = instance %v cluster %v", result.Instance.Applied, result.Cluster.Applied)
	}
}

func TestApplyConfAwsRdsValidatesGroupMismatchesIndependently(t *testing.T) {
	tests := []struct {
		name                 string
		attachedInstance     string
		attachedCluster      string
		wantInstanceModifies int
		wantClusterModifies  int
		wantScope            awsrds.Scope
		wantExpected         string
		wantActual           string
	}{
		{
			name:                 "cluster mismatch keeps valid instance scope",
			attachedInstance:     "instance-custom",
			attachedCluster:      "other-cluster",
			wantInstanceModifies: 1,
			wantScope:            awsrds.ScopeCluster,
			wantExpected:         "cluster-custom",
			wantActual:           "other-cluster",
		},
		{
			name:                "instance mismatch keeps valid cluster scope",
			attachedInstance:    "other-instance",
			attachedCluster:     "cluster-custom",
			wantClusterModifies: 1,
			wantScope:           awsrds.ScopeInstance,
			wantExpected:        "instance-custom",
			wantActual:          "other-instance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, tt.attachedInstance, tt.attachedCluster)
			client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("instance_value", "dynamic")},
			}}
			client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("cluster_value", "dynamic")},
			}}
			if tt.attachedInstance != "instance-custom" {
				client.instanceDescribeErrors["instance-custom"] = errors.New("configured instance group does not exist")
			}
			if tt.attachedCluster != "cluster-custom" {
				client.clusterDescribeErrors["cluster-custom"] = errors.New("configured cluster group does not exist")
			}
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"instance_value":"2","cluster_value":"2"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"instance_value": "1", "cluster_value": "1"}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", "cluster-custom"),
				AWSApplyAll,
			)

			if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
				t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
			}
			if len(client.instanceModifyCalls) != tt.wantInstanceModifies || len(client.clusterModifyCalls) != tt.wantClusterModifies {
				t.Fatalf("modify calls = instance %d cluster %d, want %d/%d", len(client.instanceModifyCalls), len(client.clusterModifyCalls), tt.wantInstanceModifies, tt.wantClusterModifies)
			}
			if !reflect.DeepEqual(client.instanceDescribeGroups, []string{tt.attachedInstance}) {
				t.Fatalf("described instance groups = %v, want attached group %q for mismatch-safe classification", client.instanceDescribeGroups, tt.attachedInstance)
			}
			if !reflect.DeepEqual(client.clusterDescribeGroups, []string{tt.attachedCluster}) {
				t.Fatalf("described cluster groups = %v, want attached group %q for mismatch-safe classification", client.clusterDescribeGroups, tt.attachedCluster)
			}
			result := decodeAWSApplyResult(t, output)
			var scopeResult awsrds.ScopeResult
			if tt.wantScope == awsrds.ScopeInstance {
				scopeResult = result.Instance
			} else {
				scopeResult = result.Cluster
			}
			if len(scopeResult.Skipped) != 1 || scopeResult.Skipped[0].Reason != awsrds.SkipGroupMismatch {
				t.Fatalf("per-variable mismatch result = %#v, want one group-mismatch skip", scopeResult.Skipped)
			}
			if len(scopeResult.Diagnostics) != 1 || scopeResult.Diagnostics[0].Reason != awsrds.SkipGroupMismatch || scopeResult.Diagnostics[0].ExpectedGroup != tt.wantExpected || scopeResult.Diagnostics[0].ActualGroup != tt.wantActual {
				t.Fatalf("scope diagnostics = %#v, want expected %q actual %q", scopeResult.Diagnostics, tt.wantExpected, tt.wantActual)
			}
		})
	}
}

func TestApplyConfAwsRdsReportsEachGroupMismatchOnceAtScopeLevel(t *testing.T) {
	client := auroraApplyClient(true, "attached-instance", "attached-cluster")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{
			modifiableAWSParameter("instance_one", "dynamic"),
			modifiableAWSParameter("instance_two", "dynamic"),
		},
	}}
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {}}
	installAWSApplyTestDependencies(t, client, nil)
	cfg := awsApplyConfig("configured-instance", "configured-cluster")
	cfg.ApiKey = "api-key-must-not-leak"
	cfg.MysqlPassword = "password-must-not-leak"

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"instance_one":"recommended-instance-one","instance_two":"recommended-instance-two"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{}}},
		testAWSApplyLogger(),
		cfg,
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
	}
	if len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
		t.Fatalf("mismatched groups were modified: instance %#v cluster %#v", client.instanceModifyCalls, client.clusterModifyCalls)
	}

	type scopeOutput struct {
		Diagnostics []struct {
			Reason        awsrds.SkipReason `json:"reason"`
			ExpectedGroup string            `json:"expected_group"`
			ActualGroup   string            `json:"actual_group"`
		} `json:"diagnostics"`
		Skipped []awsrds.SkippedVariable `json:"skipped"`
	}
	var structured struct {
		Instance scopeOutput `json:"instance"`
		Cluster  scopeOutput `json:"cluster"`
	}
	if err := json.Unmarshal([]byte(output), &structured); err != nil {
		t.Fatalf("decode structured mismatch output: %v; output=%s", err, output)
	}
	assertDiagnostic := func(scope string, got scopeOutput, expected, actual string) {
		t.Helper()
		if len(got.Diagnostics) != 1 {
			t.Fatalf("%s diagnostics = %#v, want exactly one", scope, got.Diagnostics)
		}
		diagnostic := got.Diagnostics[0]
		if diagnostic.Reason != awsrds.SkipGroupMismatch || diagnostic.ExpectedGroup != expected || diagnostic.ActualGroup != actual {
			t.Fatalf("%s diagnostic = %#v, want group mismatch %q -> %q", scope, diagnostic, expected, actual)
		}
	}
	assertDiagnostic("instance", structured.Instance, "configured-instance", "attached-instance")
	assertDiagnostic("cluster", structured.Cluster, "configured-cluster", "attached-cluster")
	if len(structured.Instance.Skipped) != 2 {
		t.Fatalf("instance skips = %#v, want two rejected variables", structured.Instance.Skipped)
	}
	for _, skipped := range structured.Instance.Skipped {
		if skipped.Reason != awsrds.SkipGroupMismatch {
			t.Fatalf("per-variable mismatch skip = %#v, want group mismatch", skipped)
		}
	}
	if len(structured.Cluster.Skipped) != 0 {
		t.Fatalf("cluster skips = %#v, want no synthetic variable skip", structured.Cluster.Skipped)
	}
	for _, secret := range []string{cfg.ApiKey, cfg.MysqlPassword} {
		if strings.Contains(output, secret) {
			t.Fatalf("mismatch output leaked %q: %s", secret, output)
		}
	}
	result := decodeAWSApplyResult(t, output)
	gotRecommendations := map[string]string{}
	for _, record := range result.Audit.Parameters {
		gotRecommendations[record.Name] = record.RecommendedValue
	}
	wantRecommendations := map[string]string{
		"instance_one": "recommended-instance-one",
		"instance_two": "recommended-instance-two",
	}
	if !reflect.DeepEqual(gotRecommendations, wantRecommendations) {
		t.Fatalf("audit recommendations = %#v, want %#v", gotRecommendations, wantRecommendations)
	}
}

func TestApplyConfAwsRdsReaderAndClassificationOnlyClusterGroupsNeverModifyCluster(t *testing.T) {
	tests := []struct {
		name            string
		writer          bool
		configuredGroup string
		wantReason      awsrds.SkipReason
	}{
		{name: "fresh reader role", writer: false, configuredGroup: "cluster-custom", wantReason: awsrds.SkipNotClusterWriter},
		{name: "attached group is classification only", writer: true, configuredGroup: "", wantReason: awsrds.SkipGroupNotConfigured},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(tt.writer, "instance-custom", "cluster-custom")
			if !tt.writer {
				client.clusterOutput.DBClusters[0].DBClusterMembers[0].DBClusterParameterGroupStatus = aws.String("applying")
			}
			client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {}}
			client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("cluster_value", "dynamic")},
			}}
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"cluster_value":"2"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"cluster_value": "1"}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", tt.configuredGroup),
				AWSApplyAll,
			)

			if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
				t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
			}
			if len(client.clusterModifyCalls) != 0 {
				t.Fatalf("cluster modify calls = %d, want none", len(client.clusterModifyCalls))
			}
			if !reflect.DeepEqual(client.clusterDescribeGroups, []string{"cluster-custom"}) {
				t.Fatalf("described cluster groups = %v, want attached group for live classification", client.clusterDescribeGroups)
			}
			result := decodeAWSApplyResult(t, output)
			if result.Cluster.Group != tt.configuredGroup || len(result.Cluster.Skipped) != 1 || result.Cluster.Skipped[0].Reason != tt.wantReason {
				t.Fatalf("cluster result = %#v, want group %q skip %q", result.Cluster, tt.configuredGroup, tt.wantReason)
			}
		})
	}
}

func TestApplyConfAwsRdsIneligibleClusterClassificationErrorsDoNotBlockInstance(t *testing.T) {
	const classificationSecret = "classification-error-secret"
	tests := []struct {
		name                   string
		configuredClusterGroup string
		attachedClusterGroup   string
		wantDescribeGroups     []string
	}{
		{
			name:                 "missing cluster config makes attached lookup optional",
			attachedClusterGroup: "attached-cluster",
			wantDescribeGroups:   []string{"attached-cluster"},
		},
		{
			name:                   "cluster mismatch makes attached lookup optional",
			configuredClusterGroup: "missing-cluster",
			attachedClusterGroup:   "attached-cluster",
			wantDescribeGroups:     []string{"attached-cluster"},
		},
		{
			name:                   "default cluster group is rejected before lookup",
			configuredClusterGroup: "default.aurora-mysql8.0",
			attachedClusterGroup:   "default.aurora-mysql8.0",
			wantDescribeGroups:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, "instance-custom", tt.attachedClusterGroup)
			client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("instance_value", "dynamic")},
			}}
			client.clusterDescribeErrors[tt.attachedClusterGroup] = errors.New(classificationSecret)
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"instance_value":"2"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"instance_value": "1"}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", tt.configuredClusterGroup),
				AWSApplyAll,
			)

			if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
				t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s, want valid instance success", exitCode, status, output)
			}
			if len(client.instanceModifyCalls) != 1 || len(client.clusterModifyCalls) != 0 {
				t.Fatalf("modify calls = instance %d cluster %d, want 1/0", len(client.instanceModifyCalls), len(client.clusterModifyCalls))
			}
			if !reflect.DeepEqual(client.clusterDescribeGroups, tt.wantDescribeGroups) {
				t.Fatalf("cluster classification reads = %v, want %v", client.clusterDescribeGroups, tt.wantDescribeGroups)
			}
			if strings.Contains(output, classificationSecret) {
				t.Fatalf("optional classification error leaked into task output: %s", output)
			}
		})
	}
}

func TestApplyConfAwsRdsUnknownInstanceClassificationCannotFallThroughToCluster(t *testing.T) {
	client := auroraApplyClient(true, "attached-instance", "cluster-custom")
	client.instanceDescribeErrors["attached-instance"] = errors.New("instance classification unavailable")
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("shared_name", "dynamic")},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"shared_name":"2"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"shared_name": "1"}}},
		testAWSApplyLogger(),
		awsApplyConfig("", "cluster-custom"),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s, want safe no-op", exitCode, status, output)
	}
	if !reflect.DeepEqual(client.instanceDescribeGroups, []string{"attached-instance"}) {
		t.Fatalf("instance classification reads = %v, want attached-instance", client.instanceDescribeGroups)
	}
	if len(client.clusterModifyCalls) != 0 {
		t.Fatalf("cluster modify calls = %d, want none when instance membership is unknown", len(client.clusterModifyCalls))
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Instance.Skipped) != 1 || result.Instance.Skipped[0].Name != "shared_name" || result.Instance.Skipped[0].Reason != awsrds.SkipGroupNotConfigured {
		t.Fatalf("instance safety result = %#v, want group-not-configured ownership guard", result.Instance)
	}
}

func TestApplyConfAwsRdsDefaultInstanceGroupIsClassificationOnly(t *testing.T) {
	const defaultGroup = "default.aurora-mysql8.0"
	client := auroraApplyClient(true, defaultGroup, "cluster-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("instance_value", "dynamic")},
	}}
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
		Parameters: []types.Parameter{
			modifiableAWSParameter("instance_value", "dynamic"),
			modifiableAWSParameter("cluster_value", "dynamic"),
		},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"instance_value":"2","cluster_value":"2"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"instance_value": "1", "cluster_value": "1"}}},
		testAWSApplyLogger(),
		awsApplyConfig(defaultGroup, "cluster-custom"),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
	}
	if !reflect.DeepEqual(client.instanceDescribeGroups, []string{defaultGroup}) {
		t.Fatalf("instance parameter reads = %v, want read-only classification of %q", client.instanceDescribeGroups, defaultGroup)
	}
	if len(client.instanceModifyCalls) != 0 {
		t.Fatalf("instance modify calls = %d, want none for a default group", len(client.instanceModifyCalls))
	}
	if len(client.clusterModifyCalls) != 1 || client.clusterModifyCalls[0].group != "cluster-custom" || len(client.clusterModifyCalls[0].parameters) != 1 || aws.ToString(client.clusterModifyCalls[0].parameters[0].ParameterName) != "cluster_value" {
		t.Fatalf("cluster modify calls = %#v, want only genuine cluster_value", client.clusterModifyCalls)
	}
	result := decodeAWSApplyResult(t, output)
	if !reflect.DeepEqual(result.Instance.Skipped, []awsrds.SkippedVariable{{Name: "instance_value", Reason: awsrds.SkipDefaultGroup}}) {
		t.Fatalf("default instance result = %#v, want one default-group skip", result.Instance)
	}
	if !reflect.DeepEqual(result.Cluster.Applied, []string{"cluster_value"}) {
		t.Fatalf("cluster result = %#v, want cluster_value applied", result.Cluster)
	}
}

func TestApplyConfAwsRdsEmptyPlanIsSuccessfulNoOp(t *testing.T) {
	client := auroraApplyClient(true, "instance-custom", "cluster-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {}}
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {}}
	waitCalls := 0
	installAWSApplyTestDependencies(t, client, func(awsApplyModifiedScopes) { waitCalls++ })

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"unknown":"2"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"unknown": "1"}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", "cluster-custom"),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
	}
	if len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 || waitCalls != 0 {
		t.Fatalf("no-op calls = instance %d cluster %d wait %d, want zero", len(client.instanceModifyCalls), len(client.clusterModifyCalls), waitCalls)
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Instance.Skipped) != 1 || result.Instance.Skipped[0].Reason != awsrds.SkipAbsent {
		t.Fatalf("no-op result = %#v, want absent skip", result)
	}
}

func TestApplyConfAwsRdsAWSFailurePreservesEarlierBatchesAndNamesFailure(t *testing.T) {
	parameters := make([]types.Parameter, 0, 21)
	recommendations := make(map[string]string, 21)
	current := make(map[string]interface{}, 21)
	for index := 0; index < 21; index++ {
		name := fmt.Sprintf("parameter_%02d", index)
		parameters = append(parameters, modifiableAWSParameter(name, "dynamic"))
		recommendations[name] = "2"
		current[name] = "1"
	}
	wantErr := errors.New("AWS throttled request")
	client := mysqlApplyClient("instance-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {Parameters: parameters}}
	client.instanceModifyErrors = map[int]error{2: wantErr}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: mustJSON(t, recommendations)},
		[]models.MetricsGatherer{&awsApplyGatherer{current: current}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", ""),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitFailure || status != awsApplyTaskStatusFailure {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s, want generic AWS failure", exitCode, status, output)
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Instance.Applied) != 20 {
		t.Fatalf("earlier applied parameters = %v, want 20 retained", result.Instance.Applied)
	}
	if result.Instance.Group != "instance-custom" || len(result.Instance.Failed) != 1 {
		t.Fatalf("instance failure = %#v, want one failure in instance-custom", result.Instance)
	}
	failure := result.Instance.Failed[0]
	if !reflect.DeepEqual(failure.Parameters, []string{"parameter_20"}) || failure.Error != awsApplyErrorAWSAPI {
		t.Fatalf("failed batch = %#v, want parameter_20 and safe category %q", failure, awsApplyErrorAWSAPI)
	}
}

func TestApplyConfAwsRdsAccessDeniedHasDistinctExitCode(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("max_connections", "dynamic")},
	}}
	client.instanceModifyErrors = map[int]error{1: errors.New("AccessDeniedException: not authorized")}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"max_connections":"200"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"max_connections": "100"}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", ""),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitAccessDenied || status != awsApplyTaskStatusFailure {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s, want AccessDenied exit %d", exitCode, status, output, awsApplyExitAccessDenied)
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Instance.Failed) != 1 || result.Instance.Failed[0].Error != awsApplyErrorAccessDenied {
		t.Fatalf("AccessDenied result = %#v, want safe category %q", result.Instance.Failed, awsApplyErrorAccessDenied)
	}
}

func TestApplyConfAwsRdsOrdinaryRDSMySQLUsesOnlyInstanceModifyAPI(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("max_connections", "dynamic")},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"max_connections":"200"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"max_connections": "100"}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", ""),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
	}
	if len(client.instanceModifyCalls) != 1 || len(client.clusterModifyCalls) != 0 || client.describeClusterCalls != 0 || len(client.clusterDescribeGroups) != 0 {
		t.Fatalf("ordinary RDS calls = instance modify %d cluster modify %d cluster discovery %d cluster parameter reads %d", len(client.instanceModifyCalls), len(client.clusterModifyCalls), client.describeClusterCalls, len(client.clusterDescribeGroups))
	}
}

func TestApplyConfAwsRdsUsesDBMetricsOnlyWhenAWSOmitsParameterValue(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	missingValue := modifiableAWSParameter("missing_value", "dynamic")
	explicitEmpty := modifiableAWSParameter("explicit_empty", "dynamic")
	explicitEmpty.ParameterValue = aws.String("")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{missingValue, explicitEmpty},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"missing_value":"same","explicit_empty":"same"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{
			"missing_value":  models.MetricGroupValue{"setting": "same"},
			"explicit_empty": models.MetricGroupValue{"setting": "same"},
		}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", ""),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
	}
	if len(client.instanceModifyCalls) != 1 || len(client.instanceModifyCalls[0].parameters) != 1 || aws.ToString(client.instanceModifyCalls[0].parameters[0].ParameterName) != "explicit_empty" {
		t.Fatalf("instance modify calls = %#v, want only explicit_empty", client.instanceModifyCalls)
	}
	result := decodeAWSApplyResult(t, output)
	if !reflect.DeepEqual(result.Instance.Applied, []string{"explicit_empty"}) {
		t.Fatalf("applied = %v, want explicit_empty", result.Instance.Applied)
	}
	if !reflect.DeepEqual(result.Instance.Skipped, []awsrds.SkippedVariable{{Name: "missing_value", Reason: awsrds.SkipUnchanged}}) {
		t.Fatalf("skipped = %#v, want missing_value unchanged from nested DB metrics", result.Instance.Skipped)
	}
}

func TestApplyConfAwsRdsTreatsPostgreSQLDBMetricsFallbackAsAWSNative(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	client.instanceOutput.DBInstances[0].Engine = aws.String("postgres")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{
			modifiableAWSParameter("work_mem", "dynamic"),
			modifiableAWSParameter("wal_buffers", "dynamic"),
		},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"work_mem":4194304,"wal_buffers":16777216}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{
			"work_mem": models.MetricGroupValue{
				"setting":         "4096",
				"unit":            "kB",
				"vartype":         "integer",
				"source":          "configuration file",
				"sourcefile":      "/etc/postgresql/postgresql.conf",
				"sourceline":      "120",
				"min_val":         "64",
				"max_val":         "2147483647",
				"enumvals":        "NULL",
				"pending_restart": false,
			},
			"wal_buffers": models.MetricGroupValue{
				"setting":         "2048",
				"unit":            "8kB",
				"vartype":         "integer",
				"source":          "configuration file",
				"sourcefile":      "/etc/postgresql/postgresql.conf",
				"sourceline":      "121",
				"min_val":         "-1",
				"max_val":         "262143",
				"enumvals":        "NULL",
				"pending_restart": false,
			},
		}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", ""),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
	}
	if len(client.instanceModifyCalls) != 0 {
		t.Fatalf("instance modify calls = %#v, want unchanged PostgreSQL settings", client.instanceModifyCalls)
	}
	result := decodeAWSApplyResult(t, output)
	wantSkipped := []awsrds.SkippedVariable{
		{Name: "wal_buffers", Reason: awsrds.SkipUnchanged},
		{Name: "work_mem", Reason: awsrds.SkipUnchanged},
	}
	if !reflect.DeepEqual(result.Instance.Skipped, wantSkipped) {
		t.Fatalf("instance skips = %#v, want native DB-metrics values unchanged", result.Instance.Skipped)
	}
}

func TestApplyConfAwsRdsConvertsPostgreSQLByteRecommendationsToAWSNativeUnits(t *testing.T) {
	contract := loadPostgreSQLAWSParameterUnitContract(t)

	tests := []struct {
		name                   string
		client                 *awsApplyClientFake
		configuration          *config.Config
		wantInstanceModifyCall bool
		wantClusterModifyCall  bool
	}{
		{
			name: "provisioned Aurora PostgreSQL",
			client: func() *awsApplyClientFake {
				client := auroraApplyClient(true, "instance-custom", "cluster-custom")
				client.instanceOutput.DBInstances[0].Engine = aws.String("aurora-postgresql")
				client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
					Parameters: []types.Parameter{
						modifiableAWSParameter("shared_buffers", "static"),
						modifiableAWSParameter("work_mem", "dynamic"),
						modifiableAWSParameter("maintenance_work_mem", "dynamic"),
						modifiableAWSParameter("effective_cache_size", "dynamic"),
					},
				}}
				client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
					Parameters: []types.Parameter{modifiableAWSParameter("wal_buffers", "dynamic")},
				}}
				return client
			}(),
			configuration:          awsApplyConfig("instance-custom", "cluster-custom"),
			wantInstanceModifyCall: true,
			wantClusterModifyCall:  true,
		},
		{
			name: "ordinary RDS PostgreSQL",
			client: func() *awsApplyClientFake {
				client := mysqlApplyClient("instance-custom")
				client.instanceOutput.DBInstances[0].Engine = aws.String("postgres")
				client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
					Parameters: []types.Parameter{
						modifiableAWSParameter("shared_buffers", "static"),
						modifiableAWSParameter("work_mem", "dynamic"),
						modifiableAWSParameter("maintenance_work_mem", "dynamic"),
						modifiableAWSParameter("effective_cache_size", "dynamic"),
						modifiableAWSParameter("wal_buffers", "dynamic"),
					},
				}}
				return client
			}(),
			configuration:          awsApplyConfig("instance-custom", ""),
			wantInstanceModifyCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installAWSApplyTestDependencies(t, tt.client, nil)

			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: string(contract.Recommendations)},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{}}},
				testAWSApplyLogger(),
				tt.configuration,
				AWSApplyAll,
			)

			if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
				t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s", exitCode, status, output)
			}
			if (len(tt.client.instanceModifyCalls) > 0) != tt.wantInstanceModifyCall || (len(tt.client.clusterModifyCalls) > 0) != tt.wantClusterModifyCall {
				t.Fatalf("modify calls = instance %d cluster %d, want instance=%v cluster=%v", len(tt.client.instanceModifyCalls), len(tt.client.clusterModifyCalls), tt.wantInstanceModifyCall, tt.wantClusterModifyCall)
			}
			allCalls := append(append([]awsModifyCall(nil), tt.client.instanceModifyCalls...), tt.client.clusterModifyCalls...)
			if got := awsModifiedParameterValues(t, allCalls); !reflect.DeepEqual(got, contract.AWSParameterValues) {
				t.Fatalf("AWS parameter values = %#v, want native PostgreSQL units %#v", got, contract.AWSParameterValues)
			}
		})
	}
}

func TestApplyConfAwsRdsRepeatedPendingRebootUsesCanonicalAWSValuesForBothScopes(t *testing.T) {
	client := auroraApplyClient(true, "instance-custom", "cluster-custom")
	client.instanceOutput.DBInstances[0].Engine = aws.String("aurora-postgresql")
	instanceParameter := modifiableAWSParameter("instance_static", "static")
	instanceParameter.ParameterValue = aws.String("200")
	clusterParameter := modifiableAWSParameter("cluster_static", "static")
	clusterParameter.ParameterValue = aws.String("on")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{instanceParameter},
	}}
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
		Parameters: []types.Parameter{clusterParameter},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	for run := 1; run <= 2; run++ {
		exitCode, status, output := ApplyConfAwsRds(
			&awsApplyRepeater{recommendations: `{"instance_static":"200","cluster_static":"on"}`},
			[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{
				"instance_static": models.MetricGroupValue{"setting": "100", "pending_restart": true},
				"cluster_static":  models.MetricGroupValue{"setting": "off", "pending_restart": true},
			}}},
			testAWSApplyLogger(),
			awsApplyConfig("instance-custom", "cluster-custom"),
			AWSApplyPendingRebootOnly,
		)

		if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
			t.Fatalf("run %d ApplyConfAwsRds() = exit %d status %d output %s", run, exitCode, status, output)
		}
		if len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
			t.Fatalf("run %d modify calls = instance %#v cluster %#v, want none", run, client.instanceModifyCalls, client.clusterModifyCalls)
		}
		result := decodeAWSApplyResult(t, output)
		if !reflect.DeepEqual(result.Instance.Skipped, []awsrds.SkippedVariable{{Name: "instance_static", Reason: awsrds.SkipUnchanged}}) {
			t.Fatalf("run %d instance result = %#v, want canonical AWS unchanged", run, result.Instance)
		}
		if !reflect.DeepEqual(result.Cluster.Skipped, []awsrds.SkippedVariable{{Name: "cluster_static", Reason: awsrds.SkipUnchanged}}) {
			t.Fatalf("run %d cluster result = %#v, want canonical AWS unchanged", run, result.Cluster)
		}
	}
}

func TestApplyConfAwsRdsReadbackDeadlineDoesNotFailTask(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	parameter := modifiableAWSParameter("max_connections", "dynamic")
	parameter.ParameterValue = aws.String("100")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{parameter},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	originalReadback := readAWSAppliedParameters
	originalReadbackTimeout := awsApplyReadbackTimeout
	awsApplyReadbackTimeout = 20 * time.Millisecond
	releaseReadback := make(chan struct{})
	released := false
	readAWSAppliedParameters = func(ctx context.Context, _ awsrds.ParameterReader, _ awsrds.Scope, _ awsrds.ScopePlan) (map[string]awsrds.ParameterInfo, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-releaseReadback:
			return nil, errors.New("test readback released")
		}
	}
	t.Cleanup(func() {
		if !released {
			close(releaseReadback)
		}
		readAWSAppliedParameters = originalReadback
		awsApplyReadbackTimeout = originalReadbackTimeout
	})

	var logOutput strings.Builder
	logger := *logging.Init("task-apply-aws-readback-test", false, false, &logOutput)
	type applyReturn struct {
		exitCode int
		status   int
		output   string
	}
	completed := make(chan applyReturn, 1)
	go func() {
		exitCode, status, output := ApplyConfAwsRds(
			&awsApplyRepeater{recommendations: `{"max_connections":"200"}`},
			[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"max_connections": "100"}}},
			logger,
			awsApplyConfig("instance-custom", ""),
			AWSApplyAll,
		)
		completed <- applyReturn{exitCode: exitCode, status: status, output: output}
	}()

	var applied applyReturn
	select {
	case applied = <-completed:
	case <-time.After(time.Second):
		close(releaseReadback)
		released = true
		<-completed
		t.Fatal("ApplyConfAwsRds() did not return after the bounded optional readback")
	}

	if applied.exitCode != awsApplyExitSuccess || applied.status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d output %s, want successful task", applied.exitCode, applied.status, applied.output)
	}
	result := decodeAWSApplyResult(t, applied.output)
	record := auditRecord(t, &result.Audit, awsrds.ScopeInstance, "max_connections")
	if record.VerificationStatus != awsrds.VerificationUnavailable || record.Reason != awsrds.ReasonReadbackFailed {
		t.Fatalf("readback failure audit = status %q reason %q, want unavailable/readback-failed", record.VerificationStatus, record.Reason)
	}
	if !strings.Contains(logOutput.String(), "AWS parameter readback unavailable") {
		t.Fatalf("readback failure log = %q, want warning", logOutput.String())
	}
}

func TestAWSApplyWaiterAcceptsPendingRebootAndPollsOnlyModifiedScopes(t *testing.T) {
	tests := []struct {
		name                  string
		client                *awsApplyClientFake
		request               awsApplyWaitRequest
		wantInstancePollCalls int
		wantClusterPollCalls  int
	}{
		{
			name: "pending reboot completes instance wait without cluster polling",
			client: func() *awsApplyClientFake {
				client := mysqlApplyClient("instance-custom")
				client.instanceOutput.DBInstances[0].DBParameterGroups[0].ParameterApplyStatus = aws.String("pending-reboot")
				client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
					Parameters: []types.Parameter{{ParameterName: aws.String("static_value"), ParameterValue: aws.String("2")}},
				}}
				return client
			}(),
			request: awsApplyWaitRequest{
				Instance: awsrds.ScopePlan{Group: "instance-custom", Parameters: []types.Parameter{{ParameterName: aws.String("static_value"), ParameterValue: aws.String("2")}}},
			},
			wantInstancePollCalls: 1,
		},
		{
			name: "cluster-only modification does not poll instance",
			client: func() *awsApplyClientFake {
				client := auroraApplyClient(true, "instance-custom", "cluster-custom")
				client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
					Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value"), ParameterValue: aws.String("ROW")}},
				}}
				return client
			}(),
			request: awsApplyWaitRequest{
				Cluster: awsrds.ScopePlan{Group: "cluster-custom", Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value"), ParameterValue: aws.String("ROW")}}},
			},
			wantClusterPollCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.request.Metadata = awsrds.Metadata{
				DBInstanceIdentifier: "orders-1",
				DBClusterIdentifier:  "orders-cluster",
			}
			if err := defaultWaitForAWSApply(context.Background(), tt.client, tt.request); err != nil {
				t.Fatalf("defaultWaitForAWSApply() error = %v", err)
			}
			if tt.client.describeInstanceCalls != tt.wantInstancePollCalls || tt.client.describeClusterCalls != tt.wantClusterPollCalls {
				t.Fatalf("poll calls = instance %d cluster %d, want %d/%d", tt.client.describeInstanceCalls, tt.client.describeClusterCalls, tt.wantInstancePollCalls, tt.wantClusterPollCalls)
			}
			if tt.request.modifiedScopes().Instance && !reflect.DeepEqual(tt.client.instanceDescribeGroups, []string{tt.request.Instance.Group}) {
				t.Fatalf("instance parameter polls = %v, want group %q", tt.client.instanceDescribeGroups, tt.request.Instance.Group)
			}
			if tt.request.modifiedScopes().Cluster && !reflect.DeepEqual(tt.client.clusterDescribeGroups, []string{tt.request.Cluster.Group}) {
				t.Fatalf("cluster parameter polls = %v, want group %q", tt.client.clusterDescribeGroups, tt.request.Cluster.Group)
			}
		})
	}
}

func TestAWSApplyWaiterDoesNotAcceptPreChangeSnapshot(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{{ParameterName: aws.String("max_connections"), ParameterValue: aws.String("100")}},
	}}
	request := awsApplyWaitRequest{
		Metadata: awsrds.Metadata{DBInstanceIdentifier: "mysql-1"},
		Instance: awsrds.ScopePlan{
			Group:      "instance-custom",
			Parameters: []types.Parameter{{ParameterName: aws.String("max_connections"), ParameterValue: aws.String("200")}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := defaultWaitForAWSApply(ctx, client, request)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("defaultWaitForAWSApply() error = %v, want immediate canceled-context timeout after old value", err)
	}
	if client.describeInstanceCalls != 0 {
		t.Fatalf("instance status polls = %d, want none until the requested parameter value is observed", client.describeInstanceCalls)
	}
	if !reflect.DeepEqual(client.instanceDescribeGroups, []string{"instance-custom"}) {
		t.Fatalf("parameter polls = %v, want one pre-change value check", client.instanceDescribeGroups)
	}
}

func TestAWSApplyWaitTimeoutPreservesExitCodeSixThroughScopeWrapping(t *testing.T) {
	err := newAWSApplyTimeoutErrorForScopes(awsApplyModifiedScopes{Cluster: true})
	if exitCode := awsApplyExitCode(err); exitCode != awsApplyExitTimeout {
		t.Fatalf("awsApplyExitCode() = %d, want timeout exit %d", exitCode, awsApplyExitTimeout)
	}
	result := newAWSApplyResult(awsApplyConfig("instance-custom", "cluster-custom"))
	request := awsApplyWaitRequest{
		Cluster: awsrds.ScopePlan{Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value")}}},
	}
	recordAWSWaitFailure(&result, request, err)
	decoded := decodeAWSApplyResult(t, marshalAWSApplyResult(&result))
	if len(decoded.Cluster.Failed) != 1 || decoded.Cluster.Failed[0].Error != awsApplyErrorTimeout {
		t.Fatalf("timeout result = %#v, want safe category %q", decoded.Cluster.Failed, awsApplyErrorTimeout)
	}
}

func TestAWSApplyWaitDeadlineExceededDuringPollPreservesExitCodeSix(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	client.instanceDescribeErrors["instance-custom"] = context.DeadlineExceeded
	request := awsApplyWaitRequest{
		Metadata: awsrds.Metadata{DBInstanceIdentifier: "mysql-1"},
		Instance: awsrds.ScopePlan{
			Group:      "instance-custom",
			Parameters: []types.Parameter{{ParameterName: aws.String("max_connections"), ParameterValue: aws.String("200")}},
		},
	}

	err := defaultWaitForAWSApply(context.Background(), client, request)
	if exitCode := awsApplyExitCode(err); exitCode != awsApplyExitTimeout {
		t.Fatalf("deadline poll exit code = %d error %v, want timeout exit %d", exitCode, err, awsApplyExitTimeout)
	}
	result := newAWSApplyResult(awsApplyConfig("instance-custom", ""))
	recordAWSWaitFailure(&result, request, err)
	decoded := decodeAWSApplyResult(t, marshalAWSApplyResult(&result))
	if len(decoded.Instance.Failed) != 1 || decoded.Instance.Failed[0].Error != awsApplyErrorTimeout {
		t.Fatalf("deadline result = %#v, want safe timeout category", decoded.Instance.Failed)
	}
}

func TestAWSApplySharedDeadlineRecordsEveryUnresolvedScope(t *testing.T) {
	client := auroraApplyClient(true, "instance-custom", "cluster-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{{
			ParameterName:  aws.String("instance_value"),
			ParameterValue: aws.String("old-instance-value"),
		}},
	}}
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
		Parameters: []types.Parameter{{
			ParameterName:  aws.String("cluster_value"),
			ParameterValue: aws.String("old-cluster-value"),
		}},
	}}
	request := awsApplyWaitRequest{
		Metadata: awsrds.Metadata{
			DBInstanceIdentifier: "orders-1",
			DBClusterIdentifier:  "orders-cluster",
		},
		Instance: awsrds.ScopePlan{
			Group: "instance-custom",
			Parameters: []types.Parameter{{
				ParameterName:  aws.String("instance_value"),
				ParameterValue: aws.String("new-instance-value-must-not-leak"),
			}},
		},
		Cluster: awsrds.ScopePlan{
			Group: "cluster-custom",
			Parameters: []types.Parameter{{
				ParameterName:  aws.String("cluster_value"),
				ParameterValue: aws.String("new-cluster-value-must-not-leak"),
			}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := defaultWaitForAWSApply(ctx, client, request)
	if exitCode := awsApplyExitCode(err); exitCode != awsApplyExitTimeout {
		t.Fatalf("shared deadline exit code = %d error %v, want timeout exit %d", exitCode, err, awsApplyExitTimeout)
	}
	result := newAWSApplyResult(awsApplyConfig("instance-custom", "cluster-custom"))
	recordAWSWaitFailure(&result, request, err)
	output := marshalAWSApplyResult(&result)
	decoded := decodeAWSApplyResult(t, output)
	if len(decoded.Instance.Failed) != 1 || decoded.Instance.Failed[0].Error != awsApplyErrorTimeout || !reflect.DeepEqual(decoded.Instance.Failed[0].Parameters, []string{"instance_value"}) {
		t.Fatalf("instance timeout result = %#v, want one sanitized instance timeout", decoded.Instance.Failed)
	}
	if len(decoded.Cluster.Failed) != 1 || decoded.Cluster.Failed[0].Error != awsApplyErrorTimeout || !reflect.DeepEqual(decoded.Cluster.Failed[0].Parameters, []string{"cluster_value"}) {
		t.Fatalf("cluster timeout result = %#v, want one sanitized cluster timeout", decoded.Cluster.Failed)
	}
	for _, rawValue := range []string{"old-instance-value", "old-cluster-value", "new-instance-value-must-not-leak", "new-cluster-value-must-not-leak"} {
		if strings.Contains(output, rawValue) {
			t.Fatalf("timeout output leaked parameter value %q: %s", rawValue, output)
		}
	}
}

func TestAWSApplyDeadlineDuringPollRecordsEveryStillUnresolvedScope(t *testing.T) {
	client := auroraApplyClient(true, "instance-custom", "cluster-custom")
	client.instanceDescribeErrors["instance-custom"] = context.DeadlineExceeded
	request := awsApplyWaitRequest{
		Metadata: awsrds.Metadata{
			DBInstanceIdentifier: "orders-1",
			DBClusterIdentifier:  "orders-cluster",
		},
		Instance: awsrds.ScopePlan{
			Group:      "instance-custom",
			Parameters: []types.Parameter{{ParameterName: aws.String("instance_value")}},
		},
		Cluster: awsrds.ScopePlan{
			Group:      "cluster-custom",
			Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value")}},
		},
	}

	err := defaultWaitForAWSApply(context.Background(), client, request)
	if exitCode := awsApplyExitCode(err); exitCode != awsApplyExitTimeout {
		t.Fatalf("deadline poll exit code = %d error %v, want timeout exit %d", exitCode, err, awsApplyExitTimeout)
	}
	result := newAWSApplyResult(awsApplyConfig("instance-custom", "cluster-custom"))
	recordAWSWaitFailure(&result, request, err)
	decoded := decodeAWSApplyResult(t, marshalAWSApplyResult(&result))
	if len(decoded.Instance.Failed) != 1 || decoded.Instance.Failed[0].Error != awsApplyErrorTimeout {
		t.Fatalf("instance deadline result = %#v, want safe timeout category", decoded.Instance.Failed)
	}
	if len(decoded.Cluster.Failed) != 1 || decoded.Cluster.Failed[0].Error != awsApplyErrorTimeout {
		t.Fatalf("cluster deadline result = %#v, want safe timeout category", decoded.Cluster.Failed)
	}
}

func TestAWSApplyWaitFailureIsRecordedOnlyForItsScope(t *testing.T) {
	result := newAWSApplyResult(awsApplyConfig("instance-custom", "cluster-custom"))
	request := awsApplyWaitRequest{
		Instance: awsrds.ScopePlan{Parameters: []types.Parameter{{ParameterName: aws.String("instance_value")}}},
		Cluster:  awsrds.ScopePlan{Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value")}}},
	}
	recordAWSWaitFailure(&result, request, &awsApplyWaitError{Scope: awsrds.ScopeCluster, Err: errors.New("cluster poll failed")})

	decoded := decodeAWSApplyResult(t, marshalAWSApplyResult(&result))
	if len(decoded.Instance.Failed) != 0 {
		t.Fatalf("instance failures = %#v, want none for cluster polling error", decoded.Instance.Failed)
	}
	if len(decoded.Cluster.Failed) != 1 || !reflect.DeepEqual(decoded.Cluster.Failed[0].Parameters, []string{"cluster_value"}) {
		t.Fatalf("cluster failures = %#v, want only cluster_value", decoded.Cluster.Failed)
	}
}

func installAWSApplyTestDependencies(t *testing.T, client awsrds.Client, onWait func(awsApplyModifiedScopes)) {
	t.Helper()
	originalFactory := newAWSRDSClient
	originalWaiter := waitForAWSApply
	originalReadback := readAWSAppliedParameters
	newAWSRDSClient = func(context.Context, *config.Config) (awsrds.Client, error) {
		return client, nil
	}
	waitForAWSApply = func(_ context.Context, _ awsrds.Client, request awsApplyWaitRequest) error {
		if onWait != nil {
			onWait(request.modifiedScopes())
		}
		return nil
	}
	readAWSAppliedParameters = func(_ context.Context, _ awsrds.ParameterReader, _ awsrds.Scope, plan awsrds.ScopePlan) (map[string]awsrds.ParameterInfo, error) {
		readback := make(map[string]awsrds.ParameterInfo, len(plan.Parameters))
		for _, parameter := range plan.Parameters {
			name := aws.ToString(parameter.ParameterName)
			readback[name] = awsrds.ParameterInfo{
				Name:              name,
				ParameterValue:    aws.ToString(parameter.ParameterValue),
				HasParameterValue: true,
			}
		}
		return readback, nil
	}
	t.Cleanup(func() {
		newAWSRDSClient = originalFactory
		waitForAWSApply = originalWaiter
		readAWSAppliedParameters = originalReadback
	})
}

func auroraApplyClient(writer bool, instanceGroup, clusterGroup string) *awsApplyClientFake {
	return &awsApplyClientFake{
		instanceOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
			DBInstanceIdentifier: aws.String("orders-1"),
			DBInstanceStatus:     aws.String("available"),
			Engine:               aws.String("aurora-mysql"),
			DBClusterIdentifier:  aws.String("orders-cluster"),
			DBParameterGroups: []types.DBParameterGroupStatus{{
				DBParameterGroupName: aws.String(instanceGroup),
				ParameterApplyStatus: aws.String("in-sync"),
			}},
		}}},
		clusterOutput: &rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{{
			DBClusterIdentifier:     aws.String("orders-cluster"),
			DBClusterParameterGroup: aws.String(clusterGroup),
			EngineMode:              aws.String("provisioned"),
			Status:                  aws.String("available"),
			DBClusterMembers: []types.DBClusterMember{{
				DBInstanceIdentifier:          aws.String("orders-1"),
				DBClusterParameterGroupStatus: aws.String("in-sync"),
				IsClusterWriter:               aws.Bool(writer),
			}},
		}}},
		instancePages:          map[string]*rds.DescribeDBParametersOutput{},
		clusterPages:           map[string]*rds.DescribeDBClusterParametersOutput{},
		instanceDescribeErrors: map[string]error{},
		clusterDescribeErrors:  map[string]error{},
		instanceModifyErrors:   map[int]error{},
		clusterModifyErrors:    map[int]error{},
	}
}

func mysqlApplyClient(instanceGroup string) *awsApplyClientFake {
	return &awsApplyClientFake{
		instanceOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
			DBInstanceIdentifier: aws.String("mysql-1"),
			DBInstanceStatus:     aws.String("available"),
			Engine:               aws.String("mysql"),
			DBParameterGroups: []types.DBParameterGroupStatus{{
				DBParameterGroupName: aws.String(instanceGroup),
				ParameterApplyStatus: aws.String("in-sync"),
			}},
		}}},
		instancePages:          map[string]*rds.DescribeDBParametersOutput{},
		clusterPages:           map[string]*rds.DescribeDBClusterParametersOutput{},
		instanceDescribeErrors: map[string]error{},
		clusterDescribeErrors:  map[string]error{},
		instanceModifyErrors:   map[int]error{},
		clusterModifyErrors:    map[int]error{},
	}
}

func modifiableAWSParameter(name, applyType string) types.Parameter {
	return types.Parameter{
		ParameterName: aws.String(name),
		ApplyType:     aws.String(applyType),
		IsModifiable:  aws.Bool(true),
	}
}

func awsApplyConfig(instanceGroup, clusterGroup string) *config.Config {
	return &config.Config{
		InstanceType:                "aws/rds",
		AwsRegion:                   "us-east-1",
		AwsRDSDB:                    "orders-1",
		AwsRDSParameterGroup:        instanceGroup,
		AwsRDSClusterParameterGroup: clusterGroup,
	}
}

func testAWSApplyLogger() logging.Logger {
	return *logging.Init("task-apply-aws-test", true, false, io.Discard)
}

func batchSizes(calls []awsModifyCall) []int {
	sizes := make([]int, len(calls))
	for index, call := range calls {
		sizes[index] = len(call.parameters)
	}
	return sizes
}

func decodeAWSApplyResult(t *testing.T, output string) awsrds.ApplyResult {
	t.Helper()
	var result awsrds.ApplyResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("task output is not ApplyResult JSON: %v; output=%q", err, output)
	}
	return result
}

func mustJSON(t *testing.T, value interface{}) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

type postgreSQLAWSParameterUnitContract struct {
	Recommendations    json.RawMessage   `json:"recommendations"`
	AWSParameterValues map[string]string `json:"aws_parameter_values"`
}

func loadPostgreSQLAWSParameterUnitContract(t *testing.T) postgreSQLAWSParameterUnitContract {
	t.Helper()
	path := filepath.Join("..", "awsrds", "testdata", "postgresql_aws_parameter_units_v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read PostgreSQL AWS parameter-unit contract %q: %v", path, err)
	}
	var contract postgreSQLAWSParameterUnitContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("decode PostgreSQL AWS parameter-unit contract %q: %v", path, err)
	}
	return contract
}

func awsModifiedParameterValues(t *testing.T, calls []awsModifyCall) map[string]string {
	t.Helper()
	values := make(map[string]string)
	for _, call := range calls {
		for _, parameter := range call.parameters {
			name := aws.ToString(parameter.ParameterName)
			if _, exists := values[name]; exists {
				t.Fatalf("AWS parameter %q was modified more than once", name)
			}
			values[name] = aws.ToString(parameter.ParameterValue)
		}
	}
	return values
}

func TestAWSApplyFailureOutputDoesNotContainConfigSecrets(t *testing.T) {
	const errorSecret = "raw-aws-error-recommendation-secret-200"
	const endpointSecret = "private-db-endpoint.example"
	client := mysqlApplyClient("instance-custom")
	client.instanceOutput.DBInstances[0].Endpoint = &types.Endpoint{Address: aws.String(endpointSecret)}
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("max_connections", "dynamic")},
	}}
	client.instanceModifyErrors = map[int]error{1: errors.New("request failed with value " + errorSecret)}
	installAWSApplyTestDependencies(t, client, nil)
	cfg := awsApplyConfig("instance-custom", "")
	cfg.ApiKey = "super-secret-api-key"
	cfg.MysqlUser = "super-secret-db-user"
	cfg.MysqlPassword = "super-secret-db-password"
	var logOutput bytes.Buffer
	logger := *logging.Init("task-apply-aws-secret-test", false, false, &logOutput)

	_, _, output := applyConfAWSRDS(
		&awsApplyRepeater{recommendations: `{"max_connections":"200"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"max_connections": "100"}}},
		logger,
		cfg,
		AWSApplyAll,
		awsApplyTaskContext{TaskID: 42, TaskTypeID: 4},
	)

	combined := output + logOutput.String()
	for _, secret := range []string{endpointSecret, cfg.ApiKey, cfg.MysqlUser, cfg.MysqlPassword, errorSecret} {
		if strings.Contains(combined, secret) {
			t.Fatalf("AWS apply output or audit event leaked %q: %s", secret, combined)
		}
	}
	events := decodeAWSApplyLogEvents(t, logOutput.String())
	if got := len(awsApplyLogEvents(events, "aws_rds_apply_audit")); got != 1 {
		t.Fatalf("terminal audit events = %d, want exactly one; events=%#v", got, events)
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Instance.Failed) != 1 || result.Instance.Failed[0].Error != awsApplyErrorAWSAPI {
		t.Fatalf("sanitized failure = %#v, want category %q", result.Instance.Failed, awsApplyErrorAWSAPI)
	}
}
