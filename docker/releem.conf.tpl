# ApiKey string `hcl:"apikey"`
# Defaults to 3600 seconds, api key for Releem Platform.
apikey="${RELEEM_API_KEY}"

hostname="${RELEEM_HOSTNAME}"

# MemoryLimit int `hcl:"memory_limit"`
# Defaults to 0, Mysql memory usage limit.
memory_limit=${MEMORY_LIMIT:-0}

# MetricsPeriod time.Duration `hcl:"interval_seconds"`
# Defaults to 30 seconds, how often metrics are collected.
interval_seconds=60

# ReadConfigPeriod time.Duration `hcl:"interval_read_config_seconds"`
# Defaults to 3600 seconds, how often to update the values from the config.
interval_read_config_seconds=3600

# GenerateConfigPeriod time.Duration `hcl:"interval_generate_config_seconds"`
# Defaults to 43200 seconds, how often to generate recommend the config.
interval_generate_config_seconds=${RELEEM_INTERVAL_COLLECT_ALL_METRICS:-43200}

table_size_cache_table_threshold=${RELEEM_TABLE_SIZE_CACHE_TABLE_THRESHOLD:-10000}
table_size_cache_ram_multiplier=${RELEEM_TABLE_SIZE_CACHE_RAM_MULTIPLIER:-4}
table_size_cache_ttl_seconds=${RELEEM_TABLE_SIZE_CACHE_TTL_SECONDS:-604800}

# QueryOptimization time.Duration `hcl:"interval_query_optimization_seconds"`
# Defaults to 3600 seconds, how often query metrics are collected.
interval_query_optimization_seconds=3600

# MysqlUser string`hcl:"mysql_user"`
# Mysql user name for collection metrics.
mysql_user="${DB_USER:-releem}"

# MysqlPassword string `hcl:"mysql_password"`
# Mysql user password for collection metrics.
mysql_password="${DB_PASSWORD:-releem}"

# MysqlHost string `hcl:"mysql_host"`
# Mysql host for collection metrics.
mysql_host="${DB_HOST:-127.0.0.1}"

# MysqlPort string `hcl:"mysql_port"`
# Mysql port for collection metrics.
mysql_port="${DB_PORT:-3306}"

#MysqlSslMode bool `hcl:"mysql_ssl_mode"`
# Enable SSL connection to MySQL
mysql_ssl_mode=${DB_SSL:-false}

# CommandRestartService string `hcl:"mysql_restart_service"`
# Defaults to 3600 seconds, command to restart service mysql.
mysql_restart_service="${RELEEM_MYSQL_RESTART_SERVICE:-/bin/systemctl restart mysql}"

# MysqlConfDir string `hcl:"mysql_cnf_dir"`
# The path to copy the recommended config.
mysql_cnf_dir="${RELEEM_MYSQL_CONF_DIR:-/etc/mysql/releem.conf.d}"

# PgUser string`hcl:"pg_user"`
# PostgreSQL user name for collection metrics.
pg_user="${RELEEM_PG_LOGIN:-${PG_USER:-}}"

# PgPassword string `hcl:"pg_password"`
# PostgreSQL user password for collection metrics.
pg_password="${RELEEM_PG_PASSWORD:-${PG_PASSWORD:-}}"

# PgHost string `hcl:"pg_host"`
# PostgreSQL host for collection metrics.
pg_host="${RELEEM_PG_HOST:-${PG_HOST:-127.0.0.1}}"

# PgPort string `hcl:"pg_port"`
# PostgreSQL port for collection metrics.
pg_port="${RELEEM_PG_PORT:-${PG_PORT:-5432}}"

#PgSslMode bool `hcl:"pg_ssl_mode"`
# Enable SSL connection to PostgreSQL
pg_ssl_mode=${RELEEM_PG_SSL_MODE:-${PG_SSL:-false}}

# CommandRestartService string `hcl:"pg_restart_service"`
# Defaults to 3600 seconds, command to restart service postgresql.
pg_restart_service="${RELEEM_PG_RESTART_SERVICE:-/bin/systemctl restart postgresql}"

# PgConfDir string `hcl:"pg_cnf_dir"`
# The path to copy the recommended config.
pg_cnf_dir="${RELEEM_PG_CONF_DIR:-/etc/postgresql/releem.conf.d}"


# ReleemConfDir string `hcl:"releem_cnf_dir"`
# Releem Agent configuration path.
releem_cnf_dir="/opt/releem/conf"

# InstanceType string `hcl:"instance_type"`
# Defaults to local, type of instance "local" or "aws/rds"
instance_type="${INSTANCE_TYPE:-local}"

# AwsRegion string `hcl:"aws_region"`
# Defaults to us-east-1, AWS region for RDS
aws_region="${AWS_REGION}"

# AwsRDSDB string `hcl:"aws_rds_db"`
# RDS database name.
aws_rds_db="${AWS_RDS_DB}"

# AWS_RDS_PARAMETER_GROUP string `hcl:"aws_rds_parameter_group"`
# RDS database parameter group name.
aws_rds_parameter_group="${AWS_RDS_PARAMETER_GROUP}"

#GcpProjectId string `hcl:"gcp_project_id"`
#GCP project ID for Cloud SQL instance
gcp_project_id="${RELEEM_GCP_PROJECT_ID}"

#GcpRegion string `hcl:"gcp_region"`
#GCP region for Cloud SQL instance
gcp_region="${RELEEM_GCP_REGION}"

#GcpCloudSqlInstance string `hcl:"gcp_cloudsql_instance"`
#Name of Cloud SQL instance
gcp_cloudsql_instance="${RELEEM_GCP_CLOUDSQL_INSTANCE}"

#GcpCloudSqlPublicConnection bool `hcl:"gcp_cloudsql_public_connection"`
#Enable public connection to Cloud SQL instance
gcp_cloudsql_public_connection=${RELEEM_GCP_CLOUDSQL_PUBLIC_CONNECTION:-false}

#AzureSubscriptionID string `hcl:"azure_subscription_id"`
#Azure subscription ID for Azure Database for MySQL Flexible Server
azure_subscription_id="${RELEEM_AZURE_SUBSCRIPTION_ID}"

#AzureResourceGroup string `hcl:"azure_resource_group"`
#Azure resource group for Azure Database for MySQL Flexible Server
azure_resource_group="${RELEEM_AZURE_RESOURCE_GROUP}"

#AzureMySQLServer string `hcl:"azure_mysql_server"`
#Name of Azure Database for MySQL Flexible Server
azure_mysql_server="${RELEEM_AZURE_MYSQL_SERVER}"

# Env string `hcl:"env"`
# Releem Environment.
env="${RELEEM_ENV:-prod}"

# Debug string `hcl:"debug"`
# Releem Debug messages
debug=${RELEEM_DEBUG:-false}

# Collect Explain string `hcl:"query_optimization"`
# Releem collect explain for query
query_optimization=${RELEEM_QUERY_OPTIMIZATION:-false}

# databases_query_optimization string `hcl:"databases_query_optimization"`
# List of databases for query optimization
databases_query_optimization="${RELEEM_DATABASES_QUERY_OPTIMIZATION}"

# releem_region string `hcl:"releem_region"`
# Server data storage region - EU or empty.
releem_region="${RELEEM_REGION}"
