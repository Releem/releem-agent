#!/usr/bin/env bats

load "helpers/load_mysqlconfigurer.sh"

setup() {
    REPO_ROOT="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
    CONFIGURER_SH="${REPO_ROOT}/releem.sh"

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
  echo "1.26.2"
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

@test "legacy configurer forwards arguments environment and exit status from any directory" {
    local scripts="${TEST_TMPDIR}/scripts with spaces"
    mkdir -p "$scripts"
    cp "${REPO_ROOT}/mysqlconfigurer.sh" "$scripts/mysqlconfigurer.sh"
    cat > "$scripts/releem.sh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$RELEEM_FORWARD_TEST" "$@"
exit 23
EOF
    run env RELEEM_FORWARD_TEST=preserved bash "$scripts/mysqlconfigurer.sh" -s "value with spaces" -r
    [ "$status" -eq 23 ]
    [ "${lines[0]}" = preserved ]
    [ "${lines[1]}" = -s ]
    [ "${lines[2]}" = "value with spaces" ]
    [ "${lines[3]}" = -r ]
}

@test "legacy standalone download bootstraps canonical configurer only once" {
    local scripts="${TEST_TMPDIR}/standalone"
    mkdir -p "$scripts"
    cp "${REPO_ROOT}/mysqlconfigurer.sh" "$scripts/mysqlconfigurer.sh"
    create_mock_cmd "curl" '
echo download >> "${BOOTSTRAP_LOG}"
while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then out="$2"; shift 2; else shift; fi
done
printf '\''#!/usr/bin/env bash\nprintf "canonical:%%s\\n" "$*"\n'\'' > "$out"
'
    for invocation in 1 2; do
        PATH="${MOCK_BIN}:${PATH}" run env BOOTSTRAP_LOG="${TEST_TMPDIR}/bootstrap.log" bash "$scripts/mysqlconfigurer.sh" -u
        [ "$status" -eq 0 ]
        [ "$output" = canonical:-u ]
    done
    [ "$(wc -l < "${TEST_TMPDIR}/bootstrap.log")" -eq 1 ]
    [ -x "$scripts/releem.sh" ]
}

@test "legacy bootstrap rejects failed or invalid downloads" {
    local scripts="${TEST_TMPDIR}/standalone"
    mkdir -p "$scripts"
    cp "${REPO_ROOT}/mysqlconfigurer.sh" "$scripts/mysqlconfigurer.sh"
    create_mock_cmd "curl" 'exit 22'
    PATH="${MOCK_BIN}:${PATH}" run bash "$scripts/mysqlconfigurer.sh" -u
    [ "$status" -ne 0 ]
    [ ! -e "$scripts/releem.sh" ]
    create_mock_cmd "curl" '
while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then out="$2"; shift 2; else shift; fi
done
printf "if invalid\n" > "$out"
'
    PATH="${MOCK_BIN}:${PATH}" run bash "$scripts/mysqlconfigurer.sh" -u
    [ "$status" -ne 0 ]
    [ ! -e "$scripts/releem.sh" ]
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
# - /opt/releem/releem.sh -u
# - bash /opt/releem/releem.sh -s initial
# - bash /opt/releem/releem.sh -s auto
# - /bin/bash /opt/releem/releem.sh -r
# - /opt/releem/releem.sh -p (with query_optimization=true)
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

@test "apply via agent/cron (-s auto) reports a failed job request" {
    write_mysql_conf
    printf '#!/bin/sh\necho "task request failed" >&2\nexit 1\n' > "${TEST_WORKDIR}/releem-agent"
    chmod +x "${TEST_WORKDIR}/releem-agent"
    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        bash "${CONFIGURER_SH}" -s auto

    [ "$status" -eq 1 ]
    [[ "$output" == *"Failed to create the job to apply the configuration"* ]]
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

@test "automatic apply uses prepared dynamic file without downloading" {
    write_mysql_conf "aws/rds"
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    cat > "${TEST_CONF_DIR}/z_aiops_mysql.cnf" <<'EOF'
[mysqld]
max_connections=200
EOF
    cat > "${TEST_WORKDIR}/releem-agent" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "${TEST_CALLS_LOG}"
exit 0
EOF
    chmod +x "${TEST_WORKDIR}/releem-agent"

    run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=0 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -s automatic dynamic

    [ "$status" -eq 0 ]
    run grep -E -- "(^| )-c( |$)" "${TEST_CALLS_LOG}"
    [ "$status" -eq 1 ]
    run grep -F "max_connections=200" "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    [ "$status" -eq 0 ]
}

@test "automatic apply without restart deletes the backup on success" {
    write_mysql_conf "aws/rds"
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    printf '#!/usr/bin/env bash\nexit 0\n' > "${TEST_WORKDIR}/releem-agent"
    chmod +x "${TEST_WORKDIR}/releem-agent"
    printf '[mysqld]\nmax_connections=100\n' > "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    printf '[mysqld]\nmax_connections=200\n' > "${TEST_CONF_DIR}/z_aiops_mysql.cnf"
    apply_without_restart() {
        run env \
            PATH="${MOCK_BIN}:${PATH}" \
            RELEEM_TEST_MODE=1 \
            RELEEM_RESTART_SERVICE=0 \
            RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
            RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
            mysqlcmd="${MOCK_BIN}/mariadb" \
            bash "${CONFIGURER_SH}" -s automatic dynamic
    }

    # Successful copying without restart completes the apply.
    apply_without_restart
    [ "$status" -eq 0 ]
    grep -qF "max_connections=200" "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    [ ! -e "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp" ]

    # A later successful apply also removes any older backup.
    printf '[mysqld]\nmax_connections=100\n' > "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp"
    printf '[mysqld]\nmax_connections=300\n' > "${TEST_CONF_DIR}/z_aiops_mysql.cnf"
    apply_without_restart
    [ "$status" -eq 0 ]
    grep -qF "max_connections=300" "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    [ ! -e "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp" ]
}

@test "initial config downloads fresh content before applying initial_config_mysql.cnf" {
    write_restart_scripts
    write_mysql_conf "aws/rds" "bash ${TEST_TMPDIR}/restart_ok.sh"
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    cat > "${TEST_CONF_DIR}/initial_config_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 300
EOF
    cat > "${TEST_DB_DIR}/initial_config_mysql.cnf" <<'EOF'
[mysqld]
max_connections = 100
EOF

    cat > "${TEST_WORKDIR}/releem-agent" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$TEST_AGENT_CALLS"
case "$1" in
    --initial) printf '[mysqld]\nmax_connections = 200\n' > "$RELEEM_CONF_DIR/initial_config_mysql.cnf" ;;
    --event=config_applied) exit 0 ;;
    *) exit 92 ;;
esac
EOF

    run env \
        TEST_AGENT_CALLS="${TEST_TMPDIR}/agent.calls" \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_RESTART_SERVICE=0 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        mysqlcmd="${MOCK_BIN}/mariadb" \
        bash "${CONFIGURER_SH}" -s initial

    [ "$status" -eq 0 ]
    [ "$(cat "${TEST_TMPDIR}/agent.calls")" = "$(printf '%s\n' --initial --event=config_applied)" ]
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
source "'"${REPO_ROOT}"'/releem.sh"
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

@test "unsupported MySQL reports the actual minimum version" {
    echo 5.6.7 > "${TEST_TMPDIR}/version"
    run bash -c 'RELEEM_TEST_MODE=1 source "$1"; DATABASE_TYPE=mysql; RELEEM_DB_VERSION_FILE="$2"; releem_apply_manual' _ "$CONFIGURER_SH" "${TEST_TMPDIR}/version"
    [ "$status" -eq 2 ]
    [[ "$output" == *"lower than 5.6.8"* ]]
}

@test "manual apply uses prepared file without invoking configuration download" {
    write_mysql_conf
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    printf '[mysqld]\nmax_connections=200\n' > "${TEST_CONF_DIR}/z_aiops_mysql.cnf"
    cp "${TEST_CONF_DIR}/z_aiops_mysql.cnf" "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    printf '#!/bin/sh\necho unexpected-download\nexit 99\n' > "${TEST_WORKDIR}/releem-agent"
    run env PATH="${MOCK_BIN}:${PATH}" RELEEM_TEST_MODE=1 RELEEM_RESTART_SERVICE=0 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        bash "${CONFIGURER_SH}" -a
    [ "$status" -eq 0 ]
    [[ "$output" == *"identical to the current configuration"* ]]
    [[ "$output" != *"unexpected-download"* ]]
}

@test "manual apply refuses missing prepared file" {
    write_mysql_conf
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    run env PATH="${MOCK_BIN}:${PATH}" RELEEM_TEST_MODE=1 RELEEM_RESTART_SERVICE=0 \
        RELEEM_CONF_DIR="${TEST_CONF_DIR}/" RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
        bash "${CONFIGURER_SH}" -a
    [ "$status" -eq 1 ]
    [[ "$output" == *"configuration was not found"* ]]
    [ ! -f "${TEST_DB_DIR}/z_aiops_mysql.cnf" ]
}


@test "manual and automatic apply consume the Go-downloaded file without fetching configuration" {
    write_restart_scripts
    write_mysql_conf
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    cat > "${TEST_WORKDIR}/releem-agent" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$TEST_AGENT_CALLS"
case "$1" in -c|--initial|-f) exit 91 ;; esac
exit 0
EOF
    local mode
    for mode in manual automatic; do
        printf '[mysqld]\nmax_connections=100\n' > "${TEST_DB_DIR}/z_aiops_mysql.cnf"
        printf '[mysqld]\nmax_connections=200\n' > "${TEST_CONF_DIR}/z_aiops_mysql.cnf"
        local -a arguments=(-a)
        [ "$mode" != automatic ] || arguments=(-s automatic dynamic)
        local restart_flag=0
        [ "$mode" != manual ] || restart_flag=1
        run env PATH="${MOCK_BIN}:${PATH}" RELEEM_TEST_MODE=1 RELEEM_RESTART_SERVICE="$restart_flag" \
            TEST_AGENT_CALLS="${TEST_TMPDIR}/agent.calls" RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
            RELEEM_CONF_FILE="${TEST_CONF_FILE}" bash "${CONFIGURER_SH}" "${arguments[@]}"
        [ "$status" -eq 0 ]
        grep -qx max_connections=200 "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    done
    ! grep -E '^(-c|--initial|-f)( |$)' "${TEST_TMPDIR}/agent.calls"
}

@test "apply refuses missing Go-downloaded configuration without changing the live file" {
    write_mysql_conf
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    printf '[mysqld]\nmax_connections=100\n' > "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    local mode
    for mode in manual automatic initial; do
        local -a arguments=(-a)
        [ "$mode" != automatic ] || arguments=(-s automatic)
        [ "$mode" != initial ] || arguments=(-s initial)
        run env PATH="${MOCK_BIN}:${PATH}" RELEEM_TEST_MODE=1 RELEEM_RESTART_SERVICE=0 \
            RELEEM_CONF_DIR="${TEST_CONF_DIR}/" RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
            bash "${CONFIGURER_SH}" "${arguments[@]}"
        [ "$status" -eq 1 ]
        if [ "$mode" = initial ]; then
            [[ "$output" == *"releem-agent --initial"* ]]
        fi
        grep -qx max_connections=100 "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    done
}

@test "failed initial download preserves live configuration despite stale initial content" {
    write_restart_scripts
    write_mysql_conf "aws/rds" "bash ${TEST_TMPDIR}/restart_ok.sh"
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    printf '[mysqld]\nmax_connections=300\n' > "${TEST_CONF_DIR}/initial_config_mysql.cnf"
    printf '[mysqld]\nmax_connections=100\n' > "${TEST_DB_DIR}/initial_config_mysql.cnf"
    printf '[mysqld]\nmax_connections=150\n' > "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    cp -R "${TEST_DB_DIR}" "${TEST_TMPDIR}/original-dbconf"
    cat > "${TEST_WORKDIR}/releem-agent" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$TEST_AGENT_CALLS"
exit 91
EOF
    run env PATH="${MOCK_BIN}:${PATH}" RELEEM_TEST_MODE=1 RELEEM_RESTART_SERVICE=1 \
        TEST_AGENT_CALLS="${TEST_TMPDIR}/agent.calls" RELEEM_CONF_DIR="${TEST_CONF_DIR}/" \
        RELEEM_CONF_FILE="${TEST_CONF_FILE}" bash "${CONFIGURER_SH}" -s initial
    [ "$status" -eq 1 ]
    [[ "$output" == *"Failed to download the initial configuration"* ]]
    [ "$(cat "${TEST_TMPDIR}/agent.calls")" = --initial ]
    diff -r "${TEST_TMPDIR}/original-dbconf" "${TEST_DB_DIR}"
    [ ! -s "${TEST_CALLS_LOG}" ]
}

@test "each apply backs up the current file and only success removes the backup" {
    write_restart_scripts
    write_mysql_conf "aws/rds" "bash ${TEST_TMPDIR}/restart_fail.sh"
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    printf '[mysqld]\nmax_connections=100\n' > "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    printf '[mysqld]\nmax_connections=200\n' > "${TEST_CONF_DIR}/z_aiops_mysql.cnf"
    printf 'older backup\n' > "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp"
    apply_or_rollback() {
        run env PATH="${MOCK_BIN}:${PATH}" RELEEM_TEST_MODE=1 RELEEM_RESTART_SERVICE=1 \
            RELEEM_CONF_DIR="${TEST_CONF_DIR}/" RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
            mysqlcmd="${MOCK_BIN}/mariadb" bash "${CONFIGURER_SH}" "$@"
    }
    apply_or_rollback -s automatic
    [ "$status" -eq 7 ]
    grep -qF 'max_connections=100' "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp"

    # An identical failed retry must restart and replace the backup.
    apply_or_rollback -a
    [ "$status" -eq 7 ]
    grep -qF 'max_connections=200' "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp"
    printf '[mysqld]\nmax_connections=300\n' > "${TEST_CONF_DIR}/z_aiops_mysql.cnf"
    apply_or_rollback -a
    [ "$status" -eq 7 ]
    grep -qF 'max_connections=200' "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp"

    apply_or_rollback -r
    [ "$status" -eq 7 ]
    cmp "${TEST_DB_DIR}/z_aiops_mysql.cnf" "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp"

    write_mysql_conf "aws/rds" "bash ${TEST_TMPDIR}/restart_ok.sh"
    apply_or_rollback -r
    [ "$status" -eq 0 ]
    grep -qF 'max_connections=200' "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    [ ! -e "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp" ]

    for mode in manual automatic; do
        printf '[mysqld]\n# %s\n' "$mode" > "${TEST_CONF_DIR}/z_aiops_mysql.cnf"
        if [ "$mode" = manual ]; then
            apply_or_rollback -a
        else
            apply_or_rollback -s automatic
        fi
        [ "$status" -eq 0 ]
        [ ! -e "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp" ]
    done

    # A rollback after a successful apply removes the Releem configuration.
    apply_or_rollback -r
    [ "$status" -eq 0 ]
    [ ! -e "${TEST_DB_DIR}/z_aiops_mysql.cnf" ]
}

@test "failed configuration copy preserves the current-file backup" {
    write_restart_scripts
    write_mysql_conf "aws/rds" "bash ${TEST_TMPDIR}/restart_ok.sh"
    echo "8.0.36" > "${TEST_CONF_DIR}/db_version"
    printf '[mysqld]\nmax_connections=100\n' > "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    printf '[mysqld]\nmax_connections=200\n' > "${TEST_CONF_DIR}/z_aiops_mysql.cnf"
    local real_cp
    real_cp="$(command -v cp)"
    export TEST_REAL_CP="$real_cp" TEST_STAGED_CONFIG="${TEST_CONF_DIR}/z_aiops_mysql.cnf"
    create_mock_cmd "cp" '
for arg in "$@"; do
    [ "$arg" != "$TEST_STAGED_CONFIG" ] || exit 1
done
exec "$TEST_REAL_CP" "$@"
'
    for apply in manual automatic; do
        local args=(-a)
        [ "$apply" != automatic ] || args=(-s automatic)
        run env PATH="${MOCK_BIN}:${PATH}" RELEEM_TEST_MODE=1 RELEEM_RESTART_SERVICE=1 \
            RELEEM_CONF_DIR="${TEST_CONF_DIR}/" RELEEM_CONF_FILE="${TEST_CONF_FILE}" \
            mysqlcmd="${MOCK_BIN}/mariadb" bash "${CONFIGURER_SH}" "${args[@]}"
        [ "$status" -ne 0 ]
        grep -qF 'max_connections=100' "${TEST_CONF_DIR}/z_aiops_mysql.cnf.bkp"
        grep -qF 'max_connections=100' "${TEST_DB_DIR}/z_aiops_mysql.cnf"
    done
}
