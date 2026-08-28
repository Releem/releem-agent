#!/usr/bin/env bats

load "helpers/load_mysqlconfigurer.sh"

setup() {
    REPO_ROOT="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
    CONFIGURER_SH="${REPO_ROOT}/mysqlconfigurer.sh"

    TEST_TMPDIR="$(mktemp -d)"
    TEST_WORKDIR="${TEST_TMPDIR}"
    MOCK_BIN="${TEST_TMPDIR}/mock-bin"
    TEST_CONF_DIR="${TEST_WORKDIR}/conf"
    TEST_DB_DIR="${TEST_TMPDIR}/dbconf"
    TEST_CONF_FILE="${TEST_WORKDIR}/releem.conf"
    TEST_CALLS_LOG="${TEST_TMPDIR}/calls.log"

    mkdir -p "${MOCK_BIN}" "${TEST_CONF_DIR}" "${TEST_DB_DIR}"
    : > "${TEST_CALLS_LOG}"
    export RELEEM_WORKDIR="${TEST_WORKDIR}"
    cat > "${TEST_WORKDIR}/releem-agent" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "${TEST_WORKDIR}/releem-agent"

    create_mock_cmd "sudo" '"$@"'
    create_mock_cmd "curl" '
if [[ "$*" == *"current_version_agent"* ]]; then
  echo "1.25.1"
  exit 0
fi
echo "ok"
'
    create_mock_cmd "mariadb-admin" 'echo "mysqld is alive"'
    create_mock_cmd "mysqladmin" 'echo "mysqld is alive"'
    create_mock_cmd "mariadb" '
query="$*"
if [[ "$query" == *"performance_schema'"'"'"* ]]; then
  echo "performance_schema OFF"
elif [[ "$query" == *"slow_query_log'"'"'"* ]]; then
  echo "slow_query_log OFF"
elif [[ "$query" == *"performance_schema_digests_size'"'"'"* ]]; then
  echo "performance_schema_digests_size 10"
elif [[ "$query" == *"events_statements_current"* ]]; then
  echo "NO"
elif [[ "$query" == *"events_statements_history"* ]]; then
  echo "NO"
fi
'
    create_mock_cmd "mysql" 'exec "'"${MOCK_BIN}"'/mariadb" "$@"'
    create_mock_cmd "psql" '
query="$*"
if [[ "$query" == *"SHOW shared_preload_libraries"* ]]; then
  echo ""
elif [[ "$query" == *"SHOW pg_stat_statements.track"* ]]; then
  echo "top"
elif [[ "$query" == *"SHOW pg_stat_statements.max"* ]]; then
  echo "100"
fi
'
    create_mock_cmd "pg_isready" 'exit 0'
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

write_mysql_conf() {
    local instance_type="${1:-aws/rds}"
    local restart_cmd="${2:-bash ${TEST_TMPDIR}/restart_ok.sh}"
    cat >"${TEST_CONF_FILE}" <<EOF
apikey="k1"
instance_type="${instance_type}"
mysql_user="releem"
mysql_password="pwd"
mysql_host="127.0.0.1"
mysql_port="3306"
mysql_cnf_dir="${TEST_DB_DIR}"
mysql_restart_service="${restart_cmd}"
EOF
}

write_postgresql_conf() {
    local instance_type="${1:-aws/rds}"
    local restart_cmd="${2:-bash ${TEST_TMPDIR}/restart_ok.sh}"
    local pg_dir="${3:-${TEST_DB_DIR}}"
    cat >"${TEST_CONF_FILE}" <<EOF
apikey="k1"
instance_type="${instance_type}"
pg_user="releem"
pg_password="pwd"
pg_host="127.0.0.1"
pg_port="5432"
pg_database="postgres"
pg_cnf_dir="${pg_dir}"
pg_restart_service="${restart_cmd}"
EOF
}

write_restart_scripts() {
    cat >"${TEST_TMPDIR}/restart_ok.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    cat >"${TEST_TMPDIR}/restart_fail.sh" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
    chmod +x "${TEST_TMPDIR}/restart_ok.sh" "${TEST_TMPDIR}/restart_fail.sh"
}

#
# Documented command mapping:
# - /opt/releem/mysqlconfigurer.sh -u
# - bash /opt/releem/mysqlconfigurer.sh -s initial
# - bash /opt/releem/mysqlconfigurer.sh -s auto
# - /bin/bash /opt/releem/mysqlconfigurer.sh -r
# - /opt/releem/mysqlconfigurer.sh -p (with query_optimization=true)
#

@test "manual update command (-u) succeeds when already up-to-date" {
    write_mysql_conf
    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        bash "${CONFIGURER_SH}" -u

    [ "$status" -eq 0 ]
}

@test "manual update command (-u) executes downloaded installer when newer version exists" {
    write_mysql_conf
    local marker="${TEST_TMPDIR}/install-ran.marker"
    create_mock_cmd "curl" '
if [[ "$*" == *"current_version_agent"* ]]; then
  echo "9.9.9"
  exit 0
fi
cat <<'"'"'INNER'"'"'
#!/usr/bin/env bash
echo "ok" > "'"${marker}"'"
exit 0
INNER
'

    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        bash "${CONFIGURER_SH}" -u

    [ "$status" -eq 0 ]
    [ -f "${marker}" ]
}

@test "apply via agent/cron (-s auto) exits successfully" {
    write_mysql_conf
    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        bash "${CONFIGURER_SH}" -s auto

    [ "$status" -eq 0 ]
    [[ "$output" == *"Sending request to create a job"* ]]
}

@test "initial config command (-s initial) applies initial_config_mysql.cnf" {
    write_restart_scripts
    write_mysql_conf "aws/rds" "bash ${TEST_TMPDIR}/restart_ok.sh"
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    cat > "${TEST_CONF_DIR}/initial_config_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 200
EOF
    cat > "${TEST_DB_DIR}/initial_config_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 100
EOF

    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=0 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -s initial

    [ "$status" -eq 0 ]
    run grep -E "max_connections = 200" "${TEST_DB_DIR}/initial_config_mysql.cnf"
    [ "$status" -eq 0 ]
}

@test "rollback command (-r) returns exit code 5 when restart disabled" {
    write_mysql_conf
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=0 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -r

    [ "$status" -eq 5 ]
}

@test "query optimization flow (-p) writes consumer options to collect_metrics.cnf" {
    write_mysql_conf
    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=0 \
        RELEEM_QUERY_OPTIMIZATION=true \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -p

    [ "$status" -eq 0 ]
    run grep -E "performance_schema = 1|performance-schema-consumer-events-statements-history = ON|performance-schema-consumer-events-statements-current = ON" "${TEST_DB_DIR}/collect_metrics.cnf"
    [ "$status" -eq 0 ]
}

@test "apply manual (-a) returns 1 when recommended config is missing" {
    write_mysql_conf
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"

    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=1 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -a

    [ "$status" -eq 1 ]
}

@test "apply manual (-a) returns 2 when MySQL version is below supported minimum" {
    write_mysql_conf
    echo "5.5.0" > "${TEST_CONF_DIR}/db_version"
    cat > "${TEST_CONF_DIR}/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 200
EOF

    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=1 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -a

    [ "$status" -eq 2 ]
}

@test "apply manual (-a) returns 3 when DB config directory does not exist" {
    write_mysql_conf "aws/rds" "bash ${TEST_TMPDIR}/restart_ok.sh"
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    cat > "${TEST_CONF_FILE}" <<EOF
apikey="k1"
instance_type="aws/rds"
mysql_user="releem"
mysql_password="pwd"
mysql_host="127.0.0.1"
mysql_port="3306"
mysql_cnf_dir="${TEST_TMPDIR}/missing-dir"
mysql_restart_service="bash ${TEST_TMPDIR}/restart_ok.sh"
EOF
    cat > "${TEST_CONF_DIR}/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 200
EOF

    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=1 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -a

    [ "$status" -eq 3 ]
}

@test "apply manual (-a) returns 4 when restart command is missing" {
    cat >"${TEST_CONF_FILE}" <<EOF
apikey="k1"
instance_type="aws/rds"
mysql_user="releem"
mysql_password="pwd"
mysql_host="127.0.0.1"
mysql_port="3306"
mysql_cnf_dir="${TEST_DB_DIR}"
EOF
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    cat > "${TEST_CONF_DIR}/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 200
EOF
    cat > "${TEST_DB_DIR}/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 100
EOF

    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=1 \
        RELEEM_COMMAND_RESTART_SERVICE="" \
        RELEEM_MYSQL_RESTART_SERVICE="" \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -a

    [ "$status" -eq 4 ]
}

@test "apply manual (-a) returns 7 when service restart command fails" {
    write_restart_scripts
    write_mysql_conf "aws/rds" "bash ${TEST_TMPDIR}/restart_fail.sh"
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    cat > "${TEST_CONF_DIR}/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 200
EOF
    cat > "${TEST_DB_DIR}/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 100
EOF

    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=1 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -a

    [ "$status" -eq 7 ]
}

@test "apply manual (-a) returns 11 when !includedir directive is missing in main config" {
    write_mysql_conf
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    cat > "${TEST_CONF_DIR}/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 200
EOF
    cat > "${TEST_TMPDIR}/my.cnf" <<'EOF'
[mysqld]
user=mysql
EOF

    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=1 \
        MYSQL_MY_CNF_PATH="${TEST_TMPDIR}/my.cnf" \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -a

    [ "$status" -eq 11 ]
}

@test "PostgreSQL detection exits 1 when psql is missing in local mode" {
    write_postgresql_conf "local" "bash ${TEST_TMPDIR}/restart_ok.sh"
    create_mock_cmd "which" '
if [ "$1" = "psql" ]; then
  exit 1
fi
command -v "$1"
'
    run env \
        PATH="${MOCK_BIN}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        /bin/bash "${CONFIGURER_SH}" -p

    [ "$status" -eq 1 ]
    [[ "$output" == *"Couldn't find psql"* ]]
}

@test "PostgreSQL configure (-p) writes collect_metrics.conf and exits 0 with restart disabled" {
    write_postgresql_conf
    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=0 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        psqlcmd="${MOCK_BIN}/psql" \
        bash "${CONFIGURER_SH}" -p

    [ "$status" -eq 0 ]
    run grep -E "shared_preload_libraries = 'pg_stat_statements'|pg_stat_statements.track = all|pg_stat_statements.max = 10000" "${TEST_DB_DIR}/collect_metrics.conf"
    [ "$status" -eq 0 ]
}

@test "PostgreSQL configure (-p) returns 3 when configuration directory is missing" {
    write_postgresql_conf "aws/rds" "bash ${TEST_TMPDIR}/restart_ok.sh" "${TEST_TMPDIR}/missing-pg-dir"
    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=0 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        psqlcmd="${MOCK_BIN}/psql" \
        bash "${CONFIGURER_SH}" -p

    [ "$status" -eq 3 ]
}

@test "apply manual handles restart timeout branch with exit code 6" {
    local func_tmp="${TEST_TMPDIR}/func-timeout"
    mkdir -p "${func_tmp}/conf" "${func_tmp}/db"
    cat > "${func_tmp}/conf/db_version" <<'EOF'
8.0.36
EOF
    cat > "${func_tmp}/conf/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 200
EOF
    cat > "${func_tmp}/db/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 100
EOF

    run bash -c '
RELEEM_TEST_MODE=1
source "'"${REPO_ROOT}"'/mysqlconfigurer.sh"
wait_restart() { return 6; }
DATABASE_TYPE="mysql"
DATABASE_NAME="MySQL"
RELEEM_CONF_DIR="'"${func_tmp}"'/conf/"
RELEEM_DB_VERSION_FILE="'"${func_tmp}"'/conf/db_version"
RELEEM_DB_CONFIG_DIR="'"${func_tmp}"'/db"
RELEEM_DB_CONFIG_FILE_NAME="z_aiops_mysql.cnf"
RELEEM_DB_CONFIG_FILE="'"${func_tmp}"'/conf/z_aiops_mysql.cnf"
RELEEM_COMMAND_RESTART_SERVICE="true"
RELEEM_RESTART_SERVICE=1
MYSQL_MY_CNF_PATH=""
releem_apply_manual
'
    [ "$status" -eq 6 ]
}
