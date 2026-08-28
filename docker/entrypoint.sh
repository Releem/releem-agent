#!/bin/bash
set -e
# # Substitute environment variables in Prosody configs
#envsubst < /docker/releem.conf.tpl > /opt/releem/releem.conf

cat <<EOF > /opt/releem/releem.conf
apikey="${RELEEM_API_KEY}"
hostname="${RELEEM_HOSTNAME}"
memory_limit=${MEMORY_LIMIT:-0}
interval_seconds=60
interval_read_config_seconds=3600
interval_generate_config_seconds=${RELEEM_INTERVAL_COLLECT_ALL_METRICS:-43200}
table_size_cache_table_threshold=${RELEEM_TABLE_SIZE_CACHE_TABLE_THRESHOLD:-10000}
table_size_cache_ram_multiplier=${RELEEM_TABLE_SIZE_CACHE_RAM_MULTIPLIER:-4}
table_size_cache_ttl_seconds=${RELEEM_TABLE_SIZE_CACHE_TTL_SECONDS:-604800}
interval_query_optimization_seconds=3600
mysql_user="${DB_USER:-releem}"
mysql_password="${DB_PASSWORD:-releem}"
mysql_host="${DB_HOST:-127.0.0.1}"
mysql_port="${DB_PORT:-3306}"
mysql_ssl_mode=${DB_SSL:-false}
mysql_restart_service="${RELEEM_MYSQL_RESTART_SERVICE:-/bin/systemctl restart mysql}"
mysql_cnf_dir="${RELEEM_MYSQL_CONF_DIR:-/etc/mysql/releem.conf.d}"
pg_user="${RELEEM_PG_LOGIN:-${PG_USER:-}}"
pg_password="${RELEEM_PG_PASSWORD:-${PG_PASSWORD:-}}"
pg_host="${RELEEM_PG_HOST:-${PG_HOST:-127.0.0.1}}"
pg_port="${RELEEM_PG_PORT:-${PG_PORT:-5432}}"
pg_ssl_mode=${RELEEM_PG_SSL_MODE:-${PG_SSL:-false}}
pg_restart_service="${RELEEM_PG_RESTART_SERVICE:-/bin/systemctl restart postgresql}"
pg_cnf_dir="${RELEEM_PG_CONF_DIR:-/etc/postgresql/releem.conf.d}"
releem_cnf_dir="/opt/releem/conf"
instance_type="${INSTANCE_TYPE:-local}"
aws_region="${AWS_REGION}"
aws_rds_db="${AWS_RDS_DB}"
aws_rds_parameter_group="${AWS_RDS_PARAMETER_GROUP}"
gcp_project_id="${RELEEM_GCP_PROJECT_ID}"
gcp_region="${RELEEM_GCP_REGION}"
gcp_cloudsql_instance="${RELEEM_GCP_CLOUDSQL_INSTANCE}"
gcp_cloudsql_public_connection=${RELEEM_GCP_CLOUDSQL_PUBLIC_CONNECTION:-false}
azure_subscription_id="${RELEEM_AZURE_SUBSCRIPTION_ID}"
azure_resource_group="${RELEEM_AZURE_RESOURCE_GROUP}"
azure_mysql_server="${RELEEM_AZURE_MYSQL_SERVER}"
env="${RELEEM_ENV:-prod}"
debug=${RELEEM_DEBUG:-false}
query_optimization=${RELEEM_QUERY_OPTIMIZATION:-false}
databases_query_optimization="${RELEEM_DATABASES_QUERY_OPTIMIZATION}"
releem_region="${RELEEM_REGION}"
EOF


echo -e "### This configuration was recommended by Releem. https://releem.com\n[mysqld]\nperformance_schema = 1\nslow_query_log = 1" > "/etc/mysql/releem.conf.d/collect_metrics.cnf"
if [ -n "$RELEEM_QUERY_OPTIMIZATION" -a "$RELEEM_QUERY_OPTIMIZATION" = true ]; then       
    echo "performance-schema-consumer-events-statements-history = ON" | tee -a "/etc/mysql/releem.conf.d/collect_metrics.cnf" >/dev/null
    echo "performance-schema-consumer-events-statements-current = ON" | tee -a "/etc/mysql/releem.conf.d/collect_metrics.cnf" >/dev/null
fi

/opt/releem/releem-agent -f

exec "$@"
