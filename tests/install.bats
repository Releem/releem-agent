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
    unset RELEEM_PG_HOST RELEEM_PG_LOGIN RELEEM_PG_PASSWORD RELEEM_PG_ROOT_PASSWORD RELEEM_PG_ROOT_LOGIN RELEEM_PG_TYPE
    unset RELEEM_MYSQL_HOST RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_ROOT_LOGIN RELEEM_MYSQL_TYPE

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

@test "detect_database_type uses postgresql root login as postgresql signal" {
    load_install_functions
    unset RELEEM_PG_HOST RELEEM_PG_LOGIN RELEEM_PG_PASSWORD RELEEM_PG_ROOT_PASSWORD RELEEM_PG_TYPE
    unset RELEEM_MYSQL_HOST RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_ROOT_LOGIN RELEEM_MYSQL_TYPE
    RELEEM_PG_ROOT_LOGIN="pgadmin"

    run detect_database_type
    [ "$status" -eq 0 ]
    [ "$database_type" = "postgresql" ]
}

@test "detect_database_type uses postgresql type flag as postgresql signal" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        unset RELEEM_PG_HOST RELEEM_PG_LOGIN RELEEM_PG_PASSWORD RELEEM_PG_ROOT_PASSWORD RELEEM_PG_ROOT_LOGIN
        unset RELEEM_MYSQL_HOST RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_ROOT_LOGIN RELEEM_MYSQL_TYPE
        RELEEM_PG_TYPE="1"
        detect_database_type >/dev/null
        printf "%s" "$database_type"
    ' _ "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [ "$output" = "postgresql" ]
}

@test "detect_database_type uses mysql root login as mysql signal" {
    load_install_functions
    unset RELEEM_PG_HOST RELEEM_PG_LOGIN RELEEM_PG_PASSWORD RELEEM_PG_ROOT_PASSWORD RELEEM_PG_ROOT_LOGIN RELEEM_PG_TYPE
    unset RELEEM_MYSQL_HOST RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_TYPE
    RELEEM_MYSQL_ROOT_LOGIN="admin"

    run detect_database_type
    [ "$status" -eq 0 ]
    [ "$database_type" = "mysql" ]
}

@test "detect_database_type uses mysql type flag as mysql signal" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        unset RELEEM_PG_HOST RELEEM_PG_LOGIN RELEEM_PG_PASSWORD RELEEM_PG_ROOT_PASSWORD RELEEM_PG_ROOT_LOGIN RELEEM_PG_TYPE
        unset RELEEM_MYSQL_HOST RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_ROOT_LOGIN
        RELEEM_MYSQL_TYPE="1"
        detect_database_type
    ' _ "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [[ "$output" == *"Detected MySQL configuration."* ]]
}

@test "setup_mysql_connection_string builds host and default port" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        unset RELEEM_MYSQL_PORT
        RELEEM_MYSQL_HOST="10.1.2.3"
        setup_mysql_connection_string >/dev/null
        printf "conn=%s\nroot=%s\nhost=%s\n" "$connection_string" "$root_connection_string" "$mysql_user_host"
    ' _ "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [[ "$output" == *"conn= --host=10.1.2.3 --port=3306"* ]]
    [[ "$output" == *"root= --host=10.1.2.3"* ]]
    [[ "$output" == *"host=%"* ]]
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

@test "setup_postgresql_connection_string uses explicit empty root password when env is set" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        RELEEM_PG_ROOT_PASSWORD=""
        RELEEM_PG_HOST="10.2.0.3"
        RELEEM_PG_PORT="5433"
        setup_postgresql_connection_string >/dev/null
        printf "root=%s\npeer=%s\n" "$pg_root_connection_string" "$pg_root_peer_connection"
    ' _ "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [[ "$output" == *"root= -h 10.2.0.3 -p 5433 -d postgres"* ]]
    [[ "$output" == *"peer="* ]]
    [[ "$output" != *"peer=sudo -u postgres "* ]]
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

@test "configure_releem_agent writes postgresql ssl mode as an HCL boolean" {
    load_install_functions
    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    mkdir -p "${workdir}"

    database_type="postgresql"
    RELEEM_WORKDIR="${workdir}"
    RELEEM_CONF_FILE="${conf}"
    RELEEM_PG_SSL_MODE="false"
    apikey="k1"
    sudo_cmd=""
    unset PG_LOGIN PG_PASSWORD RELEEM_PG_HOST RELEEM_PG_PORT pg_service_name_cmd PG_CONF_DIR

    run configure_releem_agent

    [ "$status" -eq 0 ]
    run grep '^pg_ssl_mode=' "${conf}"
    [ "$status" -eq 0 ]
    [ "$output" = 'pg_ssl_mode=false' ] || return 1
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

@test "create_postgresql_user grants pg_read_all_data with default root login" {
    create_mock_cmd "psql" '
query="$*"
printf "PGPASSWORD=%s args=%s\n" "${PGPASSWORD-__unset__}" "$query" >> "${PG_ARGS_LOG}"
if [[ "$query" == *"SHOW server_version_num"* ]]; then
  echo "140000"
elif [[ "$query" == *"SELECT datname"* && "$query" == *"pg_database"* ]]; then
  printf "postgres\000shop\000"
elif [[ "$query" == *"GRANT CONNECT ON DATABASE"* ]]; then
  cat >/dev/null
elif [[ "$query" == *"pg_extension WHERE extname = '"'"'pg_stat_statements'"'"'"* ]]; then
  echo "1"
fi
exit 0
'

    run env \
        RELEEM_PG_ROOT_PASSWORD="rootpwd" \
        RELEEM_QUERY_OPTIMIZATION="true" \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            pg_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            unset RELEEM_PG_LOGIN RELEEM_PG_PASSWORD
            create_postgresql_user
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql"

    [ "$status" -eq 0 ]
    run grep -F -- '-v ON_ERROR_STOP=1 -c GRANT CONNECT ON DATABASE "postgres" TO "releem";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -F -- '-v ON_ERROR_STOP=1 -c GRANT CONNECT ON DATABASE "shop" TO "releem";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -F -- '-v ON_ERROR_STOP=1 -c GRANT pg_read_all_data TO "releem";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    for procedural_sql in 'DO $$' 'EXECUTE format' 'set_config' 'decode(' 'ALTER DEFAULT PRIVILEGES'; do
        run grep -F -- "${procedural_sql}" "${TEST_TMPDIR}/pg.args"
        [ "$status" -ne 0 ]
    done
}

@test "postgresql role ALTER boundary quotes identifier and binds exact password" {
    create_mock_cmd "psql" '
for argument in "$@"; do
  printf "%s\n" "$argument" >> "${PG_ARGS_LOG}"
done
query="${!#}"
if [[ "$query" == *"FROM pg_roles"* ]]; then
  echo "1"
fi
exit 0
'

    local role='role"reader'
    local password="pa'ss word; --"
    run env \
        RELEEM_PG_ROOT_PASSWORD="rootpwd" \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            create_or_update_postgresql_monitoring_role "postgres" "$3" "$4"
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql" "${role}" "${password}"

    [ "$status" -eq 0 ]
    run grep -Fx -- "role_name=${role}" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -Fx -- "SELECT 1 FROM pg_roles WHERE rolname = :'role_name';" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -Fxc -- "role_password=${password}" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    [ "$output" -eq 1 ]
    run grep -Fx -- 'ALTER USER "role""reader" WITH PASSWORD :'"'"'role_password'"'"';' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    for grant in \
        'GRANT pg_monitor TO "role""reader";' \
        'GRANT SELECT ON pg_hba_file_rules TO "role""reader";' \
        'GRANT EXECUTE ON FUNCTION pg_hba_file_rules TO "role""reader";'; do
        run grep -Fx -- "${grant}" "${TEST_TMPDIR}/pg.args"
        [ "$status" -eq 0 ]
    done
    run grep -F -- "WITH PASSWORD '${password}'" "${TEST_TMPDIR}/pg.args"
    [ "$status" -ne 0 ]
    run grep -F -- "CREATE USER" "${TEST_TMPDIR}/pg.args"
    [ "$status" -ne 0 ]
}

@test "postgresql role CREATE boundary quotes identifier and binds exact password" {
    create_mock_cmd "psql" '
for argument in "$@"; do
  printf "%s\n" "$argument" >> "${PG_ARGS_LOG}"
done
exit 0
'

    local role='new"reader'
    local password="new'pass word; --"
    run env \
        RELEEM_PG_ROOT_PASSWORD="rootpwd" \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            create_or_update_postgresql_monitoring_role "postgres" "$3" "$4"
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql" "${role}" "${password}"

    [ "$status" -eq 0 ]
    run grep -Fx -- "role_name=${role}" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -Fx -- "SELECT 1 FROM pg_roles WHERE rolname = :'role_name';" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -Fxc -- "role_password=${password}" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    [ "$output" -eq 1 ]
    run grep -Fx -- 'CREATE USER "new""reader" WITH PASSWORD :'"'"'role_password'"'"';' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    for grant in \
        'GRANT pg_monitor TO "new""reader";' \
        'GRANT SELECT ON pg_hba_file_rules TO "new""reader";' \
        'GRANT EXECUTE ON FUNCTION pg_hba_file_rules TO "new""reader";'; do
        run grep -Fx -- "${grant}" "${TEST_TMPDIR}/pg.args"
        [ "$status" -eq 0 ]
    done
    run grep -F -- "WITH PASSWORD '${password}'" "${TEST_TMPDIR}/pg.args"
    [ "$status" -ne 0 ]
    run grep -F -- "ALTER USER" "${TEST_TMPDIR}/pg.args"
    [ "$status" -ne 0 ]
}

@test "PostgreSQL commands preserve psql executable and role arguments" {
    create_mock_cmd "psql with spaces" '
user=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -U) user="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf "user=<%s>\n" "$user" >> "${PG_ARGS_LOG}"
exit 0
'

    run env \
        RELEEM_PG_ROOT_PASSWORD="rootpwd" \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            pg_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            postgresql_root_exec "root user" -c "SELECT 1;" || true
            RELEEM_PG_LOGIN="monitor user"
            RELEEM_PG_PASSWORD="monitor-password"
            create_postgresql_user || true
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql with spaces"

    run grep -Fx -- "user=<root user>" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -Fx -- "user=<monitor user>" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
}

@test "postgresql_root_exec omits PGPASSWORD for root when root password env is unset" {
    create_mock_cmd "psql" '
printf "PGPASSWORD=%s args=%s\n" "${PGPASSWORD-__unset__}" "$*" >> "${PG_ARGS_LOG}"
exit 0
'

    run env -u RELEEM_PG_ROOT_PASSWORD \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-p 5432 -d postgres"
            postgresql_root_exec postgres -c "SELECT 1;"
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql"

    [ "$status" -eq 0 ]
    run grep -F "PGPASSWORD=__unset__" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
}

@test "postgresql_root_exec keeps empty PGPASSWORD when root password env is set to empty string" {
    create_mock_cmd "psql" '
printf "PGPASSWORD=%s args=%s\n" "${PGPASSWORD-__unset__}" "$*" >> "${PG_ARGS_LOG}"
exit 0
'

    run env \
        RELEEM_PG_ROOT_PASSWORD="" \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            postgresql_root_exec postgres -c "SELECT 1;"
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql"

    [ "$status" -eq 0 ]
    run grep -F "PGPASSWORD= args=-h 127.0.0.1 -p 5432 -d postgres -U postgres -c SELECT 1;" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
}

@test "create_postgresql_user uses custom root login" {
    create_mock_cmd "psql" '
user=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -U) user="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf "user=%s args=%s\n" "$user" "$*" >> "${PG_ARGS_LOG}"
exit 0
'

    run env \
        RELEEM_PG_ROOT_LOGIN="pgadmin" \
        RELEEM_PG_ROOT_PASSWORD="rootpwd" \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            pg_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            unset RELEEM_PG_LOGIN RELEEM_PG_PASSWORD
            create_postgresql_user
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql"

    [ "$status" -eq 0 ]
    run grep -F "user=pgadmin" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
}

@test "create_postgresql_user grants pg_read_all_data when query optimization is enabled" {
    create_mock_cmd "psql" '
printf "%s\n" "$*" >> "${PG_ARGS_LOG}"
if [[ "$*" == *"SHOW server_version_num"* ]]; then
  echo "140000"
elif [[ "$*" == *"SELECT datname"* && "$*" == *"pg_database"* ]]; then
  printf "postgres\000shop\000"
fi
exit 0
'

    run env \
        RELEEM_PG_ROOT_LOGIN="pgadmin" \
        RELEEM_PG_ROOT_PASSWORD="rootpwd" \
        RELEEM_QUERY_OPTIMIZATION="true" \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            pg_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            unset RELEEM_PG_LOGIN RELEEM_PG_PASSWORD
            create_postgresql_user
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql"

    [ "$status" -eq 0 ]
    run grep -F -- '-v ON_ERROR_STOP=1 -c GRANT CONNECT ON DATABASE "postgres" TO "releem";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -F -- '-v ON_ERROR_STOP=1 -c GRANT CONNECT ON DATABASE "shop" TO "releem";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -F -- '-v ON_ERROR_STOP=1 -c GRANT pg_read_all_data TO "releem";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
}

@test "postgresql 12 grants existing objects with explicit schema statements" {
    create_mock_cmd "psql" '
query="$*"
printf "%s\n" "$query" >> "${PG_ARGS_LOG}"
if [[ "$query" == *"SHOW server_version_num"* ]]; then
  echo "120000"
elif [[ "$query" == *"SELECT datname"* && "$query" == *"pg_database"* ]]; then
  printf "postgres\000shop\000"
elif [[ "$query" == *"SELECT nspname"* && "$query" == *"pg_namespace"* ]]; then
  printf "app\000Sales Data\000"
elif [[ "$query" == *"GRANT USAGE ON SCHEMA"* ]]; then
  cat >/dev/null
fi
exit 0
'

    run env \
        RELEEM_PG_ROOT_PASSWORD="rootpwd" \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            grant_postgresql_query_optimization_access "postgres" "releem"
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql"

    [ "$status" -eq 0 ]
    grant_output="$output"
    for database in postgres shop; do
        for grant in \
            "GRANT USAGE ON SCHEMA \"app\" TO \"releem\";" \
            "GRANT SELECT ON ALL TABLES IN SCHEMA \"app\" TO \"releem\";" \
            "GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA \"app\" TO \"releem\";" \
            "GRANT USAGE ON SCHEMA \"Sales Data\" TO \"releem\";" \
            "GRANT SELECT ON ALL TABLES IN SCHEMA \"Sales Data\" TO \"releem\";" \
            "GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA \"Sales Data\" TO \"releem\";"; do
            run grep -F -- "-d ${database} -v ON_ERROR_STOP=1 -c ${grant}" "${TEST_TMPDIR}/pg.args"
            [ "$status" -eq 0 ]
        done
    done
    run grep -F -- "GRANT pg_read_all_data" "${TEST_TMPDIR}/pg.args"
    [ "$status" -ne 0 ]
    for procedural_sql in 'DO $$' 'EXECUTE format' 'set_config' 'decode(' 'ALTER DEFAULT PRIVILEGES'; do
        run grep -F -- "${procedural_sql}" "${TEST_TMPDIR}/pg.args"
        [ "$status" -ne 0 ]
    done

    [[ "$grant_output" == *"rerun the installer after adding PostgreSQL schemas or objects"* ]]
}

@test "quote_postgresql_identifier escapes embedded double quotes" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        quote_postgresql_identifier '"'"'role"reader'"'"'
        printf "\\n"
        quote_postgresql_identifier '"'"'shop"db'"'"'
        printf "\\n"
        quote_postgresql_identifier '"'"'Sales "Data"'"'"'
    ' _ "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [ "$output" = $'"role""reader"\n"shop""db"\n"Sales ""Data"""' ]
}

@test "create_postgresql_user prompts for root password when env is unset and passwordless root fails" {
    create_mock_cmd "psql" '
query="$*"
user=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -U) user="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf "user=%s PGPASSWORD=%s query=%s\n" "$user" "${PGPASSWORD-__unset__}" "$query" >> "${PG_ARGS_LOG}"
if [ "$user" = "postgres" ] && [ "${PGPASSWORD-__unset__}" != "pgprompt" ]; then
  read -r _stolen_password || true
  exit 1
fi
if [[ "$query" == *"pg_extension WHERE extname = '"'"'pg_stat_statements'"'"'"* ]]; then
  echo "1"
fi
exit 0
'

    run env -u RELEEM_PG_ROOT_PASSWORD \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-p 5432 -d postgres"
            pg_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            unset RELEEM_PG_LOGIN RELEEM_PG_PASSWORD
            create_postgresql_user
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql" <<< "pgprompt"

    [ "$status" -eq 0 ]
    run head -n 1 "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    [[ "$output" == *"user=postgres PGPASSWORD=__unset__"* ]]
    run grep -F "user=postgres PGPASSWORD=pgprompt" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
}

@test "create_postgresql_user keeps prompting until entered root password works" {
    create_mock_cmd "psql" '
query="$*"
user=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -U) user="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf "user=%s PGPASSWORD=%s query=%s\n" "$user" "${PGPASSWORD-__unset__}" "$query" >> "${PG_ARGS_LOG}"
if [ "$user" = "postgres" ] && [ "${PGPASSWORD-__unset__}" != "pgprompt" ]; then
  exit 1
fi
if [[ "$query" == *"pg_extension WHERE extname = '"'"'pg_stat_statements'"'"'"* ]]; then
  echo "1"
fi
exit 0
'

    run env -u RELEEM_PG_ROOT_PASSWORD \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            set +e
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-p 5432 -d postgres"
            pg_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            unset RELEEM_PG_LOGIN RELEEM_PG_PASSWORD
            create_postgresql_user
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql" <<< "wrongpg
pgprompt"

    [ "$status" -eq 0 ]
    run grep -F "user=postgres PGPASSWORD=wrongpg" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -F "user=postgres PGPASSWORD=pgprompt" "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -F "user=postgres PGPASSWORD=wrongpg" "${TEST_TMPDIR}/pg.args"
    [ "${#lines[@]}" -eq 1 ]
}

@test "windows installer uses conditional MySQL root password args" {
    run grep -F '"-p$RootPassword"' "${REPO_ROOT}/windows/install.ps1"
    [ "$status" -ne 0 ]

    run grep -F "Get-MySQLRootArgs" "${REPO_ROOT}/windows/install.ps1"
    [ "$status" -eq 0 ]

    run grep -F "Prompt-MySQLRootPasswordUntilSuccess" "${REPO_ROOT}/windows/install.ps1"
    [ "$status" -eq 0 ]

    run grep -F "RELEEM_MYSQL_ROOT_LOGIN" "${REPO_ROOT}/windows/install.ps1"
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

@test "create_mysql_user uses custom root login" {
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
    RELEEM_MYSQL_ROOT_LOGIN="admin"
    unset RELEEM_MYSQL_ROOT_PASSWORD RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD RELEEM_QUERY_OPTIMIZATION

    run create_mysql_user

    [ "$status" -eq 0 ] || return 1
    run grep -F -- "--user=admin" "${TEST_TMPDIR}/mysqladmin.args"
    [ "$status" -eq 0 ] || return 1
    run grep -F -- "--user=admin" "${TEST_TMPDIR}/mysql.args"
    [ "$status" -eq 0 ] || return 1
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
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
if [[ "$*" == *"-NBe select Concat("*"from mysql.user where HEX(User)="* ]]; then
  echo "GRANT SELECT on *.* to \`releem\`@\`%\`;"
elif [[ "$*" == *"-Be GRANT SELECT on *.* to"* ]]; then
  exit 0
elif [[ "$*" == *"--user=root"* ]]; then
  exit 64
fi
exit 0
'
    create_mock_cmd "mariadb" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
if [[ "$*" == *"-NBe select Concat("*"from mysql.user where HEX(User)="* ]]; then
  echo "GRANT SELECT on *.* to \`releem\`@\`%\`;"
elif [[ "$*" == *"-Be GRANT SELECT on *.* to"* ]]; then
  exit 0
elif [[ "$*" == *"--user=root"* ]]; then
  exit 64
fi
exit 0
'

    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    local mysql_args="${TEST_TMPDIR}/mysql.args"
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
        RELEEM_MYSQL_ROOT_LOGIN="admin" \
        RELEEM_MYSQL_ROOT_PASSWORD="rootpwd" \
        MYSQL_ARGS_LOG="${mysql_args}" \
        RELEEM_CRON_ENABLE="1" \
        enable_query_optimization

    [ "$status" -eq 0 ]
    run grep -F -- "--user=admin" "${mysql_args}"
    [ "$status" -eq 0 ]
    run grep -F -- "-NBe select Concat(" "${mysql_args}"
    [ "$status" -eq 0 ]
    run grep -F -- "-Be GRANT SELECT on *.* to" "${mysql_args}"
    [ "$status" -eq 0 ]
    run grep -E "^query_optimization=true$" "${conf}"
    [ "$status" -eq 0 ]
    run grep -E "^-p$" "${workdir}/calls.log"
    [ "$status" -eq 0 ]
}

@test "enable_query_optimization mode prompts for root password when env is unset" {
    prepare_common_install_mocks
    create_mock_cmd "mysqladmin" '
printf "%s\n" "$*" >> "${MYSQLADMIN_ARGS_LOG}"
if [[ "$*" == *"--user=root"* && "$*" != *"--password=promptedpwd"* ]]; then
  exit 1
fi
echo "mysqld is alive"
'
    create_mock_cmd "mariadb-admin" '
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
if [[ "$*" == *"-NBe select Concat("*"from mysql.user where HEX(User)="* ]]; then
  echo "GRANT SELECT on *.* to \`releem\`@\`%\`;"
fi
exit 0
'
    create_mock_cmd "mariadb" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
if [[ "$*" == *"--user=root"* && "$*" != *"--password=promptedpwd"* ]]; then
  exit 1
fi
if [[ "$*" == *"-NBe select Concat("*"from mysql.user where HEX(User)="* ]]; then
  echo "GRANT SELECT on *.* to \`releem\`@\`%\`;"
fi
exit 0
'

    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    local mysqladmin_args="${TEST_TMPDIR}/mysqladmin.args"
    local mysql_args="${TEST_TMPDIR}/mysql.args"
    mkdir -p "${workdir}"
    printf 'apikey="k1"\n' > "${conf}"
    printf '#!/usr/bin/env bash\nexit 0\n' > "${workdir}/releem-agent"
    printf '#!/usr/bin/env bash\necho "$@" >> "%s/calls.log"\nexit 0\n' "${workdir}" > "${workdir}/mysqlconfigurer.sh"
    chmod +x "${workdir}/releem-agent" "${workdir}/mysqlconfigurer.sh"
    cp "${INSTALL_SH}" "${MOCK_BIN}/enable_query_optimization"
    chmod +x "${MOCK_BIN}/enable_query_optimization"

    PATH="${MOCK_BIN}:${PATH}" run env -u RELEEM_MYSQL_ROOT_PASSWORD \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        RELEEM_API_KEY="k1" \
        MYSQLADMIN_ARGS_LOG="${mysqladmin_args}" \
        MYSQL_ARGS_LOG="${mysql_args}" \
        RELEEM_CRON_ENABLE="1" \
        enable_query_optimization <<< "promptedpwd"

    [ "$status" -eq 0 ]
    run grep -F -- "--user=root ping" "${mysqladmin_args}"
    [ "$status" -eq 0 ]
    run grep -F -- "--user=root --password=promptedpwd ping" "${mysqladmin_args}"
    [ "$status" -eq 0 ]
    run grep -F -- "--user=root --password=promptedpwd -NBe select Concat(" "${mysql_args}"
    [ "$status" -eq 0 ]
    run grep -F -- "--user=root --password=promptedpwd -Be GRANT SELECT on *.* to" "${mysql_args}"
    [ "$status" -eq 0 ]
    run grep -E "^query_optimization=true$" "${conf}"
    [ "$status" -eq 0 ]
    run grep -E "^-p$" "${workdir}/calls.log"
    [ "$status" -eq 0 ]
}

@test "enable_query_optimization mode uses existing PostgreSQL config without env database hints" {
    prepare_common_install_mocks
    create_mock_cmd "sudo" '
if [ "$1" = "-u" ]; then
  shift 2
fi
"$@"
'
    create_mock_cmd "psql" '
printf "%s\n" "$*" >> "${PG_ARGS_LOG}"
if [[ "$*" == *"SHOW server_version_num"* ]]; then
  echo "140000"
elif [[ "$*" == *"SELECT datname"* && "$*" == *"pg_database"* ]]; then
  printf "postgres\000shop\000"
fi
exit 0
'
    create_mock_cmd "mysqladmin" '
echo "unexpected mysqladmin call: $*" >> "${MYSQL_ARGS_LOG}"
exit 44
'
    create_mock_cmd "mariadb-admin" '
echo "unexpected mariadb-admin call: $*" >> "${MYSQL_ARGS_LOG}"
exit 44
'
    create_mock_cmd "mysql" '
echo "unexpected mysql call: $*" >> "${MYSQL_ARGS_LOG}"
exit 44
'
    create_mock_cmd "mariadb" '
echo "unexpected mariadb call: $*" >> "${MYSQL_ARGS_LOG}"
exit 44
'

    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    local pg_args="${TEST_TMPDIR}/pg.args"
    local mysql_args="${TEST_TMPDIR}/mysql.args"
    mkdir -p "${workdir}"
    printf 'apikey="k1"\npg_user="collector-ro"\npg_password="secret"\npg_host="127.0.0.1"\npg_port="5432"\n' > "${conf}"
    printf '#!/usr/bin/env bash\nexit 0\n' > "${workdir}/releem-agent"
    printf '#!/usr/bin/env bash\necho "$@" >> "%s/calls.log"\nexit 0\n' "${workdir}" > "${workdir}/mysqlconfigurer.sh"
    chmod +x "${workdir}/releem-agent" "${workdir}/mysqlconfigurer.sh"
    cp "${INSTALL_SH}" "${MOCK_BIN}/enable_query_optimization"
    chmod +x "${MOCK_BIN}/enable_query_optimization"

    PATH="${MOCK_BIN}:${PATH}" run env -u RELEEM_PG_HOST -u RELEEM_PG_LOGIN -u RELEEM_PG_PASSWORD -u RELEEM_PG_ROOT_LOGIN -u RELEEM_PG_ROOT_PASSWORD -u RELEEM_PG_TYPE -u RELEEM_MYSQL_HOST -u RELEEM_MYSQL_LOGIN -u RELEEM_MYSQL_PASSWORD -u RELEEM_MYSQL_ROOT_LOGIN -u RELEEM_MYSQL_ROOT_PASSWORD -u RELEEM_MYSQL_TYPE \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        PG_ARGS_LOG="${pg_args}" \
        MYSQL_ARGS_LOG="${mysql_args}" \
        RELEEM_CRON_ENABLE="1" \
        bash -x "${MOCK_BIN}/enable_query_optimization" enable_query_optimization

    [ "$status" -eq 0 ]
    run grep -F -- 'GRANT CONNECT ON DATABASE "postgres" TO "collector-ro";' "${pg_args}"
    [ "$status" -eq 0 ]
    run grep -F -- 'GRANT CONNECT ON DATABASE "shop" TO "collector-ro";' "${pg_args}"
    [ "$status" -eq 0 ]
    run grep -F -- 'GRANT pg_read_all_data TO "collector-ro";' "${pg_args}"
    [ "$status" -eq 0 ]
    for procedural_sql in 'DO $$' 'EXECUTE format' 'set_config' 'decode(' 'ALTER DEFAULT PRIVILEGES'; do
        run grep -F -- "${procedural_sql}" "${pg_args}"
        [ "$status" -ne 0 ]
    done
    [ ! -s "${mysql_args}" ]
    run grep -E "^query_optimization=true$" "${conf}"
    [ "$status" -eq 0 ]
}

@test "enable_query_optimization mode grants MySQL access to configured monitoring user" {
    prepare_common_install_mocks
    create_mock_cmd "mysqladmin" 'echo "mysqld is alive"'
    create_mock_cmd "mariadb-admin" 'echo "mysqld is alive"'
    create_mock_cmd "mysql" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
if [[ "$*" == *"HEX(User)="*"6F277265696C6C79"* ]]; then
  echo "GRANT SELECT on *.* to special_user;"
fi
exit 0
'
    create_mock_cmd "mariadb" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
if [[ "$*" == *"HEX(User)="*"6F277265696C6C79"* ]]; then
  echo "GRANT SELECT on *.* to special_user;"
fi
exit 0
'

    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    local mysql_args="${TEST_TMPDIR}/mysql.args"
    mkdir -p "${workdir}"
    printf 'apikey="k1"\nmysql_user="o'"'"'reilly"\nmysql_password="secret"\nmysql_host="127.0.0.1"\nmysql_port="3306"\n' > "${conf}"
    printf '#!/usr/bin/env bash\nexit 0\n' > "${workdir}/releem-agent"
    printf '#!/usr/bin/env bash\necho "$@" >> "%s/calls.log"\nexit 0\n' "${workdir}" > "${workdir}/mysqlconfigurer.sh"
    chmod +x "${workdir}/releem-agent" "${workdir}/mysqlconfigurer.sh"
    cp "${INSTALL_SH}" "${MOCK_BIN}/enable_query_optimization"
    chmod +x "${MOCK_BIN}/enable_query_optimization"

    PATH="${MOCK_BIN}:${PATH}" run env -u RELEEM_PG_HOST -u RELEEM_PG_LOGIN -u RELEEM_PG_PASSWORD -u RELEEM_PG_ROOT_LOGIN -u RELEEM_PG_ROOT_PASSWORD -u RELEEM_PG_TYPE -u RELEEM_MYSQL_HOST -u RELEEM_MYSQL_LOGIN -u RELEEM_MYSQL_PASSWORD -u RELEEM_MYSQL_ROOT_LOGIN -u RELEEM_MYSQL_ROOT_PASSWORD -u RELEEM_MYSQL_TYPE \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        MYSQL_ARGS_LOG="${mysql_args}" \
        RELEEM_CRON_ENABLE="1" \
        bash "${MOCK_BIN}/enable_query_optimization" enable_query_optimization

    [ "$status" -eq 0 ]
    run grep -F -- "HEX(User)='6F277265696C6C79'" "${mysql_args}"
    [ "$status" -eq 0 ]
    run grep -F -- "REPLACE(User, CHAR(96)" "${mysql_args}"
    [ "$status" -eq 0 ]
    run grep -F -- "REPLACE(Host, CHAR(96)" "${mysql_args}"
    [ "$status" -eq 0 ]
    run grep -F -- "-Be GRANT SELECT on *.* to special_user;" "${mysql_args}"
    [ "$status" -eq 0 ]
    run grep -F -- "where User='releem'" "${mysql_args}"
    [ "$status" -ne 0 ]
    run grep -E "^query_optimization=true$" "${conf}"
    [ "$status" -eq 0 ]
}

@test "load_runtime_config preserves existing query optimization setting" {
    local workdir="${TEST_TMPDIR}/workdir"
    mkdir -p "${workdir}"
    printf 'apikey="k1"\nquery_optimization=false # existing setting\n' > "${workdir}/releem.conf"

    run bash -c '
        RELEEM_TEST_MODE=1 RELEEM_WORKDIR="$1" source "$2"
        unset query_optimization RELEEM_QUERY_OPTIMIZATION
        load_runtime_config
        printf "%s|%s" "$RELEEM_QUERY_OPTIMIZATION" "$query_optimization"
    ' _ "${workdir}" "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [ "$output" = "false|false" ]
}

@test "enable_query_optimization fails when configured MySQL account is missing" {
    prepare_common_install_mocks
    create_mock_cmd "mysqladmin" 'echo "mysqld is alive"'
    create_mock_cmd "mariadb-admin" 'echo "mysqld is alive"'
    create_mock_cmd "mysql" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
exit 0
'
    create_mock_cmd "mariadb" '
printf "%s\n" "$*" >> "${MYSQL_ARGS_LOG}"
exit 0
'

    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    local mysql_args="${TEST_TMPDIR}/mysql.args"
    mkdir -p "${workdir}"
    printf 'apikey="k1"\nmysql_user="missing"\nmysql_password="secret"\nmysql_host="127.0.0.1"\nmysql_port="3306"\n' > "${conf}"
    printf '#!/usr/bin/env bash\nexit 0\n' > "${workdir}/releem-agent"
    printf '#!/usr/bin/env bash\nexit 0\n' > "${workdir}/mysqlconfigurer.sh"
    chmod +x "${workdir}/releem-agent" "${workdir}/mysqlconfigurer.sh"
    cp "${INSTALL_SH}" "${MOCK_BIN}/enable_query_optimization"
    chmod +x "${MOCK_BIN}/enable_query_optimization"

    PATH="${MOCK_BIN}:${PATH}" run env -u RELEEM_MYSQL_LOGIN -u RELEEM_MYSQL_PASSWORD \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        MYSQL_ARGS_LOG="${mysql_args}" \
        RELEEM_CRON_ENABLE="1" \
        bash "${MOCK_BIN}/enable_query_optimization" enable_query_optimization

    [ "$status" -ne 0 ]
    run grep -E "^query_optimization=true$" "${conf}"
    [ "$status" -ne 0 ]
}

@test "enable_query_optimization mode grants pg_read_all_data for postgresql" {
    prepare_common_install_mocks
    create_mock_cmd "psql" '
printf "%s\n" "$*" >> "${PG_ARGS_LOG}"
if [[ "$*" == *"SHOW server_version_num"* ]]; then
  echo "140000"
elif [[ "$*" == *"SELECT datname"* && "$*" == *"pg_database"* ]]; then
  printf "postgres\000shop\000"
fi
exit 0
'

    local workdir="${TEST_TMPDIR}/workdir"
    local conf="${workdir}/releem.conf"
    local pg_args="${TEST_TMPDIR}/pg.args"
    mkdir -p "${workdir}"
    printf 'apikey="k1"\n' > "${conf}"
    printf '#!/usr/bin/env bash\nexit 0\n' > "${workdir}/releem-agent"
    printf '#!/usr/bin/env bash\necho "$@" >> "%s/calls.log"\nexit 0\n' "${workdir}" > "${workdir}/mysqlconfigurer.sh"
    chmod +x "${workdir}/releem-agent" "${workdir}/mysqlconfigurer.sh"
    cp "${INSTALL_SH}" "${MOCK_BIN}/enable_query_optimization"
    chmod +x "${MOCK_BIN}/enable_query_optimization"

    PATH="${MOCK_BIN}:${PATH}" run env \
        PATH="${MOCK_BIN}:${PATH}" \
        RELEEM_TEST_MODE=1 \
        RELEEM_WORKDIR="${workdir}" \
        RELEEM_CONF_FILE="${conf}" \
        RELEEM_API_KEY="k1" \
        RELEEM_PG_ROOT_LOGIN="pgadmin" \
        RELEEM_PG_ROOT_PASSWORD="rootpwd" \
        PG_ARGS_LOG="${pg_args}" \
        RELEEM_CRON_ENABLE="1" \
        bash "${MOCK_BIN}/enable_query_optimization" enable_query_optimization

    [ "$status" -eq 0 ]
    run grep -F -- 'GRANT CONNECT ON DATABASE "postgres" TO "releem";' "${pg_args}"
    [ "$status" -eq 0 ]
    run grep -F -- 'GRANT CONNECT ON DATABASE "shop" TO "releem";' "${pg_args}"
    [ "$status" -eq 0 ]
    run grep -F -- 'GRANT pg_read_all_data TO "releem";' "${pg_args}"
    [ "$status" -eq 0 ]
    for procedural_sql in 'DO $$' 'EXECUTE format' 'set_config' 'decode(' 'ALTER DEFAULT PRIVILEGES'; do
        run grep -F -- "${procedural_sql}" "${pg_args}"
        [ "$status" -ne 0 ]
    done
    run grep -E "^query_optimization=true$" "${conf}"
    [ "$status" -eq 0 ]
    run grep -E "^-p$" "${workdir}/calls.log"
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

@test "postgresql catalog grants preserve newline identifiers from NUL records" {
    create_mock_cmd "psql" '
query="$*"
printf "%s\n" "$query" >> "${PG_ARGS_LOG}"
if [[ "$query" == *"SHOW server_version_num"* ]]; then
  printf "120000\n"
elif [[ "$query" == *"SELECT datname"* && "$query" == *"pg_database"* ]]; then
  [[ " ${query} " == *" -0 "* ]] || exit 91
  printf "catalog\ndb\000plain db\000"
elif [[ "$query" == *"SELECT nspname"* && "$query" == *"pg_namespace"* ]]; then
  [[ " ${query} " == *" -0 "* ]] || exit 92
  printf "sales\nschema\000odd \"schema\"\000"
elif [[ "$query" == *"GRANT"* ]]; then
  [[ "$query" == *"-v ON_ERROR_STOP=1"* ]] || exit 93
  cat >/dev/null
fi
exit 0
'

    run env \
        RELEEM_PG_ROOT_PASSWORD="rootpwd" \
        PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" \
        bash -c '
            RELEEM_TEST_MODE=1 source "$1"
            psqlcmd="$2"
            pg_root_peer_connection=""
            pg_root_connection_string="-h 127.0.0.1 -p 5432 -d postgres"
            grant_postgresql_query_optimization_access "postgres" '"'"'role"reader'"'"'
        ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql"

    [ "$status" -eq 0 ]
    run grep -F -- $'GRANT CONNECT ON DATABASE "catalog\ndb" TO "role""reader";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -F -- 'GRANT CONNECT ON DATABASE "plain db" TO "role""reader";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -F -- $'-d catalog\ndb -v ON_ERROR_STOP=1 -c GRANT USAGE ON SCHEMA "sales\nschema" TO "role""reader";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
    run grep -F -- 'GRANT SELECT ON ALL TABLES IN SCHEMA "odd ""schema""" TO "role""reader";' "${TEST_TMPDIR}/pg.args"
    [ "$status" -eq 0 ]
}

@test "postgresql catalog grants do not require mapfile delimiter support" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        mapfile() { return 97; }
        postgresql_root_exec() {
            case "$*" in
                *"SHOW server_version_num"*) printf "140000\n" ;;
                *"pg_database"*) printf "postgres\000" ;;
                *"GRANT CONNECT"*) printf "%s\n" "$*" >&2 ;;
                *"GRANT pg_read_all_data"*) printf "%s\n" "$*" >&2 ;;
            esac
        }
        grant_postgresql_query_optimization_access postgres releem
    ' _ "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [[ "$output" == *'GRANT CONNECT ON DATABASE "postgres" TO "releem";'* ]]
    [[ "$output" == *'GRANT pg_read_all_data TO "releem";'* ]]
}

@test "postgresql database catalog failure stops before grants" {
    create_mock_cmd "psql" '
query="$*"
printf "%s\n" "$query" >> "${PG_ARGS_LOG}"
if [[ "$query" == *"SHOW server_version_num"* ]]; then
  printf "140000\n"
elif [[ "$query" == *"SELECT datname"* && "$query" == *"pg_database"* ]]; then
  exit 41
fi
exit 0
'

    run env PG_ARGS_LOG="${TEST_TMPDIR}/pg.args" bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        psqlcmd="$2"
        pg_root_peer_connection=""
        pg_root_connection_string="-d postgres"
        grant_postgresql_query_optimization_access postgres releem
    ' _ "${INSTALL_SH}" "${MOCK_BIN}/psql"

    [ "$status" -ne 0 ]
    run grep -F -- "GRANT" "${TEST_TMPDIR}/pg.args"
    [ "$status" -ne 0 ]
}

@test "postgresql schema catalog failure stops later grants" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        postgresql_root_exec() {
            printf "%s\n" "$*" >&2
            case "$*" in
                *"SHOW server_version_num"*) printf "120000\n" ;;
                *"pg_database"*) printf "first\000second\000" ;;
                *"pg_namespace"*) return 42 ;;
            esac
        }
        grant_postgresql_query_optimization_access postgres releem
    ' _ "${INSTALL_SH}"

    [ "$status" -ne 0 ]
    [[ "$output" != *"GRANT USAGE ON SCHEMA"* ]]
    [[ "$output" != *"GRANT SELECT ON ALL TABLES"* ]]
}

@test "postgresql grant failure stops later grants" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        postgresql_root_exec() {
            printf "%s\n" "$*" >&2
            case "$*" in
                *"SHOW server_version_num"*) printf "140000\n" ;;
                *"pg_database"*) printf "first\000second\000" ;;
                *"GRANT CONNECT ON DATABASE \"first\""*)
                    [[ "$*" == *"-v ON_ERROR_STOP=1"* ]] || return 93
                    return 43
                    ;;
                *"GRANT"*) [[ "$*" == *"-v ON_ERROR_STOP=1"* ]] || return 93 ;;
            esac
        }
        grant_postgresql_query_optimization_access postgres releem
    ' _ "${INSTALL_SH}"

    [ "$status" -ne 0 ]
    [[ "$output" != *'GRANT CONNECT ON DATABASE "second"'* ]]
    [[ "$output" != *'GRANT pg_read_all_data'* ]]
}
