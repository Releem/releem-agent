// Example of a daemon with echo service
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"runtime"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Releem/daemon"
	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/metrics"
	"github.com/Releem/mysqlconfigurer/metrics/mysql"
	"github.com/Releem/mysqlconfigurer/metrics/postgresql"
	"github.com/Releem/mysqlconfigurer/metrics/system"
	"github.com/Releem/mysqlconfigurer/models"
	r "github.com/Releem/mysqlconfigurer/repeater"
	"github.com/Releem/mysqlconfigurer/utils"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	_ "github.com/go-sql-driver/mysql"
	logging "github.com/google/logger"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"google.golang.org/api/sqladmin/v1"
)

const (
	// name of the service
	serviceName        = "releem-agent"
	serviceDescription = "Releem Agent"
)

var logger logging.Logger
var SetConfigRun, GetConfigRun, InitialConfigRun *bool
var ConfigFile, AgentEvent, AgentTask *string

// Service has embedded daemon
type Service struct {
	daemon.Daemon
}
type Programm struct{}

func exitRunWithError(args ...interface{}) {
	logger.Error(args...)
	os.Exit(1)
}

func (programm *Programm) Stop() {
	// Stop should not block. Return with a few seconds.
}

func (programm *Programm) Start() {
	// Start should not block. Do the actual work async.
	go programm.Run()
}

func (programm *Programm) Run() {
	// var gatherers map[string][]models.MetricsGatherer
	gatherers := make(map[string][]models.MetricsGatherer)
	var Mode models.ModeType

	// Do something, call your goroutines, etc
	logger.Info("Releem-agent version is ", config.ReleemAgentVersion) //
	configuration, err := config.LoadConfig(*ConfigFile, logger)
	if err != nil {
		exitRunWithError("The agent configuration failed to load", err)
	}
	defer utils.HandlePanic(configuration, logger)

	if configuration.Debug {
		logger.SetLevel(10)
	} else {
		logger.SetLevel(1)
	}

	if len(*AgentEvent) > 0 {
		Mode.Name = "Event"
		Mode.Type = *AgentEvent
	} else if len(*AgentTask) > 0 {
		Mode.Name = "TaskByName"
		Mode.Type = *AgentTask
	} else {
		Mode.Name = "Configurations"
		if *SetConfigRun {
			Mode.Type = "Set"
		} else if *InitialConfigRun {
			Mode.Type = "GetInitial"
		} else if *GetConfigRun {
			Mode.Type = "Get"
		} else {
			Mode.Type = "Default"
		}
	}
	// if Mode.Name != "Event" {
	// Select how we collect instance metrics depending on InstanceType
	switch configuration.InstanceType {
	case "aws/rds":
		logger.Info("InstanceType is aws/rds")
		logger.Info("Loading AWS configuration")

		awscfg, err := awsconfig.LoadDefaultConfig(context.TODO(), awsconfig.WithRegion(configuration.AwsRegion))
		if err != nil {
			exitRunWithError("Load AWS configuration FAILED", err)
		} else {
			logger.Info("AWS configuration loaded SUCCESS")
		}

		cwlogsclient := cloudwatchlogs.NewFromConfig(awscfg)
		//	cwclient := cloudwatch.NewFromConfig(awscfg)
		rdsclient := rds.NewFromConfig(awscfg)
		//	ec2client := ec2.NewFromConfig(awscfg)

		metadata, err := awsrds.DiscoverInstance(context.TODO(), rdsclient, configuration.AwsRDSDB)
		if err != nil {
			exitRunWithError(err.Error())
		}

		configuration.Hostname = configuration.AwsRDSDB
		metadata.ApplyEndpoint(configuration)
		gatherers["default"] = append(gatherers["default"], system.NewAWSRDSEnhancedMetricsGatherer(
			logger,
			cwlogsclient,
			configuration,
			func(ctx context.Context) (awsrds.Metadata, error) {
				return awsrds.DiscoverInstance(ctx, rdsclient, configuration.AwsRDSDB)
			},
		))
		logger.Info("AWS RDS DB instance found: ", configuration.AwsRDSDB)
	case "gcp/cloudsql":
		logger.Info("InstanceType is gcp/cloudsql")
		logger.Info("Loading GCP configuration")

		// Initialize GCP clients with Application Default Credentials
		ctx := context.Background()

		// Create monitoring client
		monitoringClient, err := monitoring.NewMetricClient(ctx)
		if err != nil {
			exitRunWithError("Failed to create GCP monitoring client", err)
		}
		defer monitoringClient.Close()

		// Create SQL Admin client
		sqlAdminService, err := sqladmin.NewService(ctx)
		if err != nil {
			exitRunWithError("Failed to create GCP SQL Admin client", err)
		}
		logger.Info("GSP configuration loaded SUCCESS")
		// Get instance details
		instance, err := sqlAdminService.Instances.Get(configuration.GcpProjectId, configuration.GcpCloudSqlInstance).Do()
		if err != nil {
			exitRunWithError("Failed to get Cloud SQL instance details", err)
		}

		logger.Info("GCP Cloud SQL instance found: ", instance.Name)

		// Find private IP address for connection
		var connectionIP, typeIP string
		if configuration.GcpCloudSqlPublicConnection {
			typeIP = "PRIMARY"
		} else {
			typeIP = "PRIVATE"
		}
		for _, ipAddr := range instance.IpAddresses {
			if ipAddr.Type == typeIP {
				connectionIP = ipAddr.IpAddress
				break
			}
		}

		if connectionIP != "" {
			// Set connection details
			configuration.Hostname = configuration.GcpCloudSqlInstance
			configuration.MysqlHost = connectionIP
			logger.Info("Using following IP for Cloud SQL connection: ", connectionIP)
		} else {
			exitRunWithError("No IP addresses found for Cloud SQL instance")
		}

		// Add GCP gatherer
		gatherers["default"] = append(gatherers["default"], system.NewGCPCloudSQLEnhancedMetricsGatherer(logger, monitoringClient, sqlAdminService, configuration))

	case "azure/mysql":
		logger.Info("InstanceType is azure/mysql")
		logger.Info("Loading Azure configuration")

		credential, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			exitRunWithError("Failed to create Azure credential", err)
		}

		azureGatherer := system.NewAzureMySQLEnhancedMetricsGatherer(logger, credential, configuration)
		instance, err := azureGatherer.GetServer(context.Background())
		if err != nil {
			exitRunWithError("Failed to get Azure Database for MySQL server details", err)
		}

		if instance.Properties == nil || instance.Properties.FullyQualifiedDomainName == nil || *instance.Properties.FullyQualifiedDomainName == "" {
			exitRunWithError("Azure Database for MySQL server has no fully qualified domain name")
		}
		configuration.Hostname = configuration.AzureMySQLServer
		configuration.MysqlHost = *instance.Properties.FullyQualifiedDomainName
		logger.Info("Azure Database for MySQL server found: ", configuration.Hostname)
		logger.Info("Using following host for Azure MySQL connection: ", configuration.MysqlHost)

		gatherers["default"] = append(gatherers["default"], azureGatherer)

	default:
		logger.Info("InstanceType is Local")
		gatherers["default"] = append(gatherers["default"], system.NewOSMetricsGatherer(logger, configuration))

	}

	// Initialize database connection based on database type
	dbType := configuration.GetDatabaseType()
	models.DB = utils.ConnectionDatabase(configuration, logger, "")
	if models.DB == nil {
		exitRunWithError("Failed to connect to the configured database")
	}
	defer models.DB.Close()

	//Init repeaters
	// repeaters := make(map[string]models.MetricsRepeater)
	// repeaters["Metrics"] = models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, models.Mode{Name: "Metrics", Type: ""}))
	// repeaters["Configurations"] = models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, Mode))
	// repeaters["Event"] = models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, Mode))
	// repeaters["TaskGet"] = models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, models.Mode{Name: "TaskGet", Type: ""}))
	// repeaters["TaskStatus"] = models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, models.Mode{Name: "TaskStatus", Type: ""}))
	// repeaters["TaskSet"] = models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, Mode))
	// repeaters["GetConfigurationJson"] = models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, models.Mode{Name: "Configurations", Type: "ForceGetJson"}))
	// repeaters["QueryOptimization"] = models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, models.Mode{Name: "Metrics", Type: "QuerysOptimization"}))
	// repeaters["DatabaseSchema"] = models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, models.Mode{Name: "TaskSet", Type: "queries_optimization"}))
	//var repeaters models.MetricsRepeater
	repeaters := models.MetricsRepeater(r.NewReleemConfigurationsRepeater(configuration, logger))

	//Init gatherers based on database type
	switch dbType {
	case "postgresql":
		postgresCapabilities := postgresql.NewPostgresCapabilities(logger)
		gatherers["default"] = append(gatherers["default"],
			postgresql.NewDBConfGatherer(logger, configuration),
			postgresql.NewDBInfoBaseGatherer(logger, configuration),
			postgresql.NewDBInfoGatherer(logger, configuration),
			postgresql.NewDBMetricsBaseGatherer(logger, configuration, postgresCapabilities),
			metrics.NewAgentMetricsGatherer(logger, configuration))

		gatherers["metrics"] = []models.MetricsGatherer{}

		gatherers["configuration"] = append(gatherers["configuration"], postgresql.NewDBMetricsConfigGatherer(logger, configuration))

		gatherers["query_optimization"] = append(gatherers["query_optimization"], postgresql.NewDBCollectQueriesOptimization(logger, configuration, postgresCapabilities))

		gatherers["sample_queries"] = []models.MetricsGatherer{}

	case "mysql":
		fallthrough
	default:
		gatherers["default"] = append(gatherers["default"],
			mysql.NewDBConfGatherer(logger, configuration),
			mysql.NewDBInfoGatherer(logger, configuration),
			mysql.NewDBMetricsBaseGatherer(logger, configuration),
			metrics.NewAgentMetricsGatherer(logger, configuration))

		gatherers["metrics"] = append(gatherers["metrics"], mysql.NewDBMetricsGatherer(logger, configuration))

		gatherers["configuration"] = append(gatherers["configuration"], mysql.NewDBMetricsConfigGatherer(logger, configuration))

		gatherers["query_optimization"] = append(gatherers["query_optimization"], mysql.NewDBCollectQueriesOptimization(logger, configuration))

		gatherers["sample_queries"] = append(gatherers["sample_queries"], mysql.NewDBCollectSampleQueriesGatherer(logger, configuration))
	}
	metrics.RunWorker(gatherers, repeaters, logger, configuration, Mode)

}

// Manage by daemon commands or run the daemon
func (service *Service) Manage(command []string) (string, error) {
	usage := "Usage: myservice install | remove | start | stop | status"
	// if received any kind of command, do it
	if len(command) >= 1 {
		switch command[0] {
		case "install":
			return service.Install()
		case "remove":
			return service.Remove()
		case "start":
			return service.Start()
		case "stop":
			return service.Stop()
		case "status":
			return service.Status()
		default:
			return usage, nil
		}
	}

	return service.Run(&Programm{})

	// never happen, but need to complete code
}
func defaultConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		return "C:\\ProgramData\\ReleemAgent\\releem.conf"
	default: // for Linux and other UNIX-like systems
		return "/opt/releem/releem.conf"
	}
}
func defaultDependencies() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{}
	default: // for Linux and other UNIX-like systems
		return []string{"network.target"}
	}
}

func defaultSystemLogFlag() bool {
	switch runtime.GOOS {
	case "windows":
		return true
	default: // for Linux and other UNIX-like systems
		return false
	}
}

func shouldRunOneShotMode(commandLen int, setConfig bool, getConfig bool, initialConfig bool, agentEvent string, agentTask string) bool {
	if commandLen > 0 {
		return false
	}

	return setConfig || getConfig || initialConfig || len(agentEvent) > 0 || len(agentTask) > 0
}

func main() {
	logger = *logging.Init("releem-agent", true, defaultSystemLogFlag(), io.Discard)
	defer logger.Close()
	logging.SetFlags(log.LstdFlags | log.Lshortfile)

	defaultPath := defaultConfigPath()
	SetConfigRun = flag.Bool("f", false, "Run Releem agent to generate configuration")
	GetConfigRun = flag.Bool("c", false, "Run Releem agent to download configuration")
	InitialConfigRun = flag.Bool("initial", false, "Run Releem agent to generate initial configuration")
	ConfigFile = flag.String("config", defaultPath, "Path to the configuration file (default: \""+defaultPath+"\")")
	AgentEvent = flag.String("event", "", "Run Releem agent to handle event")
	AgentTask = flag.String("task", "", "Run Releem agent to execute task")
	flag.Parse()
	command := flag.Args()

	if shouldRunOneShotMode(len(command), *SetConfigRun, *GetConfigRun, *InitialConfigRun, *AgentEvent, *AgentTask) {
		(&Programm{}).Run()
		logger.Info("Exiting")
		os.Exit(0)
	}

	dependencies := defaultDependencies()
	srv, err := daemon.New(serviceName, serviceDescription, daemon.SystemDaemon, dependencies...)
	if err != nil {
		logger.Error("Error: ", err)
		os.Exit(1)
	}
	service := &Service{srv}
	status, err := service.Manage(command)

	if err != nil {
		logger.Info(status, "\nError: ", err)
		os.Exit(1)
	}
	logger.Info(status)
}
