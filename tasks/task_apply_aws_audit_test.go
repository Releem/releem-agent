package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	logging "github.com/google/logger"
)

func TestApplyAWSPlanUpdatesAuditAcrossPartialFailure(t *testing.T) {
	parameters := make([]types.Parameter, 0, 21)
	audit := awsrds.NewApplyAudit(awsrds.Metadata{})
	for index := 1; index <= 21; index++ {
		name := fmt.Sprintf("p%02d", index)
		parameters = append(parameters, types.Parameter{
			ParameterName:  aws.String(name),
			ParameterValue: aws.String("submitted-value-must-not-appear-in-events"),
		})
		audit.Parameters = append(audit.Parameters, awsrds.ParameterAudit{
			Scope:          awsrds.ScopeInstance,
			Name:           name,
			Group:          "instance-group",
			SubmittedValue: aws.String("submitted-value-must-not-appear-in-events"),
		})
	}
	audit.Parameters = append(audit.Parameters, awsrds.ParameterAudit{
		Scope:          awsrds.ScopeCluster,
		Name:           "cluster_p",
		Group:          "cluster-group",
		SubmittedValue: aws.String("submitted-value-must-not-appear-in-events"),
	})

	plan := awsrds.ApplyPlan{
		Instance: awsrds.ScopePlan{Group: "instance-group", Parameters: parameters},
		Cluster: awsrds.ScopePlan{Group: "cluster-group", Parameters: []types.Parameter{{
			ParameterName: aws.String("cluster_p"), ParameterValue: aws.String("cluster-submitted-value"),
		}}},
	}
	result := awsrds.ApplyResult{
		Instance: newAWSApplyScopeResult(plan.Instance.Group),
		Cluster:  newAWSApplyScopeResult(plan.Cluster.Group),
		Audit:    audit,
	}
	client := mysqlApplyClient(plan.Instance.Group)
	client.instanceModifyErrors = map[int]error{2: errors.New("AWS request failed")}

	_, err := applyAWSPlan(
		context.Background(), client, plan, &result, testAWSApplyLogger(),
		awsApplyTaskContext{TaskID: 41, TaskTypeID: 4},
	)
	if err == nil {
		t.Fatal("applyAWSPlan() error = nil, want second instance batch failure")
	}

	if got := auditRecord(t, &result.Audit, awsrds.ScopeInstance, "p01").Outcome; got != awsrds.OutcomeApplied {
		t.Fatalf("p01 = %q", got)
	}
	if got := auditRecord(t, &result.Audit, awsrds.ScopeInstance, "p21").Outcome; got != awsrds.OutcomeFailed {
		t.Fatalf("p21 = %q", got)
	}
	if got := auditRecord(t, &result.Audit, awsrds.ScopeCluster, "cluster_p").Reason; got != awsrds.ReasonPriorScopeFailure {
		t.Fatalf("cluster reason = %q", got)
	}
	if batch := auditRecord(t, &result.Audit, awsrds.ScopeInstance, "p21").Batch; batch == nil || *batch != 2 {
		t.Fatalf("batch = %#v", batch)
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
		Instance: newAWSApplyScopeResult(plan.Instance.Group),
		Cluster:  newAWSApplyScopeResult(plan.Cluster.Group),
		Audit: awsrds.ApplyAudit{Parameters: []awsrds.ParameterAudit{
			{Scope: awsrds.ScopeInstance, Name: "instance_p", Group: plan.Instance.Group, SubmittedValue: aws.String("submitted-secret")},
			{Scope: awsrds.ScopeCluster, Name: "cluster_p", Group: plan.Cluster.Group, SubmittedValue: aws.String("another-submitted-secret")},
		}},
	}

	if _, err := applyAWSPlan(
		context.Background(), mysqlApplyClient(plan.Instance.Group), plan, &result, logger,
		awsApplyTaskContext{TaskID: 77, TaskTypeID: 4},
	); err != nil {
		t.Fatalf("applyAWSPlan() error = %v", err)
	}

	events := decodeAWSApplyLogEvents(t, output.String())
	planEvent := awsApplyLogEvent(t, events, "aws_rds_apply_plan")
	if planEvent["task_id"] != float64(77) || planEvent["task_type_id"] != float64(4) {
		t.Fatalf("plan correlation = %#v, want task 77/type 4", planEvent)
	}
	if _, ok := planEvent["instance"].(map[string]interface{}); !ok {
		t.Fatalf("plan instance metadata = %#v, want group/names/count", planEvent["instance"])
	}
	if _, ok := planEvent["cluster"].(map[string]interface{}); !ok {
		t.Fatalf("plan cluster metadata = %#v, want group/names/count", planEvent["cluster"])
	}

	batchEvent := awsApplyLogEvent(t, events, "aws_rds_apply_batch")
	if batchEvent["task_id"] != float64(77) || batchEvent["task_type_id"] != float64(4) || batchEvent["scope"] != string(awsrds.ScopeInstance) || batchEvent["group"] != "instance-group" || batchEvent["batch"] != float64(1) || batchEvent["outcome"] != string(awsrds.OutcomeApplied) {
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

func auditRecord(t *testing.T, audit *awsrds.ApplyAudit, scope awsrds.Scope, name string) *awsrds.ParameterAudit {
	t.Helper()
	for index := range audit.Parameters {
		if audit.Parameters[index].Scope == scope && audit.Parameters[index].Name == name {
			return &audit.Parameters[index]
		}
	}
	t.Fatalf("audit record %s/%s not found", scope, name)
	return nil
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
