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
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
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

func TestAWSApplyAuditLog(t *testing.T) {
	var output bytes.Buffer
	logger := *logging.Init("task-apply-aws-audit-test", false, false, &output)
	client := mysqlApplyClient("instance-custom")
	client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
		Parameters: []types.Parameter{modifiableAWSParameter("max_connections", "dynamic")},
	}}
	installAWSApplyTestDependencies(t, client, nil)

	exitCode, status, taskOutput := applyConfAWSRDS(
		&awsApplyRepeater{recommendations: `{"max_connections":"200"}`},
		[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{"max_connections": "100"}}},
		logger,
		awsApplyConfig("instance-custom", ""),
		AWSApplyAll,
		awsApplyTaskContext{TaskID: 42, TaskTypeID: 4},
	)
	if exitCode != awsApplyExitSuccess || status != awsApplyTaskStatusSuccess {
		t.Fatalf("applyConfAWSRDS() = exit %d status %d output %s", exitCode, status, taskOutput)
	}

	events := decodeAWSApplyLogEvents(t, output.String())
	terminalEvents := awsApplyLogEvents(events, "aws_rds_apply_audit")
	if len(terminalEvents) != 1 {
		t.Fatalf("terminal audit events = %d, want exactly one; events=%#v", len(terminalEvents), events)
	}
	event := terminalEvents[0]
	if event["task_id"] != float64(42) || event["task_type_id"] != float64(4) || event["task_status"] != float64(awsApplyTaskStatusSuccess) || event["task_exit_code"] != float64(awsApplyExitSuccess) {
		t.Fatalf("terminal audit correlation/status = %#v", event)
	}
	audit, ok := event["audit"].(map[string]interface{})
	if !ok || audit["schema_version"] != float64(awsrds.ApplyAuditSchemaVersion) {
		t.Fatalf("terminal audit = %#v, want schema version %d", event["audit"], awsrds.ApplyAuditSchemaVersion)
	}
	if len(awsApplyLogEvents(events, "aws_rds_apply_wait")) != 1 {
		t.Fatalf("wait events = %#v, want exactly one after modified scope", events)
	}
}

func TestVerifyAWSAppliedParameters(t *testing.T) {
	tests := []struct {
		name         string
		applyMethod  types.ApplyMethod
		readback     map[string]awsrds.ParameterInfo
		readbackErr  error
		wantObserved *string
		wantStatus   awsrds.VerificationStatus
		wantReason   string
	}{
		{"immediate match", types.ApplyMethodImmediate, parameterMap("p", "200"), nil, aws.String("200"), awsrds.VerificationMatched, ""},
		{"mismatch", types.ApplyMethodImmediate, parameterMap("p", "199"), nil, aws.String("199"), awsrds.VerificationMismatched, ""},
		{"static match", types.ApplyMethodPendingReboot, parameterMap("p", "200"), nil, aws.String("200"), awsrds.VerificationPendingReboot, ""},
		{"API failure", types.ApplyMethodImmediate, nil, errors.New("unavailable"), nil, awsrds.VerificationUnavailable, awsrds.ReasonReadbackFailed},
		{"missing parameter", types.ApplyMethodImmediate, map[string]awsrds.ParameterInfo{}, nil, nil, awsrds.VerificationUnavailable, awsrds.ReasonReadbackParameterMissing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalReadback := readAWSAppliedParameters
			readbackCalls := 0
			readAWSAppliedParameters = func(_ context.Context, _ awsrds.ParameterReader, scope awsrds.Scope, plan awsrds.ScopePlan) (map[string]awsrds.ParameterInfo, error) {
				readbackCalls++
				if scope != awsrds.ScopeInstance || plan.Group != "instance-group" {
					t.Fatalf("readback scope/group = %q/%q, want instance/instance-group", scope, plan.Group)
				}
				return tt.readback, tt.readbackErr
			}
			t.Cleanup(func() {
				readAWSAppliedParameters = originalReadback
			})

			request := awsApplyWaitRequest{
				Instance: awsrds.ScopePlan{
					Group: "instance-group",
					Parameters: []types.Parameter{{
						ParameterName:  aws.String("p"),
						ParameterValue: aws.String("200"),
						ApplyMethod:    tt.applyMethod,
					}},
				},
			}
			audit := awsrds.ApplyAudit{Parameters: []awsrds.ParameterAudit{{
				Scope:              awsrds.ScopeInstance,
				Name:               "p",
				Group:              "instance-group",
				SubmittedValue:     aws.String("200"),
				ApplyMethod:        string(tt.applyMethod),
				Outcome:            awsrds.OutcomeApplied,
				VerificationStatus: awsrds.VerificationNotApplicable,
			}}}

			verifyAWSAppliedParameters(
				context.Background(), mysqlApplyClient("instance-group"), request,
				&audit, testAWSApplyLogger(),
			)

			record := auditRecord(t, &audit, awsrds.ScopeInstance, "p")
			if !reflect.DeepEqual(record.ObservedAfter, tt.wantObserved) || record.VerificationStatus != tt.wantStatus || record.Reason != tt.wantReason {
				t.Fatalf("verification = observed %#v status %q reason %q, want %#v/%q/%q", record.ObservedAfter, record.VerificationStatus, record.Reason, tt.wantObserved, tt.wantStatus, tt.wantReason)
			}
			if readbackCalls != 1 {
				t.Fatalf("readback calls = %d, want one for the modified scope", readbackCalls)
			}
		})
	}
}

func parameterMap(name, value string) map[string]awsrds.ParameterInfo {
	return map[string]awsrds.ParameterInfo{
		name: {Name: name, ParameterValue: value, HasParameterValue: true},
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

func awsApplyLogEvents(events []map[string]interface{}, name string) []map[string]interface{} {
	matching := []map[string]interface{}{}
	for _, event := range events {
		if event["event"] == name {
			matching = append(matching, event)
		}
	}
	return matching
}
