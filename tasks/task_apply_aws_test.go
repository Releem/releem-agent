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
	err             error
	panicValue      any
}

type testAWSAPIError struct {
	code string
}

func (e testAWSAPIError) Error() string {
	return "sensitive AWS error message"
}

func (e testAWSAPIError) ErrorCode() string {
	return e.code
}

func (r *awsApplyRepeater) ProcessMetrics(_ models.MetricContext, _ models.Metrics, mode models.ModeType) (string, error) {
	if mode == (models.ModeType{Name: "Configurations", Type: "GetJson"}) {
		r.calls++
		if r.panicValue != nil {
			panic(r.panicValue)
		}
		return r.recommendations, r.err
	}
	return "", nil
}

func TestApplyConfAwsRdsConvertsRecommendationRepeaterPanicToFailure(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{panicValue: "recommendation panic"},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"max_connections": "100"}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", ""),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitFailure || status != awsApplyTaskStatusFailure {
		t.Fatalf("ApplyConfAwsRds(repeater panic) = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitFailure, awsApplyTaskStatusFailure, output)
	}
	if client.describeInstanceCalls != 0 || client.describeClusterCalls != 0 {
		t.Fatalf("ApplyConfAwsRds(repeater panic) discovery calls = instance %d cluster %d, want 0/0", client.describeInstanceCalls, client.describeClusterCalls)
	}
}

// TestApplyConfAwsRdsIgnoresRecommendationRepeaterErrorWithValidJSON
// documents a change in this branch: applyConfAWSRDS now calls
// utils.ProcessRepeaters, which only logs a repeater error and still returns
// whatever JSON body the repeater produced. A repeater that returns valid
// JSON alongside a non-nil error therefore no longer fails the apply task
// (unlike utils.ProcessRepeatersWithError, which does not exist in this
// branch's utils package) — the error is swallowed and processing continues
// as if the recommendations were valid.
func TestApplyConfAwsRdsIgnoresRecommendationRepeaterErrorWithValidJSON(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("max_connections", "dynamic")},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{
			recommendations: `{"max_connections":"200"}`,
			err:             errors.New("partial recommendation response"),
		},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"max_connections": "100"}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", ""),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds(repeater error) = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitSuccess, awsApplyTaskStatusSuccess, output)
	}
	if len(client.instanceModifyCalls) != 1 {
		t.Fatalf("ApplyConfAwsRds(repeater error) instance modify calls = %d, want 1: valid JSON is applied despite the swallowed repeater error", len(client.instanceModifyCalls))
	}
}

func TestApplyConfAwsRdsSerializationFailureCannotReturnSuccess(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {}}
	installAWSApplyTestDependencies(t, client, nil)

	originalEncoder := encodeAWSApplyResult
	encodeAWSApplyResult = func(*awsrds.ApplyResult) ([]byte, error) {
		return nil, errors.New("serialize apply result")
	}
	t.Cleanup(func() {
		encodeAWSApplyResult = originalEncoder
	})

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", ""),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitFailure || status != awsApplyTaskStatusFailure {
		t.Fatalf("ApplyConfAwsRds(serialization failure) = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitFailure, awsApplyTaskStatusFailure, output)
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Instance.Failed) != 1 || result.Instance.Failed[0].Error != "serialize AWS apply result" {
		t.Fatalf("ApplyConfAwsRds(serialization failure) failures = %#v, want serialization failure", result.Instance.Failed)
	}
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
	waitInstanceStatus   string
	waitClusterStatus    string
}

func (f *awsApplyClientFake) DescribeDBInstances(_ context.Context, _ *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	f.describeInstanceCalls++
	return f.instanceOutput, nil
}

func (f *awsApplyClientFake) DescribeDBClusters(_ context.Context, _ *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	f.describeClusterCalls++
	return f.clusterOutput, nil
}

func (f *awsApplyClientFake) DescribeGlobalClusters(context.Context, *rds.DescribeGlobalClustersInput, ...func(*rds.Options)) (*rds.DescribeGlobalClustersOutput, error) {
	return nil, errors.New("awsApplyClientFake: DescribeGlobalClusters not implemented")
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
		{name: "applying", status: "applying", wantExit: awsApplyExitClusterParameterGroupNotInSync},
		{name: "pending reboot", status: "pending-reboot", wantExit: awsApplyExitClusterParameterGroupNotInSync},
		{name: "missing status", status: "", wantExit: awsApplyExitClusterParameterGroupNotInSync},
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
			// Cluster readiness is gated before the cluster group is ever read,
			// so only the instance group is classified and nothing is modified.
			if !reflect.DeepEqual(client.instanceDescribeGroups, []string{"instance-custom"}) ||
				len(client.clusterDescribeGroups) != 0 ||
				len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
				t.Fatalf("blocked apply parameter APIs = instance lists %#v cluster lists %#v instance modifies %d cluster modifies %d, want instance-only classification and no modifies", client.instanceDescribeGroups, client.clusterDescribeGroups, len(client.instanceModifyCalls), len(client.clusterModifyCalls))
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
	client.waitClusterStatus = "pending-reboot"
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

	if exitCode != awsApplyExitPendingReboot || status != awsApplyTaskStatusFailure {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitPendingReboot, awsApplyTaskStatusFailure, output)
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

func TestApplyConfAwsRdsRejectsInstanceGroupMismatchBeforePlanning(t *testing.T) {
	tests := []struct {
		name             string
		attachedInstance string
		attachedCluster  string
		wantScope        awsrds.Scope
		wantExpected     string
		wantActual       string
	}{
		{
			name:             "instance mismatch blocks every scope",
			attachedInstance: "other-instance",
			attachedCluster:  "cluster-custom",
			wantScope:        awsrds.ScopeInstance,
			wantExpected:     "instance-custom",
			wantActual:       "other-instance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, tt.attachedInstance, tt.attachedCluster)
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"instance_value":"2","cluster_value":"2"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"instance_value": "1", "cluster_value": "1"}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", "cluster-custom"),
				AWSApplyAll,
			)

			if exitCode != 3 || status != awsApplyTaskStatusFailure {
				t.Fatalf("ApplyConfAwsRds() = exit %d status %d, want 3/%d; output %s", exitCode, status, awsApplyTaskStatusFailure, output)
			}
			if len(client.instanceDescribeGroups) != 0 || len(client.clusterDescribeGroups) != 0 || len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
				t.Fatalf("ApplyConfAwsRds() reached parameter APIs after mismatch: instance lists %#v, cluster lists %#v, instance modifies %d, cluster modifies %d", client.instanceDescribeGroups, client.clusterDescribeGroups, len(client.instanceModifyCalls), len(client.clusterModifyCalls))
			}
			result := decodeAWSApplyResult(t, output)
			var mismatchResult, matchingResult awsrds.ScopeResult
			if tt.wantScope == awsrds.ScopeInstance {
				mismatchResult = result.Instance
				matchingResult = result.Cluster
			} else {
				mismatchResult = result.Cluster
				matchingResult = result.Instance
			}
			if len(mismatchResult.Failed) != 1 || mismatchResult.Failed[0].Error != "parameter-group-mismatch" {
				t.Fatalf("mismatch scope failure = %#v, want parameter-group-mismatch", mismatchResult.Failed)
			}
			if len(mismatchResult.Diagnostics) != 1 || mismatchResult.Diagnostics[0].Reason != awsrds.SkipGroupMismatch || mismatchResult.Diagnostics[0].ExpectedGroup != tt.wantExpected || mismatchResult.Diagnostics[0].ActualGroup != tt.wantActual {
				t.Fatalf("mismatch scope diagnostics = %#v, want expected %q actual %q", mismatchResult.Diagnostics, tt.wantExpected, tt.wantActual)
			}
			if len(matchingResult.Failed) != 0 || len(matchingResult.Diagnostics) != 0 {
				t.Fatalf("matching scope result = %#v, want no failure or diagnostic", matchingResult)
			}
		})
	}
}

func TestApplyConfAwsRdsClusterGroupMismatchDoesNotBlockInstanceOnlyPlan(t *testing.T) {
	client := auroraApplyClient(true, "instance-custom", "attached-cluster")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("instance_value", "dynamic")},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"instance_value":"2"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"instance_value": "1"}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", "configured-cluster"),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds(instance-only plan) = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitSuccess, awsApplyTaskStatusSuccess, output)
	}
	if len(client.instanceModifyCalls) != 1 || len(client.clusterModifyCalls) != 0 {
		t.Fatalf("ApplyConfAwsRds(instance-only plan) modify calls = instance %d cluster %d, want 1/0", len(client.instanceModifyCalls), len(client.clusterModifyCalls))
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Cluster.Failed) != 0 || len(result.Cluster.Diagnostics) != 0 {
		t.Fatalf("ApplyConfAwsRds(instance-only plan) cluster result = %#v, want no cluster failure", result.Cluster)
	}
}

// TestApplyConfAwsRdsRejectsClusterParameterWithoutMutableClusterGroup pins the
// current contract: once a recommendation needs cluster classification, an
// unconfigured, mismatched, or AWS-managed default cluster group is a hard
// precondition failure for the whole task (mirroring the instance-group guard)
// rather than a per-parameter skip.
func TestApplyConfAwsRdsRejectsClusterParameterWithoutMutableClusterGroup(t *testing.T) {
	tests := []struct {
		name            string
		configuredGroup string
		attachedGroup   string
	}{
		{
			name:          "empty configured group",
			attachedGroup: "attached-cluster",
		},
		{
			name:            "configured group mismatch",
			configuredGroup: "configured-cluster",
			attachedGroup:   "attached-cluster",
		},
		{
			name:            "AWS-managed default group",
			configuredGroup: "default.aurora-mysql8.0",
			attachedGroup:   "default.aurora-mysql8.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, "instance-custom", tt.attachedGroup)
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

			if exitCode != awsApplyExitClusterParameterGroupMismatch || status != awsApplyTaskStatusFailure {
				t.Fatalf("ApplyConfAwsRds(%s) = exit %d status %d, want %d/%d; output %s", tt.name, exitCode, status, awsApplyExitClusterParameterGroupMismatch, awsApplyTaskStatusFailure, output)
			}
			// The group is rejected before it is ever read.
			if len(client.clusterDescribeGroups) != 0 {
				t.Fatalf("ApplyConfAwsRds(%s) cluster describe calls = %v, want none", tt.name, client.clusterDescribeGroups)
			}
			if len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
				t.Fatalf("ApplyConfAwsRds(%s) modify calls = instance %d cluster %d, want 0/0", tt.name, len(client.instanceModifyCalls), len(client.clusterModifyCalls))
			}
			result := decodeAWSApplyResult(t, output)
			if len(result.Cluster.Failed) != 1 || result.Cluster.Failed[0].Error != awsApplyErrorParameterGroupMismatch {
				t.Fatalf("ApplyConfAwsRds(%s) cluster result = %#v, want one parameter-group-mismatch failure", tt.name, result.Cluster)
			}
		})
	}
}

func TestApplyConfAwsRdsSkipsIneligibleClusterParameterForMatchingGroup(t *testing.T) {
	client := auroraApplyClient(true, "instance-custom", "cluster-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {}}
	unmodifiable := modifiableAWSParameter("cluster_value", "dynamic")
	unmodifiable.IsModifiable = aws.Bool(false)
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
		Parameters: []types.Parameter{unmodifiable},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"cluster_value":"2"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"cluster_value": "1"}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", "cluster-custom"),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("ApplyConfAwsRds(ineligible cluster parameter) = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitSuccess, awsApplyTaskStatusSuccess, output)
	}
	result := decodeAWSApplyResult(t, output)
	if !reflect.DeepEqual(result.Cluster.Skipped, []awsrds.SkippedVariable{{Name: "cluster_value", Reason: awsrds.SkipUnmodifiable}}) {
		t.Fatalf("ApplyConfAwsRds(ineligible cluster parameter) cluster skips = %#v, want unmodifiable", result.Cluster.Skipped)
	}
}

func TestApplyConfAwsRdsGroupMismatchPrecedesParameterGroupReadiness(t *testing.T) {
	tests := []struct {
		name                string
		attachedInstance    string
		attachedCluster     string
		instanceGroupStatus string
		clusterGroupStatus  string
	}{
		{
			name:                "instance mismatch takes precedence over applying instance group",
			attachedInstance:    "other-instance",
			attachedCluster:     "cluster-custom",
			instanceGroupStatus: "applying",
			clusterGroupStatus:  "in-sync",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, tt.attachedInstance, tt.attachedCluster)
			client.instanceOutput.DBInstances[0].DBParameterGroups[0].ParameterApplyStatus = aws.String(tt.instanceGroupStatus)
			client.clusterOutput.DBClusters[0].DBClusterMembers[0].DBClusterParameterGroupStatus = aws.String(tt.clusterGroupStatus)
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"instance_value":"2","cluster_value":"2"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", "cluster-custom"),
				AWSApplyAll,
			)

			if exitCode != 3 || status != awsApplyTaskStatusFailure {
				t.Fatalf("ApplyConfAwsRds(instance status %q, cluster status %q) = exit %d status %d, want 3/%d; output %s", tt.instanceGroupStatus, tt.clusterGroupStatus, exitCode, status, awsApplyTaskStatusFailure, output)
			}
		})
	}
}

func TestApplyConfAwsRdsReportsInstanceGroupMismatchOnceAtScopeLevel(t *testing.T) {
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

	if exitCode != 3 || status != awsApplyTaskStatusFailure {
		t.Fatalf("ApplyConfAwsRds() = exit %d status %d, want 3/%d; output %s", exitCode, status, awsApplyTaskStatusFailure, output)
	}
	if len(client.instanceDescribeGroups) != 0 || len(client.clusterDescribeGroups) != 0 || len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
		t.Fatalf("ApplyConfAwsRds() reached parameter APIs after mismatches: instance lists %#v, cluster lists %#v, instance modifies %d, cluster modifies %d", client.instanceDescribeGroups, client.clusterDescribeGroups, len(client.instanceModifyCalls), len(client.clusterModifyCalls))
	}

	type scopeOutput struct {
		Diagnostics []struct {
			Reason        awsrds.SkipReason `json:"reason"`
			ExpectedGroup string            `json:"expected_group"`
			ActualGroup   string            `json:"actual_group"`
		} `json:"diagnostics"`
		Failed []awsrds.FailedBatch `json:"failed"`
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
	if len(structured.Instance.Failed) != 1 || structured.Instance.Failed[0].Error != "parameter-group-mismatch" {
		t.Fatalf("instance failures = %#v, want one parameter-group-mismatch", structured.Instance.Failed)
	}
	if len(structured.Cluster.Failed) != 0 || len(structured.Cluster.Diagnostics) != 0 {
		t.Fatalf("cluster mismatch output = %#v, want cluster validation deferred after instance mismatch", structured.Cluster)
	}
	for _, secret := range []string{cfg.ApiKey, cfg.MysqlPassword} {
		if strings.Contains(output, secret) {
			t.Fatalf("mismatch output leaked %q: %s", secret, output)
		}
	}
}

func TestApplyConfAwsRdsReaderClusterGroupNeverModifiesCluster(t *testing.T) {
	tests := []struct {
		name            string
		writer          bool
		configuredGroup string
		wantReason      awsrds.SkipReason
	}{
		{name: "fresh reader role", writer: false, configuredGroup: "cluster-custom", wantReason: awsrds.SkipNotClusterWriter},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The cluster group is left in-sync (auroraApplyClient's default) so
			// the reader clears the now-unconditional readiness gate and reaches
			// BuildApplyPlan, where clusterTopologySkip rejects the mutation
			// because the instance is not the cluster writer.
			client := auroraApplyClient(tt.writer, "instance-custom", "cluster-custom")
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

// TestApplyConfAwsRdsReaderStillRequiresClusterReadiness pins the current
// contract: the Aurora cluster readiness gate runs for every Aurora instance
// before any scope routing, so a reader is blocked by a cluster group that is
// mid-apply even though it could never submit a cluster change itself.
func TestApplyConfAwsRdsReaderStillRequiresClusterReadiness(t *testing.T) {
	client := auroraApplyClient(false, "instance-custom", "cluster-custom")
	client.clusterOutput.DBClusters[0].DBClusterMembers[0].DBClusterParameterGroupStatus = aws.String("applying")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {}}
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("cluster_value", "dynamic")},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"cluster_value":"2"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"cluster_value": "1"}}},
		testAWSApplyLogger(),
		awsApplyConfig("instance-custom", "cluster-custom"),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitClusterParameterGroupNotInSync || status != awsApplyTaskStatusFailure {
		t.Fatalf("ApplyConfAwsRds(reader, cluster applying) = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitClusterParameterGroupNotInSync, awsApplyTaskStatusFailure, output)
	}
	if len(client.clusterModifyCalls) != 0 {
		t.Fatalf("ApplyConfAwsRds(reader, cluster applying) cluster modify calls = %d, want 0", len(client.clusterModifyCalls))
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Cluster.Failed) != 1 || result.Cluster.Failed[0].Error != awsApplyErrorParameterGroupNotInSync {
		t.Errorf("ApplyConfAwsRds(reader, cluster applying) cluster result = %#v, want one parameter-group-not-in-sync failure", result.Cluster)
	}
}

// TestApplyConfAwsRdsInstanceOnlyPlanStillRequiresClusterReadiness pins the
// same gate for a writer whose recommendations are entirely instance-scoped:
// readiness is checked before classification, so the cluster group blocks the
// task even though no cluster parameter would have been touched.
func TestApplyConfAwsRdsInstanceOnlyPlanStillRequiresClusterReadiness(t *testing.T) {
	for _, clusterStatus := range []string{"applying", "pending-reboot"} {
		t.Run(clusterStatus, func(t *testing.T) {
			client := auroraApplyClient(true, "instance-custom", "cluster-custom")
			client.clusterOutput.DBClusters[0].DBClusterMembers[0].DBClusterParameterGroupStatus = aws.String(clusterStatus)
			client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("instance_value", "dynamic")},
			}}
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"instance_value":"2"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"instance_value": "1"}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", "cluster-custom"),
				AWSApplyAll,
			)

			if exitCode != awsApplyExitClusterParameterGroupNotInSync || status != awsApplyTaskStatusFailure {
				t.Fatalf("ApplyConfAwsRds(instance-only, cluster %s) = exit %d status %d, want %d/%d; output %s", clusterStatus, exitCode, status, awsApplyExitClusterParameterGroupNotInSync, awsApplyTaskStatusFailure, output)
			}
			if len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
				t.Errorf("ApplyConfAwsRds(instance-only, cluster %s) modify calls = %d/%d, want 0/0", clusterStatus, len(client.instanceModifyCalls), len(client.clusterModifyCalls))
			}
		})
	}
}

// TestApplyConfAwsRdsUnknownRecommendationRequiresClusterGroup pins the current
// contract: any recommendation missing from the instance group triggers cluster
// classification, so an unconfigured or AWS-managed default cluster group fails
// the whole task before the otherwise-valid instance parameters are applied.
func TestApplyConfAwsRdsUnknownRecommendationRequiresClusterGroup(t *testing.T) {
	tests := []struct {
		name                   string
		configuredClusterGroup string
		attachedClusterGroup   string
	}{
		{
			name:                 "missing cluster config",
			attachedClusterGroup: "attached-cluster",
		},
		{
			name:                   "default cluster group",
			configuredClusterGroup: "default.aurora-mysql8.0",
			attachedClusterGroup:   "default.aurora-mysql8.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, "instance-custom", tt.attachedClusterGroup)
			client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("instance_value", "dynamic")},
			}}
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"instance_value":"2","unknown_value":"3"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"instance_value": "1"}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", tt.configuredClusterGroup),
				AWSApplyAll,
			)

			if exitCode != awsApplyExitClusterParameterGroupMismatch || status != awsApplyTaskStatusFailure {
				t.Fatalf("ApplyConfAwsRds() = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitClusterParameterGroupMismatch, awsApplyTaskStatusFailure, output)
			}
			// The task aborts before applying anything, including the
			// instance-scoped parameter that was otherwise ready to submit.
			if len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
				t.Fatalf("modify calls = instance %d cluster %d, want 0/0", len(client.instanceModifyCalls), len(client.clusterModifyCalls))
			}
			if len(client.clusterDescribeGroups) != 0 {
				t.Errorf("ApplyConfAwsRds(invalid cluster group) classification reads = %v, want none", client.clusterDescribeGroups)
			}
			result := decodeAWSApplyResult(t, output)
			if len(result.Cluster.Failed) != 1 || result.Cluster.Failed[0].Error != awsApplyErrorParameterGroupMismatch {
				t.Errorf("ApplyConfAwsRds(invalid cluster group) cluster result = %#v, want one parameter-group-mismatch failure", result.Cluster)
			}
		})
	}
}

func TestApplyConfAwsRdsEmptyInstanceGroupReturnsExitThree(t *testing.T) {
	client := auroraApplyClient(true, "attached-instance", "cluster-custom")
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"shared_name":"2"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"shared_name": "1"}}},
		testAWSApplyLogger(),
		awsApplyConfig("", "cluster-custom"),
		AWSApplyAll,
	)

	if exitCode != 3 || status != awsApplyTaskStatusFailure {
		t.Fatalf("ApplyConfAwsRds(empty instance group) = exit %d status %d, want 3/%d; output %s", exitCode, status, awsApplyTaskStatusFailure, output)
	}
	if len(client.instanceDescribeGroups) != 0 || len(client.clusterDescribeGroups) != 0 || len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
		t.Fatalf("ApplyConfAwsRds(empty instance group) reached parameter APIs: instance lists %#v, cluster lists %#v, instance modifies %d, cluster modifies %d", client.instanceDescribeGroups, client.clusterDescribeGroups, len(client.instanceModifyCalls), len(client.clusterModifyCalls))
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Instance.Failed) != 1 || result.Instance.Failed[0].Error != "parameter-group-mismatch" {
		t.Fatalf("instance failures = %#v, want one parameter-group-mismatch", result.Instance.Failed)
	}
	if len(result.Instance.Diagnostics) != 1 || result.Instance.Diagnostics[0].Reason != awsrds.SkipGroupMismatch || result.Instance.Diagnostics[0].ExpectedGroup != "" || result.Instance.Diagnostics[0].ActualGroup != "attached-instance" {
		t.Fatalf("instance diagnostics = %#v, want empty configured group and attached-instance", result.Instance.Diagnostics)
	}
}

func TestApplyConfAwsRdsPendingRebootReturnsExitTenForEitherScope(t *testing.T) {
	tests := []struct {
		name      string
		client    *awsApplyClientFake
		config    *config.Config
		variable  string
		wantScope awsrds.Scope
	}{
		{
			name:      "instance parameter",
			client:    mysqlApplyClient("instance-custom"),
			config:    awsApplyConfig("instance-custom", ""),
			variable:  "instance_static",
			wantScope: awsrds.ScopeInstance,
		},
		{
			name:      "cluster parameter",
			client:    auroraApplyClient(true, "instance-custom", "cluster-custom"),
			config:    awsApplyConfig("instance-custom", "cluster-custom"),
			variable:  "cluster_static",
			wantScope: awsrds.ScopeCluster,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantScope == awsrds.ScopeInstance {
				tt.client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
					Parameters: []types.Parameter{modifiableAWSParameter(tt.variable, "static")},
				}}
				tt.client.waitInstanceStatus = "pending-reboot"
			} else {
				tt.client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {}}
				tt.client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
					Parameters: []types.Parameter{modifiableAWSParameter(tt.variable, "static")},
				}}
				tt.client.waitClusterStatus = "pending-reboot"
			}
			installAWSApplyTestDependencies(t, tt.client, nil)

			recommendations := fmt.Sprintf(`{"%s":"2"}`, tt.variable)
			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: recommendations},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{tt.variable: "1"}}},
				testAWSApplyLogger(),
				tt.config,
				AWSApplyAll,
			)

			if exitCode != 10 || status != awsApplyTaskStatusFailure {
				t.Fatalf("ApplyConfAwsRds(%s pending reboot) = exit %d status %d, want 10/%d; output %s", tt.wantScope, exitCode, status, awsApplyTaskStatusFailure, output)
			}
			result := decodeAWSApplyResult(t, output)
			gotApplied := result.Instance.Applied
			if tt.wantScope == awsrds.ScopeCluster {
				gotApplied = result.Cluster.Applied
			}
			if !reflect.DeepEqual(gotApplied, []string{tt.variable}) {
				t.Fatalf("ApplyConfAwsRds(%s pending reboot) applied = %#v, want [%s]", tt.wantScope, gotApplied, tt.variable)
			}
		})
	}
}

func TestApplyConfAwsRdsExitTenUsesAppliedGroupStatus(t *testing.T) {
	tests := []struct {
		name        string
		scope       awsrds.Scope
		applyType   string
		groupStatus string
		wantExit    int
		wantStatus  int
	}{
		{
			name:        "instance dynamic parameter leaves group pending reboot",
			scope:       awsrds.ScopeInstance,
			applyType:   "dynamic",
			groupStatus: "pending-reboot",
			wantExit:    awsApplyExitPendingReboot,
			wantStatus:  awsApplyTaskStatusFailure,
		},
		{
			name:        "cluster dynamic parameter leaves group pending reboot",
			scope:       awsrds.ScopeCluster,
			applyType:   "dynamic",
			groupStatus: "pending-reboot",
			wantExit:    awsApplyExitPendingReboot,
			wantStatus:  awsApplyTaskStatusFailure,
		},
		{
			name:        "instance static parameter leaves group in sync",
			scope:       awsrds.ScopeInstance,
			applyType:   "static",
			groupStatus: "in-sync",
			wantExit:    awsApplyExitSuccess,
			wantStatus:  awsApplyTaskStatusSuccess,
		},
		{
			name:        "cluster static parameter leaves group in sync",
			scope:       awsrds.ScopeCluster,
			applyType:   "static",
			groupStatus: "in-sync",
			wantExit:    awsApplyExitSuccess,
			wantStatus:  awsApplyTaskStatusSuccess,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, "instance-custom", "cluster-custom")
			client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {}}
			variable := "cluster_value"
			configuration := awsApplyConfig("instance-custom", "cluster-custom")
			if tt.scope == awsrds.ScopeInstance {
				variable = "instance_value"
				client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
					Parameters: []types.Parameter{modifiableAWSParameter(variable, tt.applyType)},
				}}
				client.waitInstanceStatus = tt.groupStatus
			} else {
				client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
					Parameters: []types.Parameter{modifiableAWSParameter(variable, tt.applyType)},
				}}
				client.waitClusterStatus = tt.groupStatus
			}
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, status, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: fmt.Sprintf(`{"%s":"2"}`, variable)},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{variable: "1"}}},
				testAWSApplyLogger(),
				configuration,
				AWSApplyAll,
			)

			if exitCode != tt.wantExit || status != tt.wantStatus {
				t.Fatalf("ApplyConfAwsRds(%s, %s) = exit %d status %d, want %d/%d; output %s", tt.scope, tt.groupStatus, exitCode, status, tt.wantExit, tt.wantStatus, output)
			}
		})
	}
}

// TestApplyConfAwsRdsDefaultInstanceGroupBlocksClusterOnlyApply pins the
// current contract: an AWS-managed default instance parameter group is a hard
// precondition failure, so a cluster-only recommendation is never reached even
// though its own cluster group is a valid mutation target.
func TestApplyConfAwsRdsDefaultInstanceGroupBlocksClusterOnlyApply(t *testing.T) {
	const defaultGroup = "default.aurora-mysql8.0"
	client := auroraApplyClient(true, defaultGroup, "cluster-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {}}
	client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("cluster_value", "dynamic")},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"cluster_value":"2"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"cluster_value": "1"}}},
		testAWSApplyLogger(),
		awsApplyConfig(defaultGroup, "cluster-custom"),
		AWSApplyAll,
	)

	if exitCode != awsApplyExitParameterGroupMismatch || status != awsApplyTaskStatusFailure {
		t.Fatalf("ApplyConfAwsRds(default instance, cluster-only) = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitParameterGroupMismatch, awsApplyTaskStatusFailure, output)
	}
	// The default group is rejected before either group is read.
	if len(client.instanceDescribeGroups) != 0 || len(client.clusterDescribeGroups) != 0 {
		t.Errorf("ApplyConfAwsRds(default instance, cluster-only) parameter reads = instance %v cluster %v, want none", client.instanceDescribeGroups, client.clusterDescribeGroups)
	}
	if len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
		t.Errorf("ApplyConfAwsRds(default instance, cluster-only) modify calls = %d/%d, want 0/0", len(client.instanceModifyCalls), len(client.clusterModifyCalls))
	}
	result := decodeAWSApplyResult(t, output)
	if len(result.Instance.Failed) != 1 || result.Instance.Failed[0].Error != awsApplyErrorParameterGroupMismatch {
		t.Errorf("ApplyConfAwsRds(default instance, cluster-only) result = %#v, want one instance parameter-group-mismatch failure", result)
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

func TestAWSApplySafeErrorCodePublishesOnlyConstrainedAWSCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil error", err: nil, want: awsApplyErrorAWSAPI},
		{name: "wait timeout", err: errAWSApplyWaitTimeout, want: awsApplyErrorTimeout},
		{name: "wrapped wait timeout", err: fmt.Errorf("waiting: %w", errAWSApplyWaitTimeout), want: awsApplyErrorTimeout},
		{name: "access denied API code", err: testAWSAPIError{code: "AccessDeniedException"}, want: awsApplyErrorAccessDenied},
		{name: "access denied message", err: errors.New("AWS ACCESSDENIED while applying"), want: awsApplyErrorAccessDenied},
		{name: "safe alphanumeric code", err: testAWSAPIError{code: "InvalidParameterValue123"}, want: "aws-api:InvalidParameterValue123"},
		{name: "safe punctuation", err: testAWSAPIError{code: "RDS.Invalid_Parameter-Code"}, want: "aws-api:RDS.Invalid_Parameter-Code"},
		{name: "empty code is redacted", err: testAWSAPIError{}, want: awsApplyErrorAWSAPI},
		{name: "whitespace is redacted", err: testAWSAPIError{code: "Invalid Parameter"}, want: awsApplyErrorAWSAPI},
		{name: "delimiter is redacted", err: testAWSAPIError{code: "Invalid:secret"}, want: awsApplyErrorAWSAPI},
		{name: "newline is redacted", err: testAWSAPIError{code: "Invalid\nsecret"}, want: awsApplyErrorAWSAPI},
		{name: "overlong code is redacted", err: testAWSAPIError{code: strings.Repeat("A", 129)}, want: awsApplyErrorAWSAPI},
		{name: "ordinary error is redacted", err: errors.New("database endpoint and secret"), want: awsApplyErrorAWSAPI},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := awsApplySafeErrorCode(tt.err); got != tt.want {
				t.Fatalf("awsApplySafeErrorCode(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
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
				t.Fatalf("ApplyConfAwsRds() = exit %d status %d, want %d/%d; output %s", exitCode, status, awsApplyExitSuccess, awsApplyTaskStatusSuccess, output)
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

func TestAWSApplyWaiterAcceptsPendingRebootAndPollsOnlyModifiedScopes(t *testing.T) {
	tests := []struct {
		name                  string
		client                *awsApplyClientFake
		request               awsApplyWaitRequest
		wantInstancePollCalls int
		wantClusterPollCalls  int
		wantStatuses          awsApplyGroupStatuses
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
			wantStatuses:          awsApplyGroupStatuses{Instance: "pending-reboot"},
		},
		{
			name: "cluster pending reboot does not poll instance",
			client: func() *awsApplyClientFake {
				client := auroraApplyClient(true, "instance-custom", "cluster-custom")
				client.clusterOutput.DBClusters[0].DBClusterMembers[0].DBClusterParameterGroupStatus = aws.String("pending-reboot")
				client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
					Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value"), ParameterValue: aws.String("ROW")}},
				}}
				return client
			}(),
			request: awsApplyWaitRequest{
				Cluster: awsrds.ScopePlan{Group: "cluster-custom", Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value"), ParameterValue: aws.String("ROW")}}},
			},
			wantClusterPollCalls: 1,
			wantStatuses:         awsApplyGroupStatuses{Cluster: "pending-reboot"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := awsrds.Metadata{
				DBInstanceIdentifier: "orders-1",
				DBClusterIdentifier:  "orders-cluster",
			}
			statuses, err := defaultWaitForAWSApply(context.Background(), tt.client, tt.request, metadata)
			if err != nil {
				t.Fatalf("defaultWaitForAWSApply() error = %v", err)
			}
			if statuses != tt.wantStatuses {
				t.Errorf("defaultWaitForAWSApply() statuses = %#v, want %#v", statuses, tt.wantStatuses)
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

func TestAWSApplyWaiterFailsFastOnFailedStatus(t *testing.T) {
	tests := []struct {
		name    string
		client  *awsApplyClientFake
		request awsApplyWaitRequest
	}{
		{
			name: "instance apply status failed",
			client: func() *awsApplyClientFake {
				client := mysqlApplyClient("instance-custom")
				client.instanceOutput.DBInstances[0].DBParameterGroups[0].ParameterApplyStatus = aws.String("failed")
				client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
					Parameters: []types.Parameter{{ParameterName: aws.String("static_value"), ParameterValue: aws.String("2")}},
				}}
				return client
			}(),
			request: awsApplyWaitRequest{
				Instance: awsrds.ScopePlan{Group: "instance-custom", Parameters: []types.Parameter{{ParameterName: aws.String("static_value"), ParameterValue: aws.String("2")}}},
			},
		},
		{
			name: "cluster apply status failed",
			client: func() *awsApplyClientFake {
				client := auroraApplyClient(true, "instance-custom", "cluster-custom")
				client.clusterOutput.DBClusters[0].DBClusterMembers[0].DBClusterParameterGroupStatus = aws.String("failed")
				client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
					Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value"), ParameterValue: aws.String("ROW")}},
				}}
				return client
			}(),
			request: awsApplyWaitRequest{
				Cluster: awsrds.ScopePlan{Group: "cluster-custom", Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value"), ParameterValue: aws.String("ROW")}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := awsrds.Metadata{
				DBInstanceIdentifier: "orders-1",
				DBClusterIdentifier:  "orders-cluster",
			}
			started := time.Now()
			_, err := defaultWaitForAWSApply(context.Background(), tt.client, tt.request, metadata)
			if err == nil {
				t.Fatal("defaultWaitForAWSApply() error = nil, want a fast failure for AWS-reported failed status")
			}
			if elapsed := time.Since(started); elapsed >= awsApplyPollInterval {
				t.Fatalf("defaultWaitForAWSApply() elapsed = %v, want failure before the first poll interval elapses (no retry on a failed status)", elapsed)
			}
			if tt.client.describeInstanceCalls > 1 || tt.client.describeClusterCalls > 1 {
				t.Fatalf("poll calls = instance %d cluster %d, want at most one call per scope before failing fast", tt.client.describeInstanceCalls, tt.client.describeClusterCalls)
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
		Instance: awsrds.ScopePlan{
			Group:      "instance-custom",
			Parameters: []types.Parameter{{ParameterName: aws.String("max_connections"), ParameterValue: aws.String("200")}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := defaultWaitForAWSApply(ctx, client, request, awsrds.Metadata{DBInstanceIdentifier: "mysql-1"})
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
	decoded := decodeAWSApplyResult(t, mustMarshalAWSApplyResult(t, &result))
	if len(decoded.Cluster.Failed) != 1 || decoded.Cluster.Failed[0].Error != awsApplyErrorTimeout {
		t.Fatalf("timeout result = %#v, want safe category %q", decoded.Cluster.Failed, awsApplyErrorTimeout)
	}
}

func TestAWSApplyWaitDeadlineExceededDuringPollPreservesExitCodeSix(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	client.instanceDescribeErrors["instance-custom"] = context.DeadlineExceeded
	request := awsApplyWaitRequest{
		Instance: awsrds.ScopePlan{
			Group:      "instance-custom",
			Parameters: []types.Parameter{{ParameterName: aws.String("max_connections"), ParameterValue: aws.String("200")}},
		},
	}

	_, err := defaultWaitForAWSApply(context.Background(), client, request, awsrds.Metadata{DBInstanceIdentifier: "mysql-1"})
	if exitCode := awsApplyExitCode(err); exitCode != awsApplyExitTimeout {
		t.Fatalf("deadline poll exit code = %d error %v, want timeout exit %d", exitCode, err, awsApplyExitTimeout)
	}
	result := newAWSApplyResult(awsApplyConfig("instance-custom", ""))
	recordAWSWaitFailure(&result, request, err)
	decoded := decodeAWSApplyResult(t, mustMarshalAWSApplyResult(t, &result))
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

	_, err := defaultWaitForAWSApply(ctx, client, request, awsrds.Metadata{
		DBInstanceIdentifier: "orders-1",
		DBClusterIdentifier:  "orders-cluster",
	})
	if exitCode := awsApplyExitCode(err); exitCode != awsApplyExitTimeout {
		t.Fatalf("shared deadline exit code = %d error %v, want timeout exit %d", exitCode, err, awsApplyExitTimeout)
	}
	result := newAWSApplyResult(awsApplyConfig("instance-custom", "cluster-custom"))
	recordAWSWaitFailure(&result, request, err)
	output := mustMarshalAWSApplyResult(t, &result)
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
		Instance: awsrds.ScopePlan{
			Group:      "instance-custom",
			Parameters: []types.Parameter{{ParameterName: aws.String("instance_value")}},
		},
		Cluster: awsrds.ScopePlan{
			Group:      "cluster-custom",
			Parameters: []types.Parameter{{ParameterName: aws.String("cluster_value")}},
		},
	}

	_, err := defaultWaitForAWSApply(context.Background(), client, request, awsrds.Metadata{
		DBInstanceIdentifier: "orders-1",
		DBClusterIdentifier:  "orders-cluster",
	})
	if exitCode := awsApplyExitCode(err); exitCode != awsApplyExitTimeout {
		t.Fatalf("deadline poll exit code = %d error %v, want timeout exit %d", exitCode, err, awsApplyExitTimeout)
	}
	result := newAWSApplyResult(awsApplyConfig("instance-custom", "cluster-custom"))
	recordAWSWaitFailure(&result, request, err)
	decoded := decodeAWSApplyResult(t, mustMarshalAWSApplyResult(t, &result))
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

	decoded := decodeAWSApplyResult(t, mustMarshalAWSApplyResult(t, &result))
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
	newAWSRDSClient = func(context.Context, *config.Config) (awsrds.Client, error) {
		return client, nil
	}
	waitForAWSApply = func(_ context.Context, _ awsrds.Client, request awsApplyWaitRequest, _ awsrds.Metadata) (awsApplyGroupStatuses, error) {
		if onWait != nil {
			onWait(request.modifiedScopes())
		}
		statuses := awsApplyGroupStatuses{}
		if request.modifiedScopes().Instance {
			statuses.Instance = "in-sync"
		}
		if request.modifiedScopes().Cluster {
			statuses.Cluster = "in-sync"
		}
		if fake, ok := client.(*awsApplyClientFake); ok {
			if fake.waitInstanceStatus != "" {
				statuses.Instance = fake.waitInstanceStatus
			}
			if fake.waitClusterStatus != "" {
				statuses.Cluster = fake.waitClusterStatus
			}
		}
		return statuses, nil
	}
	t.Cleanup(func() {
		newAWSRDSClient = originalFactory
		waitForAWSApply = originalWaiter
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
			DBInstanceIdentifier: aws.String("orders-1"),
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

func mustMarshalAWSApplyResult(t *testing.T, result *awsrds.ApplyResult) string {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(AWS apply result) error = %v", err)
	}
	return string(encoded)
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

	_, _, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"max_connections":"200"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"max_connections": "100"}}},
		logger,
		cfg,
		AWSApplyAll,
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
