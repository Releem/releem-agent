package metrics

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/tasks"
	"github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

var Ready bool

func oneShotMode(mode models.ModeType) bool {
	return (mode.Name == "Configurations" && mode.Type != "Default") ||
		mode.Name == "Event" || mode.Name == "TaskByName"
}

// Set up channel on which to send signal notifications.
// We must use a buffered channel or risk missing the signal
// if we're not ready to receive when the signal is sent.
func makeTerminateChannel() <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	return ch
}

func RunWorker(gatherers map[string][]models.MetricsGatherer, repeaters models.MetricsRepeater, logger logging.Logger,
	configuration *config.Config, Mode models.ModeType) {
	defer utils.HandlePanic(configuration, logger)

	var GenerateTimer, timer, QueryOptimizationTimer *time.Timer

	models.SampleQueries = make(map[string]string)
	models.SampleQueriesMutex = sync.RWMutex{}
	terminator := makeTerminateChannel()
	oneShot := oneShotMode(Mode)

	if oneShot {
		GenerateTimer = time.NewTimer(1 * time.Second)
		timer = time.NewTimer(24 * time.Hour)
		QueryOptimizationTimer = time.NewTimer(24 * time.Hour)
	} else {
		GenerateTimer = time.NewTimer(configuration.GenerateConfigPeriod * time.Second)
		timer = time.NewTimer(1 * time.Second)
		QueryOptimizationTimer = time.NewTimer(1 * time.Minute)
	}
	CollectSampleQueries := time.NewTimer(1 * time.Second)
	if !configuration.QueryOptimization {
		CollectSampleQueries.Stop()
	}
	utils.GetStrategyCollectionSampleQueries(configuration, logger, "0")
loop:
	for {
		select {
		case <-terminator:
			logger.Info("Exiting")
			break loop
		case <-timer.C:
			logger.Info("* Starting to collect metrics...")
			timer.Reset(configuration.MetricsPeriod * time.Second)
			go func() {
				defer utils.HandlePanic(configuration, logger)
				metrics := utils.CollectMetrics(append(gatherers["default"], gatherers["metrics"]...), logger, configuration)
				if metrics == nil {
					return
				}
				utils.GetStrategyCollectionSampleQueries(configuration, logger, utils.ConvertUptimeToStr(metrics.DB.Metrics.Status))
				response := utils.ProcessRepeaters(metrics, repeaters, configuration, logger, models.ModeType{Name: "Metrics", Type: ""})
				if response == "Task" {
					logger.Info("* A task received by the agent...")
					f := tasks.ProcessTaskFunc(repeaters, gatherers["default"], logger, configuration)
					time.AfterFunc(5*time.Second, f)
				}

				logger.Info("* Database Metrics are saved...")
			}()
		case <-GenerateTimer.C:
			logger.Info("* Starting to collect metrics...")
			GenerateTimer.Reset(configuration.GenerateConfigPeriod * time.Second)
			go func() {
				var metrics *models.Metrics
				logger.Info("* Collecting metrics...")
				defer utils.HandlePanic(configuration, logger)
				if Mode.Name == "TaskByName" {
					if Mode.Type == "queries_optimization" {
						metrics = utils.CollectMetrics(append(gatherers["default"], gatherers["query_optimization"]...), logger, configuration)
					} else {
						metrics = utils.CollectMetrics(append(gatherers["default"], gatherers["configuration"]...), logger, configuration)
					}
				} else {
					metrics = utils.CollectMetrics(append(gatherers["default"], gatherers["configuration"]...), logger, configuration)
				}
				if metrics == nil {
					return
				}
				utils.GetStrategyCollectionSampleQueries(configuration, logger, "0")
				logger.Info("* Sending metrics to the Releem Cloud Platform...")
				utils.ProcessRepeaters(metrics, repeaters, configuration, logger, Mode)
				if Mode.Name == "Configurations" {
					logger.Info("* The recommended Database configuration has been downloaded to: ", configuration.GetReleemConfDir())
				}

				if oneShot {
					logger.Info("Exiting")
					os.Exit(0)
				}
				logger.Info("* Database Metrics are saved...")
			}()
		case <-QueryOptimizationTimer.C:
			logger.Info("* Starting to collect Database metrics for Query Analytics...")
			QueryOptimizationTimer.Reset(configuration.QueryOptimizationPeriod * time.Second)
			go func() {
				defer utils.HandlePanic(configuration, logger)
				metrics := utils.CollectMetrics(append(gatherers["default"], gatherers["query_optimization"]...), logger, configuration)
				if metrics == nil {
					return
				}
				utils.ProcessRepeaters(metrics, repeaters, configuration, logger, models.ModeType{Name: "Metrics", Type: "Queries"})
				logger.Info("* Database metrics for Query Analytics are saved...")
			}()
		case <-CollectSampleQueries.C:
			CollectSampleQueries.Reset(configuration.CollectSampleQueriesPeriod * time.Second)
			go func() {
				defer utils.HandlePanic(configuration, logger)
				utils.CollectMetrics(gatherers["sample_queries"], logger, configuration)
			}()
		}
	}
}
