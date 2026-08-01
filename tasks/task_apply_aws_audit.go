package tasks

import (
	"encoding/json"

	"github.com/Releem/mysqlconfigurer/awsrds"
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
