package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	logging "github.com/google/logger"
)

func TestApplyAWSPlanRecordsPartialFailure(t *testing.T) {
	parameters := make([]types.Parameter, 0, 21)
	for index := 1; index <= 21; index++ {
		parameters = append(parameters, types.Parameter{
			ParameterName:  aws.String(fmt.Sprintf("p%02d", index)),
			ParameterValue: aws.String("submitted-value-must-not-appear-in-events"),
		})
	}
	plan := awsrds.ApplyPlan{
		Instance: awsrds.ScopePlan{Group: "instance-group", Parameters: parameters},
		Cluster: awsrds.ScopePlan{Group: "cluster-group", Parameters: []types.Parameter{{
			ParameterName: aws.String("cluster_p"), ParameterValue: aws.String("cluster-submitted-value"),
		}}},
	}
	result := awsrds.ApplyResult{
		Instance: awsrds.NewScopeResult(plan.Instance.Group),
		Cluster:  awsrds.NewScopeResult(plan.Cluster.Group),
	}
	client := mysqlApplyClient(plan.Instance.Group)
	client.instanceModifyErrors = map[int]error{2: errors.New("AWS request failed")}

	waitRequest, err := applyAWSPlan(context.Background(), client, plan, &result, testAWSApplyLogger())
	if err == nil {
		t.Fatal("applyAWSPlan() error = nil, want second instance batch failure")
	}
	if got, want := result.Instance.Applied, awsParameterNames(parameters[:20]); !reflect.DeepEqual(got, want) {
		t.Fatalf("applied parameters = %#v, want %#v", got, want)
	}
	if len(result.Instance.Failed) != 1 || !reflect.DeepEqual(result.Instance.Failed[0].Parameters, []string{"p21"}) {
		t.Fatalf("instance failures = %#v, want p21 failure", result.Instance.Failed)
	}
	if len(result.Cluster.Applied) != 0 || len(result.Cluster.Failed) != 0 || len(client.clusterModifyCalls) != 0 {
		t.Fatalf("cluster scope was attempted after instance failure: result=%#v calls=%#v", result.Cluster, client.clusterModifyCalls)
	}
	if got, want := awsParameterNames(waitRequest.Instance.Parameters), awsParameterNames(parameters[:20]); !reflect.DeepEqual(got, want) {
		t.Fatalf("wait request parameters = %#v, want %#v", got, want)
	}
}

func TestAWSApplyPlanAndBatchLogsOmitValues(t *testing.T) {
	var output bytes.Buffer
	logger := *logging.Init("task-apply-aws-audit-test", false, false, &output)
	plan := awsrds.ApplyPlan{
		Instance: awsrds.ScopePlan{Group: "instance-group", Parameters: []types.Parameter{{
			ParameterName: aws.String("instance_p"), ParameterValue: aws.String("submitted-secret"),
		}}},
		Cluster: awsrds.ScopePlan{Group: "cluster-group", Parameters: []types.Parameter{{
			ParameterName: aws.String("cluster_p"), ParameterValue: aws.String("another-submitted-secret"),
		}}},
	}
	result := awsrds.ApplyResult{
		Instance: awsrds.NewScopeResult(plan.Instance.Group),
		Cluster:  awsrds.NewScopeResult(plan.Cluster.Group),
	}

	if _, err := applyAWSPlan(context.Background(), mysqlApplyClient(plan.Instance.Group), plan, &result, logger); err != nil {
		t.Fatalf("applyAWSPlan() error = %v", err)
	}

	events := decodeAWSApplyLogEvents(t, output.String())
	planEvent := awsApplyLogEvent(t, events, "aws_rds_apply_plan")
	if _, ok := planEvent["instance"].(map[string]interface{}); !ok {
		t.Fatalf("plan instance metadata = %#v, want group/names/count", planEvent["instance"])
	}
	if _, ok := planEvent["cluster"].(map[string]interface{}); !ok {
		t.Fatalf("plan cluster metadata = %#v, want group/names/count", planEvent["cluster"])
	}

	batchEvent := awsApplyLogEvent(t, events, "aws_rds_apply_batch")
	if batchEvent["scope"] != string(awsrds.ScopeInstance) || batchEvent["group"] != "instance-group" || batchEvent["batch"] != float64(1) || batchEvent["outcome"] != string(awsrds.OutcomeApplied) {
		t.Fatalf("instance batch event = %#v", batchEvent)
	}
	names, ok := batchEvent["names"].([]interface{})
	if !ok || len(names) != 1 || names[0] != "instance_p" {
		t.Fatalf("instance batch names = %#v", batchEvent["names"])
	}
	if strings.Contains(output.String(), "submitted-secret") || strings.Contains(output.String(), "another-submitted-secret") {
		t.Fatalf("structured AWS apply events leaked submitted values: %q", output.String())
	}
}

func TestAWSApplyPollingErrorPreservesUnresolvedScopes(t *testing.T) {
	want := awsApplyModifiedScopes{Instance: true, Cluster: true}
	err := newAWSApplyPollingError(awsrds.ScopeInstance, want, errors.New("poll failed"))

	var scoped *awsApplyWaitError
	if !errors.As(err, &scoped) {
		t.Fatalf("polling error = %T, want *awsApplyWaitError", err)
	}
	if scoped.Unresolved != want {
		t.Fatalf("unresolved scopes = %#v, want %#v", scoped.Unresolved, want)
	}
}

func TestAWSApplyWaitScopeOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		failed     awsrds.Scope
		unresolved awsApplyModifiedScopes
		want       map[string]string
	}{
		{
			name:       "instance completed before cluster failure",
			failed:     awsrds.ScopeCluster,
			unresolved: awsApplyModifiedScopes{Cluster: true},
			want:       map[string]string{"instance": "success", "cluster": "failed"},
		},
		{
			name:       "cluster completed before instance failure",
			failed:     awsrds.ScopeInstance,
			unresolved: awsApplyModifiedScopes{Instance: true},
			want:       map[string]string{"instance": "failed", "cluster": "success"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newAWSApplyPollingError(tt.failed, tt.unresolved, errors.New("poll failed"))
			got := awsApplyWaitScopeOutcomes(awsApplyModifiedScopes{Instance: true, Cluster: true}, err)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("scope outcomes = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func decodeAWSApplyLogEvents(t *testing.T, output string) []map[string]interface{} {
	t.Helper()
	events := []map[string]interface{}{}
	for _, line := range strings.Split(output, "\n") {
		start := strings.Index(line, "{")
		if start == -1 {
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line[start:]), &event); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func awsApplyLogEvent(t *testing.T, events []map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	for _, event := range events {
		if event["event"] == name {
			return event
		}
	}
	t.Fatalf("event %q not found in %#v", name, events)
	return nil
}

func awsApplyLogEvents(events []map[string]interface{}, name string) []map[string]interface{} {
	matching := []map[string]interface{}{}
	for _, event := range events {
		if event["event"] == name {
			matching = append(matching, event)
		}
	}
	return matching
}
