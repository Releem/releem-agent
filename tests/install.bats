#!/usr/bin/env bats

load "helpers/load_install.sh"

setup() {
    REPO_ROOT="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
    INSTALL_SH="${REPO_ROOT}/install.sh"
    TEST_TMPDIR="$(mktemp -d)"
    MOCK_BIN="${TEST_TMPDIR}/mock-bin"
    mkdir -p "${MOCK_BIN}"
}

teardown() {
    rm -rf "${TEST_TMPDIR}"
}

create_mock_cmd() {
    local cmd_name="$1"
    local body="$2"
    cat >"${MOCK_BIN}/${cmd_name}" <<EOF
#!/usr/bin/env bash
${body}
EOF
    chmod +x "${MOCK_BIN}/${cmd_name}"
}

prepare_common_install_mocks() {
    create_mock_cmd "arch" 'echo "x86_64"'
    create_mock_cmd "sudo" '"$@"'
    create_mock_cmd "lsb_release" 'echo "Description: Ubuntu 24.04"'
    create_mock_cmd "crontab" 'exit 0'
    create_mock_cmd "hostname" 'echo "test-host"'
    create_mock_cmd "timeout" 'shift; "$@"'
    create_mock_cmd "pgrep" 'echo "1234"'
    create_mock_cmd "systemctl" 'exit 0'
    create_mock_cmd "apt-get" 'exit 0'
    create_mock_cmd "yum" 'exit 0'
    create_mock_cmd "dnf" 'exit 0'
    create_mock_cmd "curl" '
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ -n "$out" ]; then
  mkdir -p "$(dirname "$out")"
  if [[ "$out" == *"releem-agent"* ]]; then
    cat >"$out" <<'"'"'INNER'"'"'
#!/usr/bin/env bash
exit 0
INNER
    chmod +x "$out"
  elif [[ "$out" == *"mysqlconfigurer.sh"* ]]; then
    cat >"$out" <<'"'"'INNER'"'"'
#!/usr/bin/env bash
exit 0
INNER
    chmod +x "$out"
  else
    : >"$out"
  fi
fi
echo "200"
'
}

@test "detect_database_type defaults to mysql" {
    load_install_functions
    unset RELEEM_PG_HOST RELEEM_PG_LOGIN RELEEM_PG_PASSWORD RELEEM_PG_ROOT_PASSWORD
    unset RELEEM_MYSQL_HOST RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_MYSQL_ROOT_PASSWORD

    run detect_database_type
    [ "$status" -eq 0 ]
    [ "$database_type" = "mysql" ]
}

@test "detect_database_type prefers postgresql if both groups set" {
    load_install_functions
    RELEEM_PG_HOST="127.0.0.1"
    RELEEM_MYSQL_HOST="127.0.0.1"

    run detect_database_type
    [ "$status" -eq 0 ]
    [ "$database_type" = "postgresql" ]
}

@test "setup_mysql_connection_string builds host and default port" {
    load_install_functions
    unset RELEEM_MYSQL_PORT
    RELEEM_MYSQL_HOST="10.1.2.3"

    run setup_mysql_connection_string
    [ "$status" -eq 0 ]
    [[ "$connection_string" == *"--host=10.1.2.3"* ]]
    [[ "$connection_string" == *"--port=3306"* ]]
    [ "$mysql_user_host" = "%" ]
}

@test "setup_mysql_connection_string uses socket and localhost host marker" {
    load_install_functions
    local sock="${TEST_TMPDIR}/mysql.sock"
    : >"$sock"
    RELEEM_MYSQL_HOST="$sock"

    run setup_mysql_connection_string
    [ "$status" -eq 0 ]
    [[ "$connection_string" == *"--socket=${sock}"* ]]
    [ "$mysql_user_host" = "localhost" ]
}

@test "setup_postgresql_connection_string enables peer mode without root password" {
    load_install_functions
    unset RELEEM_PG_ROOT_PASSWORD
    RELEEM_PG_HOST="10.2.0.3"
    RELEEM_PG_PORT="5433"

    run setup_postgresql_connection_string
    [ "$status" -eq 0 ]
    [[ "$pg_connection_string" == *"-h 10.2.0.3"* ]]
    [[ "$pg_connection_string" == *"-p 5433"* ]]
    [ "$pg_root_peer_connection" = "sudo -u postgres " ]
}

@test "detect_instance_type defaults to local and accepts override" {
    load_install_functions
    unset RELEEM_INSTANCE_TYPE
    detect_instance_type
    [ "$instance_type" = "local" ]

    RELEEM_INSTANCE_TYPE="aws/rds"
    detect_instance_type
    [ "$instance_type" = "aws/rds" ]
}

@test "configure_connection_parameters dispatches by database_type" {
    load_install_functions
    called_mysql=0
    called_pg=0
    detect_mysql_commands() { called_mysql=1; }
    setup_mysql_connection_string() { :; }
    detect_postgresql_commands() { called_pg=1; }
    setup_postgresql_connection_string() { :; }

    database_type="mysql"
    configure_connection_parameters
    [ "$called_mysql" -eq 1 ]
    [ "$called_pg" -eq 0 ]

    called_mysql=0
    called_pg=0
    database_type="postgresql"
    configure_connection_parameters
    [ "$called_mysql" -eq 0 ]
    [ "$called_pg" -eq 1 ]
}

@test "setting_up_database_instance dispatches only for local instances" {
    load_install_functions
    mysql_setup_called=0
    pg_setup_called=0
    setting_up_local_mysql_instance() { mysql_setup_called=1; }
    setting_up_local_postgresql_instance() { pg_setup_called=1; }

    instance_type="local"
    database_type="mysql"
    setting_up_database_instance
    [ "$mysql_setup_called" -eq 1 ]
    [ "$pg_setup_called" -eq 0 ]

    mysql_setup_called=0
    pg_setup_called=0
    instance_type="local"
    database_type="postgresql"
    setting_up_database_instance
    [ "$mysql_setup_called" -eq 0 ]
    [ "$pg_setup_called" -eq 1 ]

    mysql_setup_called=0
    pg_setup_called=0
    instance_type="aws/rds"
    database_type="mysql"
    setting_up_database_instance
    [ "$mysql_setup_called" -eq 0 ]
    [ "$pg_setup_called" -eq 0 ]
}

@test "detect_mysql_service picks mariadb restart command via systemctl" {
    load_install_functions
    sudo_cmd=""
    create_mock_cmd "systemctl" '
if [ "$1" = "status" ] && [ "$2" = "mariadb" ]; then
  exit 0
fi
exit 1
'
    PATH="${MOCK_BIN}:${PATH}"

    run detect_mysql_service
    [ "$status" -eq 0 ]
    [[ "$service_name_cmd" == *"systemctl restart mariadb"* ]]
}

@test "detect_postgresql_service picks default postgresql service" {
    load_install_functions
    sudo_cmd=""
    create_mock_cmd "systemctl" '
if [ "$1" = "status" ] && [ "$2" = "postgresql" ]; then
  exit 0
fi
exit 1
'
    PATH="${MOCK_BIN}:${PATH}"

    run detect_postgresql_service
    [ "$status" -eq 0 ]
    [[ "$pg_service_name_cmd" == *"systemctl restart postgresql"* ]]
}

@test "enable_collect_queries passes restart approval to postgresql configurer" {
    load_install_functions
    cat >"${TEST_TMPDIR}/capture_configurer_env.sh" <<'EOF'
#!/usr/bin/env bash
if [ "${RELEEM_RESTART_SERVICE:-}" != "1" ]; then
  exit 42
fi
echo "RELEEM_RESTART_SERVICE=${RELEEM_RESTART_SERVICE:-}" >"${CONFIGURER_ENV_LOG}"
echo "args=$*" >>"${CONFIGURER_ENV_LOG}"
EOF
    chmod +x "${TEST_TMPDIR}/capture_configurer_env.sh"

    run env -u RELEEM_RESTART_SERVICE \
        CONFIGURER_ENV_LOG="${TEST_TMPDIR}/configurer.env" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            instance_type="local"
            database_type="postgresql"
            FLAG_PG_STAT_STATEMENTS=1
            sudo_cmd=""
            RELEEM_COMMAND="bash $2"
            enable_collect_queries
        ' _ "${INSTALL_SH}" "${TEST_TMPDIR}/capture_configurer_env.sh"

    [ "$status" -eq 0 ]
    run grep -E "^RELEEM_RESTART_SERVICE=1$" "${TEST_TMPDIR}/configurer.env"
    [ "$status" -eq 0 ]
    run grep -E "^args=-p$" "${TEST_TMPDIR}/configurer.env"
    [ "$status" -eq 0 ]
}

@test "create_postgresql_user validates pg_stat_statements for existing credentials" {
    load_install_functions
    create_mock_cmd "psql" '
query="$*"
if [[ "$query" == *"SELECT VERSION()"* ]]; then
  exit 0
elif [[ "$query" == *"pg_extension WHERE extname = '"'"'pg_stat_statements'"'"'"* ]]; then
  echo "1"
  exit 0
elif [[ "$query" == *"pg_extension LIMIT 1"* ]]; then
  exit 0
elif [[ "$query" == *"pg_roles LIMIT 1"* ]]; then
  exit 0
elif [[ "$query" == *"pg_auth_members LIMIT 1"* ]]; then
  exit 0
elif [[ "$query" == *"pg_hba_file_rules LIMIT 1"* ]]; then
  exit 0
elif [[ "$query" == *"has_schema_privilege"* ]]; then
  exit 0
elif [[ "$query" == *"relrowsecurity"* ]]; then
  exit 0
fi
exit 0
'
    run env -u FLAG_PG_STAT_STATEMENTS \
        PATH="${MOCK_BIN}:${PATH}" \
        FLAG_LOG="${TEST_TMPDIR}/pg-stat-flag.log" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            RELEEM_PG_LOGIN="releem"
            RELEEM_PG_PASSWORD="pwd"
            create_postgresql_user
            echo "FLAG_PG_STAT_STATEMENTS=${FLAG_PG_STAT_STATEMENTS:-}" >"${FLAG_LOG}"
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql"

    [ "$status" -eq 0 ]
    run grep -E "^FLAG_PG_STAT_STATEMENTS=1$" "${TEST_TMPDIR}/pg-stat-flag.log"
    [ "$status" -eq 0 ]
}

@test "create_mysql_user omits root password option when root password env is unset and root connects" {
    load_install_functions
    set -e
    create_mock_cmd "mysqladmin" '
printf "%s\n" "$*" >> "${MYSQLADMIN_ARGS_LOG}"
echo "mysqld is alive"
'
    create_mock_cmd "mysql" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
exit 0
'

    export MYSQLADMIN_ARGS_LOG="${TEST_TMPDIR}/mysqladmin.args"
    export MYSQL_ARGS_LOG="${TEST_TMPDIR}/mysql.args"
    mysqladmincmd="${MOCK_BIN}/mysqladmin"
    mysqlcmd="${MOCK_BIN}/mysql"
    root_connection_string="--host=127.0.0.1 --port=3306"
    connection_string="--host=127.0.0.1 --port=3306"
    mysql_user_host="127.0.0.1"
    unset RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_QUERY_OPTIMIZATION

    run create_mysql_user

    [ "$status" -eq 0 ] || return 1
    run grep -F -e "--user=root --password= ping" "${TEST_TMPDIR}/mysqladmin.args"
    [ "$status" -ne 0 ] || return 1
}

@test "Linux and Windows installers grant Group Replication membership access" {
    local grant="GRANT SELECT ON performance_schema.replication_group_members"

    run grep -F "$grant" "${INSTALL_SH}"
    [ "$status" -eq 0 ]

    run grep -F "$grant" "${REPO_ROOT}/windows/install.ps1"
    [ "$status" -eq 0 ]
}

@test "Windows test runner accepts the workflow OS option" {
    run bash "${REPO_ROOT}/tests/run_tests_windows.sh" --os windows-server-2022 --help

    [ "$status" -eq 0 ]
    [[ "$output" == *"Usage:"* ]]
}

@test "Linux and Windows installers grant version-compatible MariaDB replica monitoring access" {
    local current_grant="GRANT REPLICA MONITOR ON *.*"
    local compatibility_grant="GRANT REPLICATION SLAVE ADMIN ON *.*"

    run grep -F "$current_grant" "${INSTALL_SH}"
    [ "$status" -eq 0 ]
    run grep -F "$compatibility_grant" "${INSTALL_SH}"
    [ "$status" -eq 0 ]

    run grep -F "$current_grant" "${REPO_ROOT}/windows/install.ps1"
    [ "$status" -eq 0 ]
    run grep -F "$compatibility_grant" "${REPO_ROOT}/windows/install.ps1"
    [ "$status" -eq 0 ]
}

@test "create_mysql_user falls back to legacy MariaDB replica monitoring privilege" {
    load_install_functions
    set -e
    create_mock_cmd "mysqladmin" 'echo "mysqld is alive"'
    create_mock_cmd "mysql" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
if [[ "$*" == *"GRANT REPLICA MONITOR ON"* ]]; then
  exit 1
fi
exit 0
'

    export MYSQL_ARGS_LOG="${TEST_TMPDIR}/mysql.args"
    mysqladmincmd="${MOCK_BIN}/mysqladmin"
    mysqlcmd="${MOCK_BIN}/mysql"
    root_connection_string="--host=127.0.0.1 --port=3306"
    connection_string="--host=127.0.0.1 --port=3306"
    mysql_user_host="127.0.0.1"
    unset RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_QUERY_OPTIMIZATION

    run create_mysql_user

    [ "$status" -eq 0 ]
    run grep -F "GRANT REPLICA MONITOR ON *.*" "${TEST_TMPDIR}/mysql.args"
    [ "$status" -eq 0 ]
    run grep -F "GRANT REPLICATION SLAVE ADMIN ON *.*" "${TEST_TMPDIR}/mysql.args"
    [ "$status" -eq 0 ]
}

@test "create_mysql_user treats Group Replication membership grant as non-fatal" {
    load_install_functions
    set -e
    create_mock_cmd "mysqladmin" 'echo "mysqld is alive"'
    create_mock_cmd "mysql" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
if [[ "$*" == *"performance_schema.replication_group_members"* ]]; then
  exit 1
fi
exit 0
'

    export MYSQL_ARGS_LOG="${TEST_TMPDIR}/mysql.args"
    mysqladmincmd="${MOCK_BIN}/mysqladmin"
    mysqlcmd="${MOCK_BIN}/mysql"
    root_connection_string="--host=127.0.0.1 --port=3306"
    connection_string="--host=127.0.0.1 --port=3306"
    mysql_user_host="127.0.0.1"
    unset RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_QUERY_OPTIMIZATION

    run create_mysql_user

    [ "$status" -eq 0 ]
    run grep -F "GRANT SELECT ON performance_schema.replication_group_members" "${TEST_TMPDIR}/mysql.args"
    [ "$status" -eq 0 ]
}

@test "create_mysql_user keeps empty root password option when root password env is set to empty string" {
    load_install_functions
    set -e
    create_mock_cmd "mysqladmin" '
printf "%s\n" "$*" >> "${MYSQLADMIN_ARGS_LOG}"
echo "mysqld is alive"
'
    create_mock_cmd "mysql" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
exit 0
'

    export MYSQLADMIN_ARGS_LOG="${TEST_TMPDIR}/mysqladmin.args"
    export MYSQL_ARGS_LOG="${TEST_TMPDIR}/mysql.args"
    mysqladmincmd="${MOCK_BIN}/mysqladmin"
    mysqlcmd="${MOCK_BIN}/mysql"
    root_connection_string="--host=127.0.0.1 --port=3306"
    connection_string="--host=127.0.0.1 --port=3306"
    mysql_user_host="127.0.0.1"
    RELEEM_MYSQL_ROOT_PASSWORD=""
    unset RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_QUERY_OPTIMIZATION

    run create_mysql_user

    [ "$status" -eq 0 ] || return 1
    run head -n 1 "${TEST_TMPDIR}/mysqladmin.args"
    [ "$status" -eq 0 ] || return 1
    [[ "$output" == *"--password="* ]] || return 1
}

@test "create_mysql_user prompts for root password when env is unset and passwordless root fails" {
    load_install_functions
    set -e
    create_mock_cmd "mysqladmin" '
printf "%s\n" "$*" >> "${MYSQLADMIN_ARGS_LOG}"
if [[ "$*" == *"--user=root"* && "$*" != *"--password=promptedpwd"* ]]; then
  exit 1
fi
echo "mysqld is alive"
'
    create_mock_cmd "mysql" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
if [[ "$*" == *"--user=root"* && "$*" != *"--password=promptedpwd"* ]]; then
  exit 1
fi
exit 0
'

    export MYSQLADMIN_ARGS_LOG="${TEST_TMPDIR}/mysqladmin.args"
    export MYSQL_ARGS_LOG="${TEST_TMPDIR}/mysql.args"
    mysqladmincmd="${MOCK_BIN}/mysqladmin"
    mysqlcmd="${MOCK_BIN}/mysql"
    root_connection_string="--host=127.0.0.1 --port=3306"
    connection_string="--host=127.0.0.1 --port=3306"
    mysql_user_host="127.0.0.1"
    unset RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_QUERY_OPTIMIZATION

    run create_mysql_user <<< "promptedpwd"

    [ "$status" -eq 0 ] || return 1
    run grep -F -e "--user=root --password=promptedpwd ping" "${TEST_TMPDIR}/mysqladmin.args"
    [ "$status" -eq 0 ] || return 1
    run grep -F -e "--user=root --password=promptedpwd -Be CREATE USER" "${TEST_TMPDIR}/mysql.args"
    [ "$status" -eq 0 ] || return 1
}

@test "create_mysql_user keeps prompting until entered root password works" {
    load_install_functions
    set -e
    create_mock_cmd "mysqladmin" '
printf "%s\n" "$*" >> "${MYSQLADMIN_ARGS_LOG}"
if [[ "$*" == *"--user=root"* && "$*" != *"--password=promptedpwd"* ]]; then
  exit 1
fi
echo "mysqld is alive"
'
    create_mock_cmd "mysql" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
if [[ "$*" == *"--user=root"* && "$*" != *"--password=promptedpwd"* ]]; then
  exit 1
fi
exit 0
'

    export MYSQLADMIN_ARGS_LOG="${TEST_TMPDIR}/mysqladmin.args"
    export MYSQL_ARGS_LOG="${TEST_TMPDIR}/mysql.args"
    mysqladmincmd="${MOCK_BIN}/mysqladmin"
    mysqlcmd="${MOCK_BIN}/mysql"
    root_connection_string="--host=127.0.0.1 --port=3306"
    connection_string="--host=127.0.0.1 --port=3306"
    mysql_user_host="127.0.0.1"
    unset RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_QUERY_OPTIMIZATION

    run create_mysql_user <<< "wrongpwd
promptedpwd"

    [ "$status" -eq 0 ] || return 1
    run grep -F -e "--user=root --password=promptedpwd ping" "${TEST_TMPDIR}/mysqladmin.args"
    [ "$status" -eq 0 ] || return 1
    run grep -F -e "--user=root --password=promptedpwd -Be CREATE USER" "${TEST_TMPDIR}/mysql.args"
    [ "$status" -eq 0 ] || return 1
}

@test "detect_mysql_commands exits non-zero when mysql binaries are missing" {
    create_mock_cmd "which" 'exit 1'
    run env RELEEM_TEST_MODE=1 PATH="${MOCK_BIN}:/bin" bash -c "source '${INSTALL_SH}'; detect_mysql_commands"
    [ "$status" -ne 0 ]
    [[ "$output" == *"Couldn't find mysqladmin/mariadb-admin"* ]]
}

@test "aws/rds mode writes aws keys and releem_dir to releem.conf" {
    prepare_common_install_mocks
    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    mkdir -p "${workdir}"
    PATH="${MOCK_BIN}:${PATH}" run env \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        RELEEM_API_KEY="k1" \
        RELEEM_INSTANCE_TYPE="aws/rds" \
        RELEEM_AWS_REGION="eu-west-1" \
        RELEEM_AWS_RDS_DB="db-1" \
        RELEEM_AWS_RDS_PARAMETER_GROUP="releem-agent" \
        RELEEM_MYSQL_LOGIN="releem" \
        RELEEM_MYSQL_PASSWORD="pwd" \
        RELEEM_DB_MEMORY_LIMIT="0" \
        RELEEM_CRON_ENABLE="1" \
        RELEEM_AGENT_DISABLE="1" \
        bash "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    run grep -E "^(releem_dir|instance_type|aws_region|aws_rds_db|aws_rds_parameter_group)=" "${conf}"
    [ "$status" -eq 0 ]
    [[ "$output" == *"releem_dir=\"${workdir}\""* ]]
    [[ "$output" == *'instance_type="aws/rds"'* ]]
    [[ "$output" == *'aws_region="eu-west-1"'* ]]
}

@test "aws/rds mode fails when mandatory aws vars are missing" {
    prepare_common_install_mocks
    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    mkdir -p "${workdir}"
    PATH="${MOCK_BIN}:${PATH}" run env \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        RELEEM_API_KEY="k1" \
        RELEEM_INSTANCE_TYPE="aws/rds" \
        RELEEM_MYSQL_LOGIN="releem" \
        RELEEM_MYSQL_PASSWORD="pwd" \
        RELEEM_DB_MEMORY_LIMIT="0" \
        RELEEM_CRON_ENABLE="1" \
        RELEEM_AGENT_DISABLE="1" \
        bash "${INSTALL_SH}"

    [ "$status" -eq 1 ]
    [[ "$output" == *"AWS region, AWS RDS DB or AWS RDS Parameter Group is not set"* ]]
}

@test "gcp/cloudsql mode writes gcp keys and query optimization flag" {
    prepare_common_install_mocks
    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    mkdir -p "${workdir}"
    PATH="${MOCK_BIN}:${PATH}" run env \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        RELEEM_API_KEY="k1" \
        RELEEM_INSTANCE_TYPE="gcp/cloudsql" \
        RELEEM_GCP_PROJECT_ID="project-1" \
        RELEEM_GCP_REGION="us-central1" \
        RELEEM_GCP_CLOUDSQL_INSTANCE="inst-1" \
        RELEEM_MYSQL_LOGIN="releem" \
        RELEEM_MYSQL_PASSWORD="pwd" \
        RELEEM_QUERY_OPTIMIZATION="true" \
        RELEEM_DB_MEMORY_LIMIT="0" \
        RELEEM_CRON_ENABLE="1" \
        RELEEM_AGENT_DISABLE="1" \
        bash "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    run grep -E "^(instance_type|gcp_project_id|gcp_region|gcp_cloudsql_instance|query_optimization)=" "${conf}"
    [ "$status" -eq 0 ]
    [[ "$output" == *'instance_type="gcp/cloudsql"'* ]]
    [[ "$output" == *'query_optimization=true'* ]]
}

@test "azure/mysql mode writes azure keys and enables ssl by default" {
    prepare_common_install_mocks
    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    mkdir -p "${workdir}"
    PATH="${MOCK_BIN}:${PATH}" run env \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        RELEEM_API_KEY="k1" \
        RELEEM_INSTANCE_TYPE="azure/mysql" \
        RELEEM_AZURE_SUBSCRIPTION_ID="sub-1" \
        RELEEM_AZURE_RESOURCE_GROUP="rg-1" \
        RELEEM_AZURE_MYSQL_SERVER="mysql-1" \
        RELEEM_MYSQL_LOGIN="releem" \
        RELEEM_MYSQL_PASSWORD="pwd" \
        RELEEM_DB_MEMORY_LIMIT="0" \
        RELEEM_CRON_ENABLE="1" \
        RELEEM_AGENT_DISABLE="1" \
        bash "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    run grep -E "^(instance_type|azure_subscription_id|azure_resource_group|azure_mysql_server|mysql_ssl_mode)=" "${conf}"
    [ "$status" -eq 0 ]
    [[ "$output" == *'instance_type="azure/mysql"'* ]]
    [[ "$output" == *'azure_subscription_id="sub-1"'* ]]
    [[ "$output" == *'azure_resource_group="rg-1"'* ]]
    [[ "$output" == *'azure_mysql_server="mysql-1"'* ]]
    [[ "$output" == *'mysql_ssl_mode=true'* ]]
}

@test "azure/mysql mode fails when mandatory azure vars are missing" {
    prepare_common_install_mocks
    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    mkdir -p "${workdir}"
    PATH="${MOCK_BIN}:${PATH}" run env \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        RELEEM_API_KEY="k1" \
        RELEEM_INSTANCE_TYPE="azure/mysql" \
        RELEEM_MYSQL_LOGIN="releem" \
        RELEEM_MYSQL_PASSWORD="pwd" \
        RELEEM_DB_MEMORY_LIMIT="0" \
        RELEEM_CRON_ENABLE="1" \
        RELEEM_AGENT_DISABLE="1" \
        bash "${INSTALL_SH}"

    [ "$status" -eq 1 ]
    [[ "$output" == *"Azure subscription ID, resource group or MySQL server is not set"* ]]
}

@test "enable_query_optimization mode updates releem.conf and executes mysqlconfigurer" {
    prepare_common_install_mocks
    create_mock_cmd "mysqladmin" 'echo "mysqld is alive"'
    create_mock_cmd "mariadb-admin" 'echo "mysqld is alive"'
    create_mock_cmd "mysql" '
if [[ "$*" == *"-NBe"* ]]; then
  echo "GRANT SELECT on *.* to \`releem\`@\`%\`;"
fi
exit 0
'
    create_mock_cmd "mariadb" '
if [[ "$*" == *"-NBe"* ]]; then
  echo "GRANT SELECT on *.* to \`releem\`@\`%\`;"
fi
exit 0
'

    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    mkdir -p "${workdir}"
    printf 'apikey="k1"\n' > "${conf}"
    printf '#!/usr/bin/env bash\nexit 0\n' > "${workdir}/releem-agent"
    printf '#!/usr/bin/env bash\necho "$@" >> "%s/calls.log"\nexit 0\n' "${workdir}" > "${workdir}/mysqlconfigurer.sh"
    chmod +x "${workdir}/releem-agent" "${workdir}/mysqlconfigurer.sh"
    cp "${INSTALL_SH}" "${MOCK_BIN}/enable_query_optimization"
    chmod +x "${MOCK_BIN}/enable_query_optimization"

    PATH="${MOCK_BIN}:${PATH}" run env \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        RELEEM_API_KEY="k1" \
        RELEEM_MYSQL_ROOT_PASSWORD="rootpwd" \
        RELEEM_CRON_ENABLE="1" \
        enable_query_optimization

    [ "$status" -eq 0 ]
    run grep -E "^query_optimization=true$" "${conf}"
    [ "$status" -eq 0 ]
    run grep -E "^-p$" "${workdir}/calls.log"
    [ "$status" -eq 0 ]
}

@test "uninstall mode calls agent uninstall flow and rm with mocked side-effects" {
    prepare_common_install_mocks
    create_mock_cmd "rm" '
echo "$@" >> "${UNINSTALL_RM_LOG}"
exit 0
'

    local workdir="${TEST_TMPDIR}/workdir"
    local agent_calls="${TEST_TMPDIR}/agent.calls"
    local rm_calls="${TEST_TMPDIR}/rm.calls"
    mkdir -p "${workdir}"
    cat > "${workdir}/releem-agent" <<'EOF'
#!/usr/bin/env bash
echo "$@" >> "${UNINSTALL_AGENT_LOG}"
exit 0
EOF
    chmod +x "${workdir}/releem-agent"

    PATH="${MOCK_BIN}:${PATH}" run env \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_API_KEY="k1" \
        UNINSTALL_AGENT_LOG="${agent_calls}" \
        UNINSTALL_RM_LOG="${rm_calls}" \
        bash "${INSTALL_SH}" uninstall

    [ "$status" -eq 0 ]
    run grep -E -- "--event=agent_uninstall|stop|remove" "${agent_calls}"
    [ "$status" -eq 0 ]
    run grep -F -- "-rf ${workdir}" "${rm_calls}"
    [ "$status" -eq 0 ]
}

@test "update mode downloads artifacts and restarts agent with mocked side-effects" {
    prepare_common_install_mocks
    create_mock_cmd "curl" '
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
echo "$out" >> "${UPDATE_CURL_LOG}"
if [ -n "$out" ]; then
  mkdir -p "$(dirname "$out")"
  cat >"$out" <<'"'"'INNER'"'"'
#!/usr/bin/env bash
echo "$@" >> "${UPDATE_AGENT_LOG}"
exit 0
INNER
  chmod +x "$out"
fi
echo "200"
'

    local workdir="${TEST_TMPDIR}/workdir"
    local curl_calls="${TEST_TMPDIR}/curl.calls"
    local agent_calls="${TEST_TMPDIR}/agent.calls"
    mkdir -p "${workdir}"
    cat > "${workdir}/releem-agent" <<'EOF'
#!/usr/bin/env bash
echo "$@" >> "${UPDATE_AGENT_LOG}"
exit 0
EOF
    cat > "${workdir}/mysqlconfigurer.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "${workdir}/releem-agent" "${workdir}/mysqlconfigurer.sh"

    PATH="${MOCK_BIN}:${PATH}" run env \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_API_KEY="k1" \
        UPDATE_CURL_LOG="${curl_calls}" \
        UPDATE_AGENT_LOG="${agent_calls}" \
        bash "${INSTALL_SH}" update

    [ "$status" -eq 0 ]
    run grep -E "releem-agent.new|mysqlconfigurer.sh.new" "${curl_calls}"
    [ "$status" -eq 0 ]
    run grep -E "stop|start|-f" "${agent_calls}"
    [ "$status" -eq 0 ]
}
