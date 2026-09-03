package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mysql/armmysqlflexibleservers"
	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

type azureMySQLConfigurationMetadata struct {
	name           string
	value          string
	dynamic        bool
	readOnly       bool
	pendingRestart bool
}

func ApplyConfAzureMySQL(repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer,
	logger logging.Logger, configuration *config.Config, restart bool) (int, int, string) {

	task_exit_code, task_status := 0, 1
	var task_output string

	if configuration.AzureSubscriptionID == "" || configuration.AzureResourceGroup == "" || configuration.AzureMySQLServer == "" {
		task_output = task_output + "azure_subscription_id, azure_resource_group and azure_mysql_server must be set\n"
		logger.Error(task_output)
		return 1, 4, task_output
	}

	metrics := utils.CollectMetrics(gatherers, logger, configuration)
	ctx := context.Background()

	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		logger.Errorf("Failed to create Azure credential: %v", err)
		task_output = task_output + "Failed to create Azure credential: " + err.Error() + "\n"
		return 1, 4, task_output
	}

	clientFactory, err := armmysqlflexibleservers.NewClientFactory(configuration.AzureSubscriptionID, credential, nil)
	if err != nil {
		logger.Errorf("Failed to create Azure MySQL client factory: %v", err)
		task_output = task_output + "Failed to create Azure MySQL client factory: " + err.Error() + "\n"
		return 1, 4, task_output
	}

	serversClient := clientFactory.NewServersClient()
	serverResp, err := serversClient.Get(ctx, configuration.AzureResourceGroup, configuration.AzureMySQLServer, nil)
	if err != nil {
		task_exit_code, task_status = azureMySQLTaskErrorCode(err)
		logger.Errorf("Failed to get Azure Database for MySQL server details: %v", err)
		task_output = task_output + "Failed to get Azure Database for MySQL server details: " + err.Error() + "\n"
		return task_exit_code, task_status, task_output
	}

	if serverResp.Server.Properties != nil && serverResp.Server.Properties.State != nil &&
		*serverResp.Server.Properties.State != armmysqlflexibleservers.ServerStateReady {
		task_output = task_output + fmt.Sprintf("Azure Database for MySQL server state '%s' is not Ready\n", *serverResp.Server.Properties.State)
		logger.Error(task_output)
		return 1, 4, task_output
	}

	configurationsClient := clientFactory.NewConfigurationsClient()
	azureConfigurations, err := loadAzureMySQLConfigurations(ctx, configurationsClient, configuration.AzureResourceGroup, configuration.AzureMySQLServer)
	if err != nil {
		task_exit_code, task_status = azureMySQLTaskErrorCode(err)
		logger.Errorf("Failed to list Azure MySQL configurations: %v", err)
		task_output = task_output + "Failed to list Azure MySQL configurations: " + err.Error() + "\n"
		return task_exit_code, task_status, task_output
	}

	recommendedVars := models.MetricGroupValue{}
	recommendVar := utils.ProcessRepeaters(metrics, repeaters, configuration, logger, models.ModeType{Name: "Configurations", Type: "GetJson"})
	err = json.Unmarshal([]byte(recommendVar), &recommendedVars)
	if err != nil {
		logger.Error(err)
		task_output = task_output + err.Error()
		return 8, 4, task_output
	}

	var updates []*armmysqlflexibleservers.ConfigurationForBatchUpdate
	var skippedReadOnly []string
	var skippedUnknown []string
	restartRequired := false

	for key, rawRecommendedValue := range recommendedVars {
		recommendedValue := mysqlConfigValueToString(rawRecommendedValue)
		currentDBValue := mysqlConfigValueToString(metrics.DB.Conf.Variables[key])

		if azureMySQLConfigValuesEqual(currentDBValue, recommendedValue) {
			continue
		}

		configMetadata, ok := azureConfigurations[strings.ToLower(key)]
		if !ok {
			logger.Infof("Azure MySQL configuration %s is not found and will be skipped", key)
			skippedUnknown = append(skippedUnknown, key)
			continue
		}

		if configMetadata.readOnly {
			logger.Infof("Azure MySQL configuration %s is read-only and will be skipped", key)
			skippedReadOnly = append(skippedReadOnly, key)
			continue
		}

		if azureMySQLConfigValuesEqual(configMetadata.value, recommendedValue) {
			if configMetadata.pendingRestart {
				logger.Infof("Azure MySQL configuration %s already has recommended value and is pending restart", key)
				task_output = task_output + fmt.Sprintf("Azure MySQL configuration %s already has recommended value and is pending restart.\n", key)
				restartRequired = true
			}
			continue
		}

		logger.Infof("Updating Azure MySQL configuration %s: current=%s, recommended=%s, db_current=%s, dynamic=%t",
			configMetadata.name, configMetadata.value, recommendedValue, currentDBValue, configMetadata.dynamic)
		task_output = task_output + fmt.Sprintf("Updating Azure MySQL configuration %s: current=%s, recommended=%s, db_current=%s, dynamic=%t\n",
			configMetadata.name, configMetadata.value, recommendedValue, currentDBValue, configMetadata.dynamic)

		updates = append(updates, &armmysqlflexibleservers.ConfigurationForBatchUpdate{
			Name: to.Ptr(configMetadata.name),
			Properties: &armmysqlflexibleservers.ConfigurationForBatchUpdateProperties{
				Value: to.Ptr(recommendedValue),
			},
		})

		if !configMetadata.dynamic {
			restartRequired = true
		}
	}

	if len(skippedUnknown) > 0 {
		task_output = task_output + fmt.Sprintf("Skipped unknown Azure MySQL configurations: %s\n", strings.Join(skippedUnknown, ", "))
	}
	if len(skippedReadOnly) > 0 {
		task_output = task_output + fmt.Sprintf("Skipped read-only Azure MySQL configurations: %s\n", strings.Join(skippedReadOnly, ", "))
	}

	if len(updates) == 0 {
		logger.Info("No Azure MySQL configuration changes to apply")
		task_output = task_output + "No Azure MySQL configuration changes to apply.\n"
	} else {
		logger.Infof("Applying %d Azure MySQL configuration changes", len(updates))
		poller, err := configurationsClient.BeginBatchUpdate(ctx, configuration.AzureResourceGroup, configuration.AzureMySQLServer, armmysqlflexibleservers.ConfigurationListForBatchUpdate{
			Value: updates,
		}, nil)
		if err != nil {
			task_exit_code, task_status = azureMySQLTaskErrorCode(err)
			logger.Errorf("Azure MySQL configurations update failed: %v", err)
			task_output = task_output + "Azure MySQL configurations update failed: " + err.Error() + "\n"
			return task_exit_code, task_status, task_output
		}
		if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			task_exit_code, task_status = azureMySQLTaskErrorCode(err)
			logger.Errorf("Azure MySQL configurations update failed: %v", err)
			task_output = task_output + "Azure MySQL configurations update failed: " + err.Error() + "\n"
			return task_exit_code, task_status, task_output
		}

		logger.Info("Azure MySQL configurations updated successfully")
		task_output = task_output + "Azure MySQL configurations updated successfully.\n"
	}

	if restart {
		if restartRequired {
			logger.Info("Restarting Azure Database for MySQL server to apply static configurations")
			task_output = task_output + "Restarting Azure Database for MySQL server to apply static configurations.\n"
			poller, err := serversClient.BeginRestart(ctx, configuration.AzureResourceGroup, configuration.AzureMySQLServer, armmysqlflexibleservers.ServerRestartParameter{}, nil)
			if err != nil {
				task_exit_code, task_status = azureMySQLTaskErrorCode(err)
				logger.Errorf("Azure Database for MySQL server restart failed: %v", err)
				task_output = task_output + "Azure Database for MySQL server restart failed: " + err.Error() + "\n"
				return task_exit_code, task_status, task_output
			}
			if _, err := poller.PollUntilDone(ctx, nil); err != nil {
				task_exit_code, task_status = azureMySQLTaskErrorCode(err)
				logger.Errorf("Azure Database for MySQL server restart failed: %v", err)
				task_output = task_output + "Azure Database for MySQL server restart failed: " + err.Error() + "\n"
				return task_exit_code, task_status, task_output
			}
			logger.Info("Azure Database for MySQL server restarted successfully")
			task_output = task_output + "Azure Database for MySQL server restarted successfully.\n"
		}
	} else if restartRequired {
		task_exit_code = 10
		task_status = 4
		task_output = task_output + "Some Azure MySQL configuration changes require restart.\n"
	}

	return task_exit_code, task_status, task_output
}

func loadAzureMySQLConfigurations(ctx context.Context, configurationsClient *armmysqlflexibleservers.ConfigurationsClient,
	resourceGroup string, serverName string) (map[string]azureMySQLConfigurationMetadata, error) {

	configurations := make(map[string]azureMySQLConfigurationMetadata)
	pager := configurationsClient.NewListByServerPager(resourceGroup, serverName, nil)

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}

		for _, configuration := range page.Value {
			if configuration == nil || configuration.Name == nil || configuration.Properties == nil {
				continue
			}

			metadata := azureMySQLConfigurationMetadata{
				name:           *configuration.Name,
				dynamic:        configuration.Properties.IsDynamicConfig != nil && *configuration.Properties.IsDynamicConfig == armmysqlflexibleservers.IsDynamicConfigTrue,
				readOnly:       configuration.Properties.IsReadOnly != nil && *configuration.Properties.IsReadOnly == armmysqlflexibleservers.IsReadOnlyTrue,
				pendingRestart: configuration.Properties.IsConfigPendingRestart != nil && *configuration.Properties.IsConfigPendingRestart == armmysqlflexibleservers.IsConfigPendingRestartTrue,
			}
			if configuration.Properties.Value != nil {
				metadata.value = *configuration.Properties.Value
			}

			configurations[strings.ToLower(metadata.name)] = metadata
		}
	}

	return configurations, nil
}

func mysqlConfigValueToString(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case bool:
		if v {
			return "ON"
		}
		return "OFF"
	}

	switch number := utils.AsNumber(value); number.Kind {
	case utils.SignedNumber:
		return strconv.FormatInt(number.Int, 10)
	case utils.UnsignedNumber:
		return strconv.FormatUint(number.Uint, 10)
	case utils.FloatNumber:
		// Integral floats keep their integer spelling so a recommendation of
		// 100 is not submitted to Azure as "100.0".
		if math.Trunc(number.Float) == number.Float {
			return strconv.FormatInt(int64(number.Float), 10)
		}
		return strconv.FormatFloat(number.Float, 'f', -1, number.FloatBits)
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func azureMySQLConfigValuesEqual(current string, recommended string) bool {
	current = strings.TrimSpace(current)
	recommended = strings.TrimSpace(recommended)

	if strings.EqualFold(current, recommended) {
		return true
	}

	currentFloat, currentErr := strconv.ParseFloat(current, 64)
	recommendedFloat, recommendedErr := strconv.ParseFloat(recommended, 64)
	if currentErr == nil && recommendedErr == nil {
		return math.Abs(currentFloat-recommendedFloat) < 0.000000001
	}

	return false
}

func azureMySQLTaskErrorCode(err error) (int, int) {
	errMessage := strings.ToLower(err.Error())
	if strings.Contains(errMessage, "authorizationfailed") ||
		strings.Contains(errMessage, "forbidden") ||
		strings.Contains(errMessage, "unauthorized") ||
		strings.Contains(errMessage, "access denied") ||
		strings.Contains(errMessage, "denied") {
		return 9, 4
	}

	return 8, 4
}
