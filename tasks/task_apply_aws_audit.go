package tasks

import (
	"encoding/json"
	"errors"

	"github.com/Releem/mysqlconfigurer/awsrds"
	logging "github.com/google/logger"
)

// type awsApplyTaskContext struct {
// 	TaskID     int `json:"task_id,omitempty"`
// 	TaskTypeID int `json:"task_type_id,omitempty"`
// }

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

func awsApplyScopePlanEventFields(plan awsrds.ScopePlan) map[string]interface{} {
	names := awsParameterNames(plan.Parameters)
	return map[string]interface{}{
		"group": plan.Group,
		"names": names,
		"count": len(names),
	}
}

func awsApplyWaitScopeOutcomes(modified awsApplyModifiedScopes, err error) map[string]string {
	outcomes := map[string]string{}
	if modified.Instance {
		outcomes[string(awsrds.ScopeInstance)] = "success"
	}
	if modified.Cluster {
		outcomes[string(awsrds.ScopeCluster)] = "success"
	}
	if err == nil {
		return outcomes
	}

	var scoped *awsApplyWaitError
	if !errors.As(err, &scoped) {
		for scope := range outcomes {
			outcomes[scope] = "failed"
		}
		return outcomes
	}
	if modified.Instance && scoped.Unresolved.Instance {
		outcomes[string(awsrds.ScopeInstance)] = "unresolved"
	}
	if modified.Cluster && scoped.Unresolved.Cluster {
		outcomes[string(awsrds.ScopeCluster)] = "unresolved"
	}
	if _, modifiedScope := outcomes[string(scoped.Scope)]; modifiedScope {
		outcomes[string(scoped.Scope)] = "failed"
	}
	return outcomes
}
