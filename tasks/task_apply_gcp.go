package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
	"google.golang.org/api/sqladmin/v1"
)

func ApplyConfGcpCloudSQL(repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer,
	logger logging.Logger, configuration *config.Config, applyMode string) (int, int, string) {

	var task_exit_code, task_status int = 0, 1
	var task_output string

	metrics := utils.CollectMetrics(gatherers, logger, configuration)

	// Initialize GCP clients with Application Default Credentials
	ctx := context.Background()

	// Create SQL Admin client
	sqlAdminService, err := sqladmin.NewService(ctx)
	if err != nil {
		logger.Error("Failed to create GCP SQL Admin client", err)
		task_output = task_output + "Failed to create GCP SQL Admin client" + err.Error()
		task_status = 4
		task_exit_code = 1
		return task_exit_code, task_status, task_output
	}
	logger.Info("GSP configuration loaded SUCCESS")
	task_output = task_output + "GSP configuration loaded SUCCESS\n"
	// Get instance details
	instance, err := sqlAdminService.Instances.Get(configuration.GcpProjectId, configuration.GcpCloudSqlInstance).Do()
	if err != nil {
		logger.Error("Failed to get Cloud SQL instance details", err)
		task_output = task_output + "Failed to get Cloud SQL instance details" + err.Error()
		task_status = 4
		task_exit_code = 1
		return task_exit_code, task_status, task_output
	}

	logger.Info("GCP Cloud SQL instance found: ", instance.Name)
	task_output = task_output + "GCP Cloud SQL instance found: " + instance.Name + "\n"

	// Get current database flags from the instance
	currentFlags := make(map[string]string)
	if instance.Settings != nil && instance.Settings.DatabaseFlags != nil {
		for _, flag := range instance.Settings.DatabaseFlags {
			currentFlags[flag.Name] = flag.Value
		}
		logger.Infof("Current database flags count: %d", len(currentFlags))
	}

	recommendedVars := models.MetricGroupValue{}
	recommend_var := utils.ProcessRepeaters(metrics, repeaters, configuration, logger, models.ModeType{Name: "Configurations", Type: "GetJson", ApplyMode: applyMode})
	err = json.Unmarshal([]byte(recommend_var), &recommendedVars)
	if err != nil {
		logger.Error(err)
		task_output = task_output + err.Error()
	}
	if applyMode == "dynamic" {
		flagsResponse, flagsErr := sqlAdminService.Flags.List().DatabaseVersion(instance.DatabaseVersion).Context(ctx).Do()
		if flagsErr != nil {
			logger.Error("Failed to load GCP Cloud SQL flag metadata", flagsErr)
			task_output += "Failed to load GCP Cloud SQL flag metadata: " + flagsErr.Error()
			return 8, 4, task_output
		}
		var unknownFlags []string
		recommendedVars, unknownFlags = filterGCPRecommendationsByApplyMode(recommendedVars, flagsResponse.Items, applyMode)
		if len(unknownFlags) > 0 {
			message := "GCP Cloud SQL flag metadata is missing recommended flags: " + strings.Join(unknownFlags, ", ")
			logger.Error(message)
			task_output += message
			return 8, 4, task_output
		}
	}

	// Merge current flags with recommended changes
	// mergeGcpDatabaseFlags merges current database flags with recommended changes
	// preserving existing flags that are not being modified
	var mergedFlags []*sqladmin.DatabaseFlags
	processedFlags := make(map[string]bool)

	// First, add all recommended changes
	for key := range recommendedVars {
		if recommendedVars[key] != metrics.DB.Conf.Variables[key] {
			logger.Infof("Updating flag %s: current=%v, recommended=%v, db_current=%v",
				key, currentFlags[key], recommendedVars[key], metrics.DB.Conf.Variables[key])
			task_output = task_output + fmt.Sprintf("Updating flag %s: current=%v, recommended=%v, db_current=%v\n",
				key, currentFlags[key], recommendedVars[key], metrics.DB.Conf.Variables[key])
			value := recommendedVars[key].(string)
			mergedFlags = append(mergedFlags, &sqladmin.DatabaseFlags{
				Name:  key,
				Value: value,
			})
			processedFlags[key] = true
		}
	}

	// Then, preserve all existing flags that weren't modified
	for flagName, flagValue := range currentFlags {
		if !processedFlags[flagName] {
			logger.Infof("Preserving existing flag %s=%s", flagName, flagValue)
			task_output = task_output + fmt.Sprintf("Preserving existing flag %s=%s\n", flagName, flagValue)
			mergedFlags = append(mergedFlags, &sqladmin.DatabaseFlags{
				Name:  flagName,
				Value: flagValue,
			})
		}
	}

	logger.Infof("Total flags to apply: %d (recommended changes: %d, preserved: %d)",
		len(mergedFlags), len(processedFlags), len(mergedFlags)-len(processedFlags))
	task_output = task_output + fmt.Sprintf("Total flags to apply: %d (recommended changes: %d, preserved: %d)\n",
		len(mergedFlags), len(processedFlags), len(mergedFlags)-len(processedFlags))

	req := &sqladmin.DatabaseInstance{
		Settings: &sqladmin.Settings{
			DatabaseFlags: mergedFlags,
		},
	}
	// A partial update (Patch) is safer than a full update (Update) because we only change the specified fields.
	op, err := sqlAdminService.Instances.Patch(configuration.GcpProjectId, configuration.GcpCloudSqlInstance, req).Context(ctx).Do()
	if err != nil {
		if strings.Contains(err.Error(), "notAuthorized") {
			task_exit_code = 9
			task_status = 4
		} else {
			task_exit_code = 8
			task_status = 4
		}
		logger.Errorf("Instances.Patch: %v", err)
		task_output = task_output + err.Error()
		return task_exit_code, task_status, task_output
	} else {
		logger.Info("Instances.Patch modified successfully")
		task_output = task_output + "Instances.Patch modified successfully.\n"
	}

	if err := waitForOp(ctx, sqlAdminService, configuration.GcpProjectId, op); err != nil {
		logger.Fatalf("waitForOp: %v", err)
		task_exit_code = 8
		task_status = 4
		task_output = task_output + err.Error()
	} else {
		task_output = task_output + "Cloud SQL instance updated successfully.\n"
		logger.Info("Cloud SQL instance updated successfully.")
	}

	return task_exit_code, task_status, task_output

}

func filterGCPRecommendationsByApplyMode(recommendations models.MetricGroupValue, flags []*sqladmin.Flag, applyMode string) (models.MetricGroupValue, []string) {
	if applyMode != "dynamic" {
		return recommendations, nil
	}
	knownFlags := make(map[string]bool, len(flags))
	for _, flag := range flags {
		if flag != nil && flag.Name != "" {
			knownFlags[flag.Name] = !flag.RequiresRestart
		}
	}
	filtered := make(models.MetricGroupValue)
	unknown := make([]string, 0)
	for name, value := range recommendations {
		dynamic, ok := knownFlags[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		if dynamic {
			filtered[name] = value
		}
	}
	sort.Strings(unknown)
	return filtered, unknown
}

// Waiting for long Cloud SQL operations to complete
func waitForOp(ctx context.Context, sqlAdminService *sqladmin.Service, project string, op *sqladmin.Operation) error {
	opName := op.Name
	for {
		cur, err := sqlAdminService.Operations.Get(project, opName).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("operations.get: %w", err)
		}
		if cur.Status == "DONE" {
			if cur.Error != nil && len(cur.Error.Errors) > 0 {
				return fmt.Errorf("cloudsql op error: %v", cur.Error.Errors[0].Message)
			}
			return nil
		}
		time.Sleep(3 * time.Second)
	}
}
