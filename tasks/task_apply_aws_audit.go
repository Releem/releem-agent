package tasks

import (
	"context"
	"encoding/json"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	logging "github.com/google/logger"
)

type awsApplyTaskContext struct {
	TaskID     int `json:"task_id,omitempty"`
	TaskTypeID int `json:"task_type_id,omitempty"`
}

func logAWSApplyEvent(logger logging.Logger, event string, fields map[string]interface{}) {
	payload := map[string]interface{}{"event": event}
	for name, value := range fields {
		payload[name] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		logger.Warningf("AWS apply log event %q could not be serialized: %v", event, err)
		return
	}
	logger.Info(string(encoded))
}

func awsApplyPlanEventFields(plan awsrds.ApplyPlan, task awsApplyTaskContext) map[string]interface{} {
	return map[string]interface{}{
		"task_id":      task.TaskID,
		"task_type_id": task.TaskTypeID,
		"instance":     awsApplyScopePlanEventFields(plan.Instance),
		"cluster":      awsApplyScopePlanEventFields(plan.Cluster),
	}
}

func awsApplyScopePlanEventFields(plan awsrds.ScopePlan) map[string]interface{} {
	names := awsParameterNames(plan.Parameters)
	return map[string]interface{}{
		"group": plan.Group,
		"names": names,
		"count": len(names),
	}
}

func awsApplyBatchEventFields(scope awsrds.Scope, group string, batch int, names []string, outcome awsrds.ApplyOutcome, errorCode string, task awsApplyTaskContext) map[string]interface{} {
	fields := map[string]interface{}{
		"task_id":      task.TaskID,
		"task_type_id": task.TaskTypeID,
		"scope":        string(scope),
		"group":        group,
		"batch":        batch,
		"names":        names,
		"outcome":      string(outcome),
	}
	if errorCode != "" {
		fields["error"] = errorCode
	}
	return fields
}

func awsApplyWaitEventFields(modified awsApplyModifiedScopes, err error, task awsApplyTaskContext) map[string]interface{} {
	fields := map[string]interface{}{
		"task_id":         task.TaskID,
		"task_type_id":    task.TaskTypeID,
		"modified_scopes": modified,
		"outcome":         "success",
	}
	if err != nil {
		fields["outcome"] = "failed"
		fields["error"] = awsApplySafeErrorCode(err)
	}
	return fields
}

func markAWSAuditBatch(audit *awsrds.ApplyAudit, scope awsrds.Scope, parameters []types.Parameter, batch int, outcome awsrds.ApplyOutcome, reason, errorCode string) {
	if audit == nil {
		return
	}
	for _, name := range awsParameterNames(parameters) {
		for index := range audit.Parameters {
			record := &audit.Parameters[index]
			if record.Scope != scope || record.Name != name {
				continue
			}
			batchCopy := batch
			record.Batch = &batchCopy
			record.Outcome = outcome
			record.Reason = reason
			record.Error = errorCode
			break
		}
	}
}

func markAWSAuditRemaining(audit *awsrds.ApplyAudit, scope awsrds.Scope, parameters []types.Parameter, reason string) {
	if audit == nil {
		return
	}
	for _, name := range awsParameterNames(parameters) {
		for index := range audit.Parameters {
			record := &audit.Parameters[index]
			if record.Scope != scope || record.Name != name {
				continue
			}
			record.Outcome = awsrds.OutcomeNotAttempted
			record.Reason = reason
			record.Error = ""
			break
		}
	}
}

func verifyAWSAppliedParameters(ctx context.Context, client awsrds.ParameterReader, request awsApplyWaitRequest, audit *awsrds.ApplyAudit, logger logging.Logger) {
	if audit == nil {
		return
	}
	verifyAWSAppliedScope(ctx, client, awsrds.ScopeInstance, request.Instance, audit, logger)
	verifyAWSAppliedScope(ctx, client, awsrds.ScopeCluster, request.Cluster, audit, logger)
}

func verifyAWSAppliedScope(ctx context.Context, client awsrds.ParameterReader, scope awsrds.Scope, plan awsrds.ScopePlan, audit *awsrds.ApplyAudit, logger logging.Logger) {
	if len(plan.Parameters) == 0 {
		return
	}

	readback, err := readAWSAppliedParameters(ctx, client, scope, plan)
	if err != nil {
		for _, parameter := range plan.Parameters {
			markAWSAuditReadbackUnavailable(audit, scope, aws.ToString(parameter.ParameterName), awsrds.ReasonReadbackFailed)
		}
		logger.Warningf("AWS parameter readback unavailable for %s parameter group %q: %s", scope, plan.Group, awsApplySafeErrorCode(err))
		return
	}

	for _, parameter := range plan.Parameters {
		name := aws.ToString(parameter.ParameterName)
		record := findAWSAuditRecord(audit, scope, name)
		if record == nil || record.Outcome != awsrds.OutcomeApplied {
			continue
		}

		observed, exists := readback[name]
		if !exists || !observed.HasParameterValue {
			markAWSAuditReadbackUnavailable(audit, scope, name, awsrds.ReasonReadbackParameterMissing)
			logger.Warningf("AWS parameter readback missing for %s parameter group %q parameter %q", scope, plan.Group, name)
			continue
		}

		record.ObservedAfter = aws.String(observed.ParameterValue)
		record.Reason = ""
		if observed.ParameterValue != aws.ToString(parameter.ParameterValue) {
			record.VerificationStatus = awsrds.VerificationMismatched
			logger.Warningf("AWS parameter readback mismatch for %s parameter group %q parameter %q", scope, plan.Group, name)
			continue
		}
		if parameter.ApplyMethod == types.ApplyMethodPendingReboot {
			record.VerificationStatus = awsrds.VerificationPendingReboot
			continue
		}
		record.VerificationStatus = awsrds.VerificationMatched
	}
}

func markAWSAuditReadbackUnavailable(audit *awsrds.ApplyAudit, scope awsrds.Scope, name, reason string) {
	record := findAWSAuditRecord(audit, scope, name)
	if record == nil || record.Outcome != awsrds.OutcomeApplied {
		return
	}
	record.ObservedAfter = nil
	record.VerificationStatus = awsrds.VerificationUnavailable
	record.Reason = reason
}

func findAWSAuditRecord(audit *awsrds.ApplyAudit, scope awsrds.Scope, name string) *awsrds.ParameterAudit {
	if audit == nil {
		return nil
	}
	for index := range audit.Parameters {
		record := &audit.Parameters[index]
		if record.Scope == scope && record.Name == name {
			return record
		}
	}
	return nil
}
