package config

import (
	"os"
	"runtime"
	"time"

	logging "github.com/google/logger"
	"github.com/hashicorp/hcl"
)

const (
	ReleemAgentVersion                  = "1.25.2"
	DefaultTableSizeCacheTableThreshold = int64(10000)
	DefaultTableSizeCacheRAMMultiplier  = int64(4)
	DefaultTableSizeCacheTTLSeconds     = time.Duration(604800)
)

type Config struct {
	Debug                        bool          `hcl:"debug"`
	Env                          string        `hcl:"env"`
	Hostname                     string        `hcl:"hostname"`
	ApiKey                       string        `hcl:"apikey"`
	MetricsPeriod                time.Duration `hcl:"interval_seconds"`
	ReadConfigPeriod             time.Duration `hcl:"interval_read_config_seconds"`
	GenerateConfigPeriod         time.Duration `hcl:"interval_generate_config_seconds"`
	TableSizeCacheTableThreshold int64         `hcl:"table_size_cache_table_threshold"`
	TableSizeCacheRAMMultiplier  int64         `hcl:"table_size_cache_ram_multiplier"`
	TableSizeCacheTTL            time.Duration `hcl:"table_size_cache_ttl_seconds"`
	QueryOptimizationPeriod      time.Duration `hcl:"interval_query_optimization_seconds"`
	CollectSampleQueriesPeriod   time.Duration `hcl:"interval_collect_sample_queries_seconds"`
	MysqlPassword                string        `hcl:"mysql_password" json:"-"`
	MysqlUser                    string        `hcl:"mysql_user"`
	MysqlHost                    string        `hcl:"mysql_host"`
	MysqlPort                    string        `hcl:"mysql_port"`
	MysqlSslMode                 bool          `hcl:"mysql_ssl_mode"`
	MysqlConfDir                 string        `hcl:"mysql_cnf_dir"`
	MysqlRestartService          string        `hcl:"mysql_restart_service"`
	PgPassword                   string        `hcl:"pg_password" json:"-"`
	PgUser                       string        `hcl:"pg_user"`
	PgHost                       string        `hcl:"pg_host"`
	PgPort                       string        `hcl:"pg_port"`
	PgSslMode                    bool          `hcl:"pg_ssl_mode"`
	PgConfDir                    string        `hcl:"pg_cnf_dir"`
	PgRestartService             string        `hcl:"pg_restart_service"`
	ReleemConfDir                string        `hcl:"releem_cnf_dir"`
	ReleemDir                    string        `hcl:"releem_dir"`
	MemoryLimit                  int           `hcl:"memory_limit"`
	InstanceType                 string        `hcl:"instance_type"`
	AwsRegion                    string        `hcl:"aws_region"`
	AwsRDSDB                     string        `hcl:"aws_rds_db"`
	AwsRDSParameterGroup         string        `hcl:"aws_rds_parameter_group"`
	AwsRDSClusterParameterGroup  string        `hcl:"aws_rds_cluster_parameter_group"`
	GcpProjectId                 string        `hcl:"gcp_project_id"`
	GcpRegion                    string        `hcl:"gcp_region"`
	GcpCloudSqlInstance          string        `hcl:"gcp_cloudsql_instance"`
	GcpCloudSqlPublicConnection  bool          `hcl:"gcp_cloudsql_public_connection"`
	AzureSubscriptionID          string        `hcl:"azure_subscription_id"`
	AzureResourceGroup           string        `hcl:"azure_resource_group"`
	AzureMySQLServer             string        `hcl:"azure_mysql_server"`
	QueryOptimization            bool          `hcl:"query_optimization"`
	DatabasesQueryOptimization   string        `hcl:"databases_query_optimization"`
	ReleemRegion                 string        `hcl:"releem_region"`
	// Task automator configuration
	EnableExecDDL       bool    `hcl:"enable_exec_ddl"`
	BackupDir           string  `hcl:"backup_dir"`
	PTOSCPath           string  `hcl:"ptosc_path"`
	MysqldumpPath       string  `hcl:"mysqldump_path"`
	XtrabackupPath      string  `hcl:"xtrabackup_path"`
	BackupSpaceBuffer   float64 `hcl:"backup_space_buffer"`
	OnlineDDLTestSchema string  `hcl:"online_ddl_test_schema"`
	DisableSpaceChecks  bool    `hcl:"disable_space_checks"`
}

func LoadConfig(filename string, logger logging.Logger) (*Config, error) {
	logger.Infof("Loading configuration %s", filename)
	configBytes, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	return LoadConfigFromString(string(configBytes), logger)
}

func LoadConfigFromString(data string, logger logging.Logger) (*Config, error) {
	config := &Config{}
	err := hcl.Decode(&config, data)
	if err != nil {
		return nil, err
	}
	if config.MetricsPeriod == 0 {
		config.MetricsPeriod = 60
	}
	if config.ReadConfigPeriod == 0 {
		config.ReadConfigPeriod = 3600
	}
	if config.GenerateConfigPeriod == 0 {
		config.GenerateConfigPeriod = 43200
	}
	if config.TableSizeCacheTableThreshold <= 0 {
		config.TableSizeCacheTableThreshold = DefaultTableSizeCacheTableThreshold
	}
	if config.TableSizeCacheRAMMultiplier <= 0 {
		config.TableSizeCacheRAMMultiplier = DefaultTableSizeCacheRAMMultiplier
	}
	if config.TableSizeCacheTTL <= 0 {
		config.TableSizeCacheTTL = DefaultTableSizeCacheTTLSeconds
	}
	if config.QueryOptimizationPeriod == 0 {
		config.QueryOptimizationPeriod = 3600
	}
	if config.CollectSampleQueriesPeriod == 0 {
		config.CollectSampleQueriesPeriod = 10
	}
	if config.MysqlHost == "" {
		config.MysqlHost = "127.0.0.1"
	}
	if config.MysqlPort == "" {
		config.MysqlPort = "3306"
	}
	if config.PgHost == "" {
		config.PgHost = "127.0.0.1"
	}
	if config.PgPort == "" {
		config.PgPort = "5432"
	}
	if config.ReleemDir == "" {
		config.ReleemDir = defaultReleemDirPath()
	}
	if config.InstanceType == "" {
		config.InstanceType = "local"
	}
	// Set defaults for task-automator config fields
	if config.BackupDir == "" {
		config.BackupDir = "/tmp/backups"
	}
	if config.PTOSCPath == "" {
		config.PTOSCPath = "pt-online-schema-change"
	}
	if config.MysqldumpPath == "" {
		config.MysqldumpPath = "mysqldump"
	}
	if config.XtrabackupPath == "" {
		config.XtrabackupPath = "xtrabackup"
	}
	if config.BackupSpaceBuffer == 0 {
		config.BackupSpaceBuffer = 20.0 // Default 20% buffer
	}
	if config.OnlineDDLTestSchema == "" {
		config.OnlineDDLTestSchema = "releem_online_ddl_test"
	}
	return config, nil
}

func (config *Config) GetApiKey() string {
	return config.ApiKey
}
func (config *Config) GetEnv() string {
	return config.Env
}

func (config *Config) GetMemoryLimit() int {
	return config.MemoryLimit
}
func (config *Config) GetReleemConfDir() string {
	return config.ReleemConfDir
}

// GetDatabaseType returns the database type based on configuration parameters
func (config *Config) GetDatabaseType() string {
	// Check if PostgreSQL parameters are configured
	if config.PgUser != "" && config.PgPassword != "" {
		return "postgresql"
	}
	// Default to MySQL if MySQL parameters are configured or no specific DB params
	if config.MysqlUser != "" && config.MysqlPassword != "" {
		return "mysql"
	}
	// Default to MySQL for backward compatibility
	return "mysql"
}

func defaultReleemDirPath() string {
	switch runtime.GOOS {
	case "windows":
		return "C:\\Program Files\\ReleemAgent"
	default: // for Linux and other UNIX-like systems
		return "/opt/releem"
	}
}
