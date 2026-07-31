package metrics

import (
	"runtime"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

type AgentMetricsGatherer struct {
	logger        logging.Logger
	configuration *config.Config
}

func NewAgentMetricsGatherer(logger logging.Logger, configuration *config.Config) *AgentMetricsGatherer {
	return &AgentMetricsGatherer{
		logger:        logger,
		configuration: configuration,
	}
}

func (Agent *AgentMetricsGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(Agent.configuration, Agent.logger)

	output := make(map[string]interface{})
	output["Version"] = config.ReleemAgentVersion
	if len(Agent.configuration.Hostname) > 0 {
		output["Hostname"] = Agent.configuration.Hostname
	}
	output["QueryOptimization"] = Agent.configuration.QueryOptimization

	models.SampleQueriesMutex.RLock()
	output["SampleQueriesCount"] = len(models.SampleQueries)
	models.SampleQueriesMutex.RUnlock()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	output["AllocMemory"] = m.Alloc
	output["TotalAllocMemory"] = m.TotalAlloc

	metrics.ReleemAgent.Info = output
	metrics.ReleemAgent.Conf = *Agent.configuration

	Agent.logger.V(5).Info("CollectMetrics Agent ", output)
	return nil

}
