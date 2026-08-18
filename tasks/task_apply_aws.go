package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	"github.com/aws/aws-sdk-go-v2/aws"
	configaws "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	logging "github.com/google/logger"
)

type AWSApplyMode int

const (
	AWSApplyAll AWSApplyMode = iota
	AWSApplyPendingRebootOnly
)

// These values preserve the public RDS task meanings used before routed
// parameter application: 3 identifies an attached parameter-group mismatch,
// 6 is a bounded-wait timeout, 8 is a general AWS apply failure, 9 identifies
// AccessDenied, and 10 reports that an applied change requires a reboot.
const (
	awsApplyExitSuccess                 = 0
	awsApplyExitInstanceUnavailable     = 1
	awsApplyExitParameterGroupNotInSync = 2
	awsApplyExitParameterGroupMismatch  = 3
	awsApplyExitTimeout                 = 6
	awsApplyExitFailure                 = 8
	awsApplyExitAccessDenied            = 9
	awsApplyExitPendingReboot           = 10

	awsApplyTaskStatusSuccess = 1
	awsApplyTaskStatusFailure = 4
	awsApplyBatchSize         = 20
)

const (
	awsApplyErrorTimeout                 = "timeout"
	awsApplyErrorAccessDenied            = "access-denied"
	awsApplyErrorAWSAPI                  = "aws-api-error"
	awsApplyErrorParameterGroupNotInSync = "parameter-group-not-in-sync"
	awsApplyErrorParameterGroupMismatch  = "parameter-group-mismatch"
)

var errAWSApplyWaitTimeout = errors.New("timed out waiting for AWS parameter apply")

type awsAPIError interface {
	error
	ErrorCode() string
}

const (
	// awsApplyWaitTimeout preserves the effective wall-clock budget of the
	// pre-routing waiter, which polled every 3s for up to 400 iterations
	// (~1200s) rather than 400s.
	awsApplyWaitTimeout  = 1200 * time.Second
	awsApplyPollInterval = 3 * time.Second
)

type awsApplyModifiedScopes struct {
	Instance bool `json:"instance"`
	Cluster  bool `json:"cluster"`
}

type awsApplyWaitRequest struct {
	Metadata awsrds.Metadata
	Instance awsrds.ScopePlan
	Cluster  awsrds.ScopePlan
}

func (request awsApplyWaitRequest) modifiedScopes() awsApplyModifiedScopes {
	return awsApplyModifiedScopes{
		Instance: len(request.Instance.Parameters) > 0,
		Cluster:  len(request.Cluster.Parameters) > 0,
	}
}

type awsRDSClientFactory func(context.Context, *config.Config) (awsrds.Client, error)

type awsApplyWaitFunc func(context.Context, awsrds.Client, awsApplyWaitRequest) error

type awsApplyReadbackFunc func(context.Context, awsrds.ParameterReader, awsrds.Scope, awsrds.ScopePlan) (map[string]awsrds.ParameterInfo, error)

var newAWSRDSClient awsRDSClientFactory = func(ctx context.Context, configuration *config.Config) (awsrds.Client, error) {
	cfg, err := configaws.LoadDefaultConfig(ctx, configaws.WithRegion(configuration.AwsRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return rds.NewFromConfig(cfg), nil
}

var waitForAWSApply awsApplyWaitFunc = defaultWaitForAWSApply

var readAWSAppliedParameters awsApplyReadbackFunc = defaultReadAWSAppliedParameters

var awsApplyReadbackTimeout = 30 * time.Second

// ApplyConfAwsRds obtains one live recommendation snapshot, refreshes AWS
// topology, routes every eligible value to its owning parameter-group API,
// and returns the deterministic per-scope result as task output.
func ApplyConfAwsRds(repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer,
	logger logging.Logger, configuration *config.Config, mode AWSApplyMode) (int, int, string) {
	return applyConfAWSRDS(repeaters, gatherers, logger, configuration, mode, awsApplyTaskContext{})
}

func applyConfAWSRDS(repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer,
	logger logging.Logger, configuration *config.Config, mode AWSApplyMode, task awsApplyTaskContext) (int, int, string) {
	result := newAWSApplyResult(configuration)
	finish := func(exitCode, status int) (int, int, string) {
		output := marshalAWSApplyResult(&result)
		logAWSApplyEvent(logger, "aws_rds_apply_audit", map[string]interface{}{
			"task_id":        task.TaskID,
			"task_type_id":   task.TaskTypeID,
			"task_status":    status,
			"task_exit_code": exitCode,
			"audit":          result.Audit,
		})
		return exitCode, status, output
	}

	if configuration == nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, fmt.Errorf("AWS RDS configuration is nil"))
		return finish(awsApplyExitFailure, awsApplyTaskStatusFailure)
	}

	// Recommendations and current values must come from the same single
	// collection. In particular, AWSApplyAll does not repeat this work for
	// immediate and pending-reboot parameters.
	metrics := utils.CollectMetrics(gatherers, logger, configuration)
	if metrics == nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, fmt.Errorf("collect current metrics: no metrics returned"))
		return finish(awsApplyExitFailure, awsApplyTaskStatusFailure)
	}
	recommendationJSON := utils.ProcessRepeaters(
		metrics,
		repeaters,
		configuration,
		logger,
		models.ModeType{Name: "Configurations", Type: "GetJson"},
	)
	recommendations, err := decodeAWSRecommendations(recommendationJSON)
	if err != nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return finish(awsApplyExitFailure, awsApplyTaskStatusFailure)
	}

	ctx := context.Background()
	client, err := newAWSRDSClient(ctx, configuration)
	if err != nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return finish(awsApplyExitCode(err), awsApplyTaskStatusFailure)
	}

	metadata, err := awsrds.DiscoverInstance(ctx, client, configuration.AwsRDSDB)
	if err != nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return finish(awsApplyExitCode(err), awsApplyTaskStatusFailure)
	}
	awsrds.PopulateAuditTopology(&result.Audit, metadata)
	if metadata.InstanceStatus != "available" {
		err = fmt.Errorf("DB instance %q status %q is not available", metadata.DBInstanceIdentifier, metadata.InstanceStatus)
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return finish(awsApplyExitInstanceUnavailable, awsApplyTaskStatusFailure)
	}
	groupValidation := validateAWSGroups(logger, metadata, configuration)
	if groupValidation.InstanceMismatch || groupValidation.ClusterMismatch {
		recordAWSGroupMismatchFailures(&result, groupValidation, metadata, configuration)
		return finish(awsApplyExitParameterGroupMismatch, awsApplyTaskStatusFailure)
	}
	if metadata.DBParameterGroupStatus != "in-sync" {
		err = fmt.Errorf(
			"DB instance %q parameter group %q status %q is not in-sync",
			metadata.DBInstanceIdentifier,
			metadata.DBParameterGroup,
			metadata.DBParameterGroupStatus,
		)
		logger.Error(err)
		recordAWSInstanceParameterGroupReadinessFailure(&result, metadata)
		return finish(awsApplyExitParameterGroupNotInSync, awsApplyTaskStatusFailure)
	}
	if metadata.IsAurora() && metadata.IsClusterWriter && metadata.DBClusterParameterGroupStatus != "in-sync" {
		err = fmt.Errorf(
			"DB cluster %q parameter group %q status %q is not in-sync for writer instance %q",
			metadata.DBClusterIdentifier,
			metadata.DBClusterParameterGroup,
			metadata.DBClusterParameterGroupStatus,
			metadata.DBInstanceIdentifier,
		)
		logger.Error(err)
		recordAWSClusterParameterGroupReadinessFailure(&result, metadata)
		return finish(awsApplyExitParameterGroupNotInSync, awsApplyTaskStatusFailure)
	}

	instanceParameters := map[string]awsrds.ParameterInfo{}
	instanceLookupGroup := configuration.AwsRDSParameterGroup
	instanceClassificationOnly := false
	instanceMembershipUnknown := false
	// A default or unconfigured group is never a mutation target. Read the
	// attached group only to classify recommendations.
	if awsrds.IsDefaultParameterGroup(configuration.AwsRDSParameterGroup) ||
		configuration.AwsRDSParameterGroup == "" {
		instanceLookupGroup = metadata.DBParameterGroup
		instanceClassificationOnly = true
		instanceMembershipUnknown = instanceLookupGroup == ""
	}
	if instanceLookupGroup != "" {
		instanceParameters, err = awsrds.ListParameters(
			ctx,
			client,
			instanceLookupGroup,
			awsrds.ScopeInstance,
		)
		if err != nil {
			if !instanceClassificationOnly {
				recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
				return finish(awsApplyExitCode(err), awsApplyTaskStatusFailure)
			}
			logger.Errorf("Optional instance parameter classification for group %q failed: %s", instanceLookupGroup, awsApplySafeErrorCode(err))
			instanceParameters = map[string]awsrds.ParameterInfo{}
			instanceMembershipUnknown = true
		}
	}

	clusterParameters := map[string]awsrds.ParameterInfo{}
	clusterLookup := awsrds.SelectClusterParameterGroupLookup(
		metadata,
		configuration.AwsRDSClusterParameterGroup,
	)
	switch {
	case awsrds.IsDefaultParameterGroup(configuration.AwsRDSClusterParameterGroup):
		// Reject default groups before ListParameters; they are never eligible
		// targets and their metadata is unnecessary for valid instance work.
		clusterLookup = awsrds.ClusterParameterGroupLookup{}
	}
	if clusterLookup.Group != "" && !metadata.IsClusterWriter {
		// A reader can never submit cluster-scope changes, so a transient
		// classification read must not fail the whole apply task.
		clusterLookup.ClassificationOnly = true
	}
	if clusterLookup.Group != "" {
		clusterParameters, err = awsrds.ListParameters(
			ctx,
			client,
			clusterLookup.Group,
			awsrds.ScopeCluster,
		)
		if err != nil {
			if !clusterLookup.ClassificationOnly {
				recordAWSApplyFailure(&result, awsrds.ScopeCluster, nil, err)
				return finish(awsApplyExitCode(err), awsApplyTaskStatusFailure)
			}
			logger.Errorf("Optional cluster parameter classification for group %q failed: %s", clusterLookup.Group, awsApplySafeErrorCode(err))
			clusterParameters = map[string]awsrds.ParameterInfo{}
		} else if clusterLookup.ClassificationOnly {
			logger.Infof("DB cluster parameter group %q loaded for recommendation classification only", clusterLookup.Group)
		}
	}
	plan, plannedResult := awsrds.BuildApplyPlan(awsrds.BuildApplyPlanInput{
		Metadata:                  metadata,
		ConfiguredInstanceGroup:   configuration.AwsRDSParameterGroup,
		ConfiguredClusterGroup:    configuration.AwsRDSClusterParameterGroup,
		InstanceMembershipUnknown: instanceMembershipUnknown,
		InstanceParameters:        instanceParameters,
		ClusterParameters:         clusterParameters,
		Recommendations:           recommendations,
		CurrentValues:             awsCurrentParameterValues(metrics.DB.Conf.Variables),
		PendingRebootOnly:         mode == AWSApplyPendingRebootOnly,
	})
	result = plannedResult

	waitRequest, applyErr := applyAWSPlan(ctx, client, plan, &result, logger, task)
	waitRequest.Metadata = metadata
	modified := waitRequest.modifiedScopes()
	if modified.Instance || modified.Cluster {
		waitErr := waitForAWSApply(ctx, client, waitRequest)
		logAWSApplyEvent(logger, "aws_rds_apply_wait", awsApplyWaitEventFields(modified, waitErr, task))
		if waitErr != nil {
			recordAWSWaitFailure(&result, waitRequest, waitErr)
			if applyErr == nil {
				applyErr = waitErr
			}
		}
		verifyAWSAppliedParameters(ctx, client, waitRequest, &result.Audit, logger)
	}
	if applyErr != nil {
		logger.Errorf("AWS parameter apply failed: %s", awsApplySafeErrorCode(applyErr))
		return finish(awsApplyExitCode(applyErr), awsApplyTaskStatusFailure)
	}
	if awsApplyRequiresReboot(waitRequest) {
		logger.Info("AWS parameter apply completed with pending reboot")
		return finish(awsApplyExitPendingReboot, awsApplyTaskStatusFailure)
	}

	return finish(awsApplyExitSuccess, awsApplyTaskStatusSuccess)
}

func decodeAWSRecommendations(raw string) (map[string]interface{}, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var recommendations map[string]interface{}
	if err := decoder.Decode(&recommendations); err != nil {
		return nil, fmt.Errorf("decode AWS RDS recommendations: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode AWS RDS recommendations: multiple JSON values")
		}
		return nil, fmt.Errorf("decode AWS RDS recommendations: trailing data: %w", err)
	}
	if recommendations == nil {
		recommendations = map[string]interface{}{}
	}
	return recommendations, nil
}

type awsGroupValidation struct {
	InstanceMismatch bool
	ClusterMismatch  bool
}

func validateAWSGroups(logger logging.Logger, metadata awsrds.Metadata, configuration *config.Config) awsGroupValidation {
	validation := awsGroupValidation{}
	configuredInstanceGroup := configuration.AwsRDSParameterGroup
	if configuredInstanceGroup == "" {
		validation.InstanceMismatch = true
		logger.Errorf("Configured DB parameter group is empty; attached group is %q", metadata.DBParameterGroup)
	} else if configuredInstanceGroup != metadata.DBParameterGroup {
		validation.InstanceMismatch = true
		logger.Errorf("Configured DB parameter group %q does not match attached group %q", configuredInstanceGroup, metadata.DBParameterGroup)
	}
	if configured := configuration.AwsRDSClusterParameterGroup; configured != "" && configured != metadata.DBClusterParameterGroup {
		validation.ClusterMismatch = true
		logger.Errorf("Configured DB cluster parameter group %q does not match attached group %q", configured, metadata.DBClusterParameterGroup)
	}
	return validation
}

func recordAWSGroupMismatchFailures(result *awsrds.ApplyResult, validation awsGroupValidation, metadata awsrds.Metadata, configuration *config.Config) {
	if result == nil || configuration == nil {
		return
	}
	if validation.InstanceMismatch {
		appendAWSGroupMismatchDiagnostic(&result.Instance, configuration.AwsRDSParameterGroup, metadata.DBParameterGroup)
		result.Instance.Failed = append(result.Instance.Failed, awsrds.FailedBatch{
			Parameters: []string{},
			Error:      awsApplyErrorParameterGroupMismatch,
		})
	}
	if validation.ClusterMismatch {
		appendAWSGroupMismatchDiagnostic(&result.Cluster, configuration.AwsRDSClusterParameterGroup, metadata.DBClusterParameterGroup)
		result.Cluster.Failed = append(result.Cluster.Failed, awsrds.FailedBatch{
			Parameters: []string{},
			Error:      awsApplyErrorParameterGroupMismatch,
		})
	}
}

func appendAWSGroupMismatchDiagnostic(result *awsrds.ScopeResult, expected, actual string) {
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Reason == awsrds.SkipGroupMismatch && diagnostic.ExpectedGroup == expected && diagnostic.ActualGroup == actual {
			return
		}
	}
	result.Diagnostics = append(result.Diagnostics, awsrds.ScopeDiagnostic{
		Reason:        awsrds.SkipGroupMismatch,
		ExpectedGroup: expected,
		ActualGroup:   actual,
	})
}

func awsCurrentParameterValues(values models.MetricGroupValue) map[string]interface{} {
	current := make(map[string]interface{}, len(values))
	for name, value := range values {
		switch nested := value.(type) {
		case models.MetricGroupValue:
			if setting, exists := nested["setting"]; exists {
				value = setting
			}
		case map[string]interface{}:
			if setting, exists := nested["setting"]; exists {
				value = setting
			}
		}
		current[name] = value
	}
	return current
}

func awsApplyRequiresReboot(request awsApplyWaitRequest) bool {
	for _, plan := range []awsrds.ScopePlan{request.Instance, request.Cluster} {
		for _, parameter := range plan.Parameters {
			if parameter.ApplyMethod == types.ApplyMethodPendingReboot {
				return true
			}
		}
	}
	return false
}

func applyAWSPlan(ctx context.Context, client awsrds.Client, plan awsrds.ApplyPlan, result *awsrds.ApplyResult, logger logging.Logger, task awsApplyTaskContext) (awsApplyWaitRequest, error) {
	waitRequest := awsApplyWaitRequest{
		Instance: awsrds.ScopePlan{Group: plan.Instance.Group, Parameters: []types.Parameter{}},
		Cluster:  awsrds.ScopePlan{Group: plan.Cluster.Group, Parameters: []types.Parameter{}},
	}
	logAWSApplyEvent(logger, "aws_rds_apply_plan", awsApplyPlanEventFields(plan, task))

	instanceApplied, err := applyAWSScopeBatches(ctx, client, awsrds.ScopeInstance, plan.Instance, &result.Instance, &result.Audit, logger, task)
	waitRequest.Instance.Parameters = instanceApplied
	if err != nil {
		markAWSAuditRemaining(&result.Audit, awsrds.ScopeCluster, plan.Cluster.Parameters, awsrds.ReasonPriorScopeFailure)
		return waitRequest, err
	}

	clusterApplied, err := applyAWSScopeBatches(ctx, client, awsrds.ScopeCluster, plan.Cluster, &result.Cluster, &result.Audit, logger, task)
	waitRequest.Cluster.Parameters = clusterApplied
	if err != nil {
		return waitRequest, err
	}

	return waitRequest, nil
}

func applyAWSScopeBatches(ctx context.Context, client awsrds.Client, scope awsrds.Scope, plan awsrds.ScopePlan, result *awsrds.ScopeResult, audit *awsrds.ApplyAudit, logger logging.Logger, task awsApplyTaskContext) ([]types.Parameter, error) {
	applied := []types.Parameter{}
	for start := 0; start < len(plan.Parameters); start += awsApplyBatchSize {
		end := start + awsApplyBatchSize
		if end > len(plan.Parameters) {
			end = len(plan.Parameters)
		}
		batch := append([]types.Parameter(nil), plan.Parameters[start:end]...)
		names := awsParameterNames(batch)

		var err error
		switch scope {
		case awsrds.ScopeInstance:
			_, err = client.ModifyDBParameterGroup(ctx, &rds.ModifyDBParameterGroupInput{
				DBParameterGroupName: aws.String(plan.Group),
				Parameters:           batch,
			})
		case awsrds.ScopeCluster:
			_, err = client.ModifyDBClusterParameterGroup(ctx, &rds.ModifyDBClusterParameterGroupInput{
				DBClusterParameterGroupName: aws.String(plan.Group),
				Parameters:                  batch,
			})
		default:
			err = fmt.Errorf("unsupported AWS parameter scope %q", scope)
		}

		batchNumber := start/awsApplyBatchSize + 1
		if err != nil {
			errorCode := awsApplySafeErrorCode(err)
			result.Failed = append(result.Failed, awsrds.FailedBatch{
				Parameters: names,
				Error:      errorCode,
			})
			markAWSAuditBatch(audit, scope, batch, batchNumber, awsrds.OutcomeFailed, "", errorCode)
			markAWSAuditRemaining(audit, scope, plan.Parameters[end:], awsrds.ReasonPriorBatchFailure)
			logAWSApplyEvent(logger, "aws_rds_apply_batch", awsApplyBatchEventFields(scope, plan.Group, batchNumber, names, awsrds.OutcomeFailed, errorCode, task))
			return applied, fmt.Errorf("modify %s parameter group %q for parameters %s: %w", scope, plan.Group, strings.Join(names, ","), err)
		}

		applied = append(applied, batch...)
		result.Applied = append(result.Applied, names...)
		markAWSAuditBatch(audit, scope, batch, batchNumber, awsrds.OutcomeApplied, "", "")
		logAWSApplyEvent(logger, "aws_rds_apply_batch", awsApplyBatchEventFields(scope, plan.Group, batchNumber, names, awsrds.OutcomeApplied, "", task))
	}
	return applied, nil
}

func awsParameterNames(parameters []types.Parameter) []string {
	names := make([]string, len(parameters))
	for index, parameter := range parameters {
		names[index] = aws.ToString(parameter.ParameterName)
	}
	return names
}

func newAWSApplyResult(configuration *config.Config) awsrds.ApplyResult {
	var instanceGroup, clusterGroup string
	if configuration != nil {
		instanceGroup = configuration.AwsRDSParameterGroup
		clusterGroup = configuration.AwsRDSClusterParameterGroup
	}
	return awsrds.ApplyResult{
		Instance: newAWSApplyScopeResult(instanceGroup),
		Cluster:  newAWSApplyScopeResult(clusterGroup),
		Audit:    awsrds.NewApplyAudit(awsrds.Metadata{}),
	}
}

func newAWSApplyScopeResult(group string) awsrds.ScopeResult {
	return awsrds.ScopeResult{
		Group:   group,
		Applied: []string{},
		Skipped: []awsrds.SkippedVariable{},
		Failed:  []awsrds.FailedBatch{},
	}
}

func recordAWSApplyFailure(result *awsrds.ApplyResult, scope awsrds.Scope, parameters []string, err error) {
	failure := awsrds.FailedBatch{
		Parameters: append([]string(nil), parameters...),
		Error:      awsApplySafeErrorCode(err),
	}
	if scope == awsrds.ScopeCluster {
		result.Cluster.Failed = append(result.Cluster.Failed, failure)
		return
	}
	result.Instance.Failed = append(result.Instance.Failed, failure)
}

func recordAWSInstanceParameterGroupReadinessFailure(result *awsrds.ApplyResult, metadata awsrds.Metadata) {
	result.Instance.Failed = append(result.Instance.Failed, awsrds.FailedBatch{
		Parameters:           []string{},
		Error:                awsApplyErrorParameterGroupNotInSync,
		DBInstanceIdentifier: &metadata.DBInstanceIdentifier,
		ParameterGroup:       &metadata.DBParameterGroup,
		ParameterGroupStatus: &metadata.DBParameterGroupStatus,
	})
}

func recordAWSClusterParameterGroupReadinessFailure(result *awsrds.ApplyResult, metadata awsrds.Metadata) {
	result.Cluster.Failed = append(result.Cluster.Failed, awsrds.FailedBatch{
		Parameters:           []string{},
		Error:                awsApplyErrorParameterGroupNotInSync,
		DBClusterIdentifier:  &metadata.DBClusterIdentifier,
		ParameterGroup:       &metadata.DBClusterParameterGroup,
		ParameterGroupStatus: &metadata.DBClusterParameterGroupStatus,
	})
}

type awsApplyWaitError struct {
	Scope      awsrds.Scope
	Unresolved awsApplyModifiedScopes
	Err        error
}

func (err *awsApplyWaitError) Error() string {
	if err.Unresolved.Instance && err.Unresolved.Cluster {
		return fmt.Sprintf("wait for instance and cluster parameter apply: %v", err.Err)
	}
	return fmt.Sprintf("wait for %s parameter apply: %v", err.Scope, err.Err)
}

func (err *awsApplyWaitError) Unwrap() error {
	return err.Err
}

func newAWSApplyTimeoutErrorForScopes(unresolved awsApplyModifiedScopes) error {
	scope := awsrds.ScopeInstance
	if !unresolved.Instance && unresolved.Cluster {
		scope = awsrds.ScopeCluster
	}
	return &awsApplyWaitError{Scope: scope, Unresolved: unresolved, Err: errAWSApplyWaitTimeout}
}

func newAWSApplyPollingError(scope awsrds.Scope, unresolved awsApplyModifiedScopes, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return newAWSApplyTimeoutErrorForScopes(unresolved)
	}
	return &awsApplyWaitError{Scope: scope, Unresolved: unresolved, Err: err}
}

func recordAWSWaitFailure(result *awsrds.ApplyResult, request awsApplyWaitRequest, err error) {
	scope := awsrds.ScopeInstance
	var scopedError *awsApplyWaitError
	if errors.As(err, &scopedError) {
		if scopedError.Unresolved.Instance || scopedError.Unresolved.Cluster {
			if scopedError.Unresolved.Instance {
				recordAWSApplyFailure(result, awsrds.ScopeInstance, awsParameterNames(request.Instance.Parameters), err)
			}
			if scopedError.Unresolved.Cluster {
				recordAWSApplyFailure(result, awsrds.ScopeCluster, awsParameterNames(request.Cluster.Parameters), err)
			}
			return
		}
		scope = scopedError.Scope
	} else if len(request.Instance.Parameters) == 0 {
		scope = awsrds.ScopeCluster
	}

	parameters := request.Instance.Parameters
	if scope == awsrds.ScopeCluster {
		parameters = request.Cluster.Parameters
	}
	recordAWSApplyFailure(result, scope, awsParameterNames(parameters), err)
}

func marshalAWSApplyResult(result *awsrds.ApplyResult) string {
	result.Sort()
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"instance":{"applied":[],"skipped":[],"failed":[{"parameters":[],"error":"serialize AWS apply result"}]},"cluster":{"applied":[],"skipped":[],"failed":[]}}`
	}
	return string(encoded)
}

func awsApplyExitCode(err error) int {
	if errors.Is(err, errAWSApplyWaitTimeout) {
		return awsApplyExitTimeout
	}
	if isAWSAccessDenied(err) {
		return awsApplyExitAccessDenied
	}
	return awsApplyExitFailure
}

func awsApplySafeErrorCode(err error) string {
	if errors.Is(err, errAWSApplyWaitTimeout) {
		return awsApplyErrorTimeout
	}
	if isAWSAccessDenied(err) {
		return awsApplyErrorAccessDenied
	}

	var apiError awsAPIError
	if errors.As(err, &apiError) {
		if code := apiError.ErrorCode(); isSafeAWSAPIErrorCode(code) {
			return "aws-api:" + code
		}
	}
	return awsApplyErrorAWSAPI
}

func isAWSAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	var apiError awsAPIError
	if errors.As(err, &apiError) && strings.Contains(strings.ToLower(apiError.ErrorCode()), "accessdenied") {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "accessdenied")
}

func isSafeAWSAPIErrorCode(code string) bool {
	if code == "" || len(code) > 128 {
		return false
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func defaultWaitForAWSApply(ctx context.Context, client awsrds.Client, request awsApplyWaitRequest) error {
	scopes := request.modifiedScopes()
	if !scopes.Instance && !scopes.Cluster {
		return nil
	}

	waitContext, cancel := context.WithTimeout(ctx, awsApplyWaitTimeout)
	defer cancel()

	instanceReady := !scopes.Instance
	clusterReady := !scopes.Cluster
	for {
		if !instanceReady {
			ready, err := awsApplyScopeReady(waitContext, client, request.Metadata, awsrds.ScopeInstance, request.Instance)
			if err != nil {
				return newAWSApplyPollingError(awsrds.ScopeInstance, awsApplyModifiedScopes{
					Instance: scopes.Instance && !instanceReady,
					Cluster:  scopes.Cluster && !clusterReady,
				}, err)
			}
			instanceReady = ready
		}
		if !clusterReady {
			ready, err := awsApplyScopeReady(waitContext, client, request.Metadata, awsrds.ScopeCluster, request.Cluster)
			if err != nil {
				return newAWSApplyPollingError(awsrds.ScopeCluster, awsApplyModifiedScopes{
					Instance: scopes.Instance && !instanceReady,
					Cluster:  scopes.Cluster && !clusterReady,
				}, err)
			}
			clusterReady = ready
		}
		if instanceReady && clusterReady {
			return nil
		}

		timer := time.NewTimer(awsApplyPollInterval)
		select {
		case <-waitContext.Done():
			timer.Stop()
			return newAWSApplyTimeoutErrorForScopes(awsApplyModifiedScopes{
				Instance: scopes.Instance && !instanceReady,
				Cluster:  scopes.Cluster && !clusterReady,
			})
		case <-timer.C:
		}
	}
}

func defaultReadAWSAppliedParameters(ctx context.Context, client awsrds.ParameterReader, scope awsrds.Scope, plan awsrds.ScopePlan) (map[string]awsrds.ParameterInfo, error) {
	return awsrds.ListParameters(ctx, client, plan.Group, scope)
}

func awsApplyScopeReady(ctx context.Context, client awsrds.Client, metadata awsrds.Metadata, scope awsrds.Scope, plan awsrds.ScopePlan) (bool, error) {
	observed, err := awsAppliedParametersObserved(ctx, client, scope, plan)
	if err != nil || !observed {
		return false, err
	}

	switch scope {
	case awsrds.ScopeInstance:
		output, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
			DBInstanceIdentifier: aws.String(metadata.DBInstanceIdentifier),
		})
		if err != nil {
			return false, fmt.Errorf("poll DB instance %q: %w", metadata.DBInstanceIdentifier, err)
		}
		if output == nil || len(output.DBInstances) != 1 {
			return false, fmt.Errorf("poll DB instance %q: expected one result", metadata.DBInstanceIdentifier)
		}
		instance := output.DBInstances[0]
		if aws.ToString(instance.DBInstanceStatus) != "available" {
			return false, nil
		}
		applyStatus := ""
		for _, group := range instance.DBParameterGroups {
			if aws.ToString(group.DBParameterGroupName) == plan.Group {
				applyStatus = aws.ToString(group.ParameterApplyStatus)
				break
			}
		}
		if applyStatus == "" {
			return false, fmt.Errorf("poll DB instance %q: no status for parameter group %q", metadata.DBInstanceIdentifier, plan.Group)
		}
		if applyStatus != "in-sync" && applyStatus != "pending-reboot" {
			return false, nil
		}
		return true, nil

	case awsrds.ScopeCluster:
		output, err := client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
			DBClusterIdentifier: aws.String(metadata.DBClusterIdentifier),
		})
		if err != nil {
			return false, fmt.Errorf("poll DB cluster %q: %w", metadata.DBClusterIdentifier, err)
		}
		if output == nil || len(output.DBClusters) != 1 {
			return false, fmt.Errorf("poll DB cluster %q: expected one result", metadata.DBClusterIdentifier)
		}
		if aws.ToString(output.DBClusters[0].Status) != "available" {
			return false, nil
		}
		return true, nil

	default:
		return false, fmt.Errorf("unsupported AWS parameter scope %q", scope)
	}
}

func awsAppliedParametersObserved(ctx context.Context, client awsrds.Client, scope awsrds.Scope, plan awsrds.ScopePlan) (bool, error) {
	listed, err := awsrds.ListParameters(ctx, client, plan.Group, scope)
	if err != nil {
		return false, fmt.Errorf("poll %s parameter group %q: %w", scope, plan.Group, err)
	}
	for _, parameter := range plan.Parameters {
		name := aws.ToString(parameter.ParameterName)
		actual, exists := listed[name]
		if !exists || !actual.HasParameterValue ||
			actual.ParameterValue != aws.ToString(parameter.ParameterValue) {
			return false, nil
		}
	}
	return true, nil
}
