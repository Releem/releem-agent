package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

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
			var skipped []awsrds.SkippedVariable
			if tt.wantScope == awsrds.ScopeInstance {
				skipped = result.Instance.Skipped
			} else {
				skipped = result.Cluster.Skipped
			}
			if len(skipped) != 1 || skipped[0].Reason != awsrds.SkipGroupMismatch || skipped[0].ExpectedGroup != tt.wantExpected || skipped[0].ActualGroup != tt.wantActual {
				t.Fatalf("mismatch result = %#v, want expected %q actual %q", skipped, tt.wantExpected, tt.wantActual)
			}
		})
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
	if !reflect.DeepEqual(failure.Parameters, []string{"parameter_20"}) || failure.Error != wantErr.Error() {
		t.Fatalf("failed batch = %#v, want parameter_20 and %q", failure, wantErr)
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
	newAWSRDSClient = func(context.Context, *config.Config) (awsrds.Client, error) {
		return client, nil
	}
	waitForAWSApply = func(_ context.Context, _ awsrds.Client, request awsApplyWaitRequest) error {
		if onWait != nil {
			onWait(request.modifiedScopes())
		}
		return nil
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
				DBInstanceIdentifier: aws.String("orders-1"),
				IsClusterWriter:      aws.Bool(writer),
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

func TestAWSApplyFailureOutputDoesNotContainConfigSecrets(t *testing.T) {
	client := mysqlApplyClient("instance-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("max_connections", "dynamic")},
	}}
	client.instanceModifyErrors = map[int]error{1: errors.New("request failed")}
	installAWSApplyTestDependencies(t, client, nil)
	cfg := awsApplyConfig("instance-custom", "")
	cfg.ApiKey = "super-secret-api-key"
	cfg.MysqlPassword = "super-secret-db-password"

	_, _, output := ApplyConfAwsRds(
		&awsApplyRepeater{recommendations: `{"max_connections":"200"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"max_connections": "100"}}},
		testAWSApplyLogger(),
		cfg,
		AWSApplyAll,
	)

	if strings.Contains(output, cfg.ApiKey) || strings.Contains(output, cfg.MysqlPassword) {
		t.Fatalf("task output leaked configuration secrets: %s", output)
	}
}
