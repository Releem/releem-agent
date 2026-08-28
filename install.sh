#!/usr/bin/env bash
# install.sh - Version 1.23.7
# (C) Releem, Inc 2022
# All rights reserved

export PATH=$PATH:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin
set -e -E

# Releem installation script: install and set up the Releem Agent on supported Linux distributions
# using the package manager.

# Set defaults.
install_script_version=1.23.7
logfile="/var/log/releem-install.log"
npipe=""

RELEEM_WORKDIR="${RELEEM_WORKDIR:-/opt/releem}"
RELEEM_CONF_FILE="$RELEEM_WORKDIR/releem.conf"
RELEEM_COMMAND="/bin/bash $RELEEM_WORKDIR/mysqlconfigurer.sh"
RELEEM_AGENT_BINARY_URL="https://releem.s3.amazonaws.com/v2/releem-agent-$(arch)"
RELEEM_AGENT_SCRIPT_URL="https://releem.s3.amazonaws.com/v2/mysqlconfigurer.sh"

# Root user detection
if [ "$(echo "$UID")" = "0" ]; then
    sudo_cmd=''
else
    sudo_cmd='sudo'
fi

function on_exit() {  
    if [[ "${RELEEM_REGION}" == "EU" ]]; then
        API_DOMAIN="api.queries.eu.releem.com"
    else
        API_DOMAIN="api.queries.releem.com"
    fi    
    curl -s -L -d @"$logfile" -H "x-releem-api-key: $apikey" -H "Content-Type: application/json" -X POST "https://${API_DOMAIN}/v2/events/saving_log"
    [ -n "$npipe" ] && rm -f "$npipe"
}
if [ -z "$RELEEM_TEST_MODE" ]; then
    trap on_exit EXIT
fi

function on_error() {
    printf "\033[31m $ERROR_MESSAGE\n"
    printf "\033[31m It looks like you encountered an issue while installing the Releem.\n"
    printf "\033[31m If you are still experiencing problems, please send an email to hello@releem.com \n"
    printf "\033[31m with the contents of the $logfile. We will do our best to resolve the issue.\n"
    printf "\033[0m\n"
}
if [ -z "$RELEEM_TEST_MODE" ]; then
    trap on_error ERR
fi

function setup_logging() {
    # Set up a named pipe for logging 
    npipe=/tmp/$$.install.tmp
    mknod "$npipe" p
    # Log all output to a log for error checking
    tee <"$npipe" "$logfile" &
    exec 1>&-
    exec 1>"$npipe" 2>&1
}
if [ -z "$RELEEM_TEST_MODE" ]; then
    setup_logging
fi

function releem_set_cron() {
    ($sudo_cmd crontab -l 2>/dev/null | grep -v "$RELEEM_WORKDIR/mysqlconfigurer.sh" || true; echo "$RELEEM_CRON") | $sudo_cmd crontab -
}

function releem_update() {
    detect_releem_api_key
    printf "\033[37m\n * Downloading latest version of Releem Agent.\033[0m\n"

    $sudo_cmd curl -w "%{http_code}" -L -o $RELEEM_WORKDIR/releem-agent.new $RELEEM_AGENT_BINARY_URL
    $sudo_cmd curl -w "%{http_code}" -L -o $RELEEM_WORKDIR/mysqlconfigurer.sh.new $RELEEM_AGENT_SCRIPT_URL
    $sudo_cmd $RELEEM_WORKDIR/releem-agent  stop || true
    $sudo_cmd mv $RELEEM_WORKDIR/releem-agent.new $RELEEM_WORKDIR/releem-agent
    $sudo_cmd mv $RELEEM_WORKDIR/mysqlconfigurer.sh.new $RELEEM_WORKDIR/mysqlconfigurer.sh
    $sudo_cmd chmod 755 $RELEEM_WORKDIR/mysqlconfigurer.sh   $RELEEM_WORKDIR/releem-agent
    $sudo_cmd $RELEEM_WORKDIR/releem-agent start || true
    $sudo_cmd $RELEEM_WORKDIR/releem-agent -f
    
    echo
    echo
    echo -e "Releem Agent updated successfully."
    echo
    echo -e "To check Releem Performance Score please visit https://app.releem.com/dashboard?menu=metrics"
    echo

    exit 0
}

# Detect instance type based on environment variables
function detect_instance_type() {
    instance_type="local"
    if [ -n "$RELEEM_INSTANCE_TYPE" ]; then
        instance_type=$RELEEM_INSTANCE_TYPE 
    fi
}

# Detect database type based on environment variables
function detect_database_type() {
    printf "\033[37m\n * Detecting database type based on environment variables.\033[0m\n"
    # Database type detection
    database_type="mysql"  # Default to MySQL for backward compatibility
    
    # Check for PostgreSQL environment variables
    if [[ -n "${RELEEM_PG_HOST+x}" ]] || [[ -n "${RELEEM_PG_LOGIN+x}" ]] || [[ -n "${RELEEM_PG_PASSWORD+x}" ]] || [[ -n "${RELEEM_PG_ROOT_PASSWORD+x}" ]]; then
        database_type="postgresql"
        printf "\033[37m   Detected PostgreSQL configuration.\033[0m\n"
    # Check for MySQL environment variables (fallback)
    elif [[ -n "${RELEEM_MYSQL_HOST+x}" ]] || [[ -n "${RELEEM_MYSQL_LOGIN+x}" ]] || [[ -n "${RELEEM_MYSQL_PASSWORD+x}" ]] || [[ -n "${RELEEM_MYSQL_ROOT_PASSWORD+x}" ]]; then
        database_type="mysql"
        printf "\033[37m   Detected MySQL configuration.\033[0m\n"
    else
        # Default to MySQL for backward compatibility
        database_type="mysql"
        printf "\033[37m   No specific database configuration detected, defaulting to MySQL.\033[0m\n"
    fi
}

# ================================================================================
# FUNCTIONS FOR LOCAL INSTANCE CONFIGURATION
# ================================================================================

function detect_mysql_commands() {
    local mysqladmin_cmd=""
    local mysql_cmd=""
    
    printf "\033[37m\n * Detecting MySQL/MariaDB commands.\033[0m\n"
    
    # Detect mysqladmin/mariadb-admin
    mysqladmin_cmd=$(which mariadb-admin 2>/dev/null || which mysqladmin 2>/dev/null || true)
    if [ -z "$mysqladmin_cmd" ]; then
        printf "\033[31m Couldn't find mysqladmin/mariadb-admin in your \$PATH. Correct the path to mysqladmin/mariadb-admin in a \$PATH variable \033[0m\n"
        on_error
        exit 1
    fi
    
    # Detect mysql/mariadb
    mysql_cmd=$(which mariadb 2>/dev/null || which mysql 2>/dev/null || true)
    if [ -z "$mysql_cmd" ]; then
        printf "\033[31m Couldn't find mysql/mariadb in your \$PATH. Correct the path to mysql/mariadb in a \$PATH variable \033[0m\n"
        on_error
        exit 1
    fi
    
    # Export as global variables
    mysqladmincmd="$mysqladmin_cmd"
    mysqlcmd="$mysql_cmd"
}

function detect_postgresql_commands() {
    local psql_cmd=""
    local pg_isready_cmd=""
    
    printf "\033[37m\n * Detecting PostgreSQL commands.\033[0m\n"
    
    # Detect psql
    psql_cmd=$(which psql 2>/dev/null || true)
    if [ -z "$psql_cmd" ]; then
        printf "\033[31m Couldn't find psql in your \$PATH. Please install PostgreSQL client tools or correct the \$PATH variable \033[0m\n"
        on_error
        exit 1
    fi

    # Detect pg_isready
    pg_isready_cmd=$(which pg_isready 2>/dev/null || true)
    # if [ -z "$pg_isready_cmd" ]; then
    #     printf "\033[31m Couldn't find pg_isready in your \$PATH. Please install PostgreSQL client tools \033[0m\n"
    #     on_error
    #     exit 1
    # fi    

    # Export as global variables
    psqlcmd="$psql_cmd"
    pg_isreadycmd="$pg_isready_cmd"

    printf "\033[37m   Found psql: %s\033[0m\n" "$psql_cmd"
    printf "\033[37m   Found pg_isready: %s\033[0m\n" "$pg_isready_cmd"
}

function setup_mysql_connection_string() {
    printf "\033[37m\n * Setting up MySQL connection parameters.\033[0m\n"
    
    connection_string=""  
    root_connection_string=""
    
    if [ -n "$RELEEM_MYSQL_HOST" ]; then
        if [ -S "$RELEEM_MYSQL_HOST" ]; then
            mysql_user_host="localhost"
            connection_string="${connection_string} --socket=${RELEEM_MYSQL_HOST}"
            root_connection_string="${root_connection_string} --socket=${RELEEM_MYSQL_HOST}"
            printf "\033[37m   Using socket: %s\033[0m\n" "$RELEEM_MYSQL_HOST"
        else
            if [ "$RELEEM_MYSQL_HOST" == "127.0.0.1" ]; then
                mysql_user_host="127.0.0.1"
            else
                mysql_user_host="%"
            fi
            connection_string="${connection_string} --host=${RELEEM_MYSQL_HOST}"
            
            if [ -n "$RELEEM_MYSQL_PORT" ]; then
                connection_string="${connection_string} --port=${RELEEM_MYSQL_PORT}"
            else
                connection_string="${connection_string} --port=3306"
            fi
            printf "\033[37m   Using host: %s, port: %s\033[0m\n" "$RELEEM_MYSQL_HOST" "${RELEEM_MYSQL_PORT:-3306}"
        fi
    else
        mysql_user_host="127.0.0.1"
        connection_string="${connection_string} --host=127.0.0.1"
        
        if [ -n "$RELEEM_MYSQL_PORT" ]; then
            connection_string="${connection_string} --port=${RELEEM_MYSQL_PORT}"
        else
            connection_string="${connection_string} --port=3306"
        fi
        printf "\033[37m   Using default: 127.0.0.1:%s\033[0m\n" "${RELEEM_MYSQL_PORT:-3306}"
    fi
}

function setup_postgresql_connection_string() {
    printf "\033[37m\n * Setting up PostgreSQL connection parameters.\033[0m\n"
    
    pg_connection_string=""
    pg_root_connection_string=""
    pg_root_peer_connection=""

    # Set PostgreSQL host
    if [ -z "$RELEEM_PG_ROOT_PASSWORD" ]; then
        pg_root_connection_string="${pg_root_connection_string}"
        pg_root_peer_connection="sudo -u postgres "
    else
        pg_root_connection_string="${pg_root_connection_string} -h ${RELEEM_PG_HOST:-127.0.0.1}"            
    fi
    pg_connection_string="${pg_connection_string} -h ${RELEEM_PG_HOST:-127.0.0.1}"            


    # Set PostgreSQL port
    pg_connection_string="${pg_connection_string} -p ${RELEEM_PG_PORT:-5432}"
    pg_root_connection_string="${pg_root_connection_string} -p ${RELEEM_PG_PORT:-5432}"

    printf "\033[37m   Using connection params: admin - '%s psql %s', user - '%s'\033[0m\n" "$pg_root_peer_connection" "$pg_root_connection_string" "$pg_connection_string"

    # Set database name (default to postgres)
    pg_database="${RELEEM_PG_DATABASE:-postgres}"
    pg_connection_string="${pg_connection_string} -d ${pg_database}"
    pg_root_connection_string="${pg_root_connection_string} -d ${pg_database}"
}

function detect_mysql_service() {
    printf "\033[37m\n * Detecting MySQL service name for database server restart.\033[0m\n"

    local systemctl_cmd
    systemctl_cmd=$(which systemctl 2>/dev/null || true)

    if [ -n "$systemctl_cmd" ]; then
        # Check if MySQL is running
        if $sudo_cmd $systemctl_cmd status mariadb >/dev/null 2>&1; then
            service_name_cmd="$sudo_cmd $systemctl_cmd restart mariadb"
        elif $sudo_cmd $systemctl_cmd status mysql >/dev/null 2>&1; then
            service_name_cmd="$sudo_cmd $systemctl_cmd restart mysql"
        elif $sudo_cmd $systemctl_cmd status mysqld >/dev/null 2>&1; then
            service_name_cmd="$sudo_cmd $systemctl_cmd restart mysqld"
        else
            printf "\033[31m\n * Failed to determine systemd service to restart.\033[0m"
        fi
    else
        # Check if MySQL is running
        if [ -f /etc/init.d/mysql ]; then
            service_name_cmd="$sudo_cmd /etc/init.d/mysql restart"
        elif [ -f /etc/init.d/mysqld ]; then
            service_name_cmd="$sudo_cmd /etc/init.d/mysqld restart"
        elif [ -f /etc/init.d/mariadb ]; then
            service_name_cmd="$sudo_cmd /etc/init.d/mariadb restart"
        else
            printf "\033[31m\n * Failed to determine init.d service to restart.\033[0m"
        fi
    fi
    
    if [ -z "$service_name_cmd" ]; then
        printf "\033[31m\n   The automatic applying configuration will not work. \n\033[0m"
    fi
}

function detect_postgresql_service() {
    printf "\033[37m\n * Detecting PostgreSQL service name for database server restart.\033[0m\n"

    local systemctl_cmd
    systemctl_cmd=$(which systemctl 2>/dev/null || true)

    if [ -n "$systemctl_cmd" ]; then
        # Check if PostgreSQL is running
        if $sudo_cmd $systemctl_cmd status postgresql >/dev/null 2>&1; then
            pg_service_name_cmd="$sudo_cmd $systemctl_cmd restart postgresql"
        elif $sudo_cmd $systemctl_cmd status postgresql-* >/dev/null 2>&1; then
            # Try to find versioned PostgreSQL service
            pg_service=$(systemctl list-units --type=service | grep -o 'postgresql-[0-9][0-9]*\.service' | head -n1 | sed 's/\.service//')
            if [ -n "$pg_service" ]; then
                pg_service_name_cmd="$sudo_cmd $systemctl_cmd restart $pg_service"
            else
                printf "\033[31m\n * Failed to determine PostgreSQL systemd service to restart.\033[0m"
            fi
        else
            printf "\033[31m\n * Failed to determine PostgreSQL systemd service to restart.\033[0m"
        fi
    else
        # Check if PostgreSQL is running with init.d
        if [ -f /etc/init.d/postgresql ]; then
            pg_service_name_cmd="$sudo_cmd /etc/init.d/postgresql restart"
        else
            printf "\033[31m\n * Failed to determine PostgreSQL init.d service to restart.\033[0m"
        fi
    fi
    
    if [ -z "$pg_service_name_cmd" ]; then
        printf "\033[31m\n   The automatic applying PostgreSQL configuration will not work. \n\033[0m"
    else
        printf "\033[37m   PostgreSQL restart command: %s\033[0m\n" "$pg_service_name_cmd"
    fi
}

function setup_mysql_config_directory() {
    MYSQL_CONF_DIR="/etc/mysql/releem.conf.d"

    printf "\033[37m\n * Setting up MySQL configuration directory.\033[0m\n"
    if [ -n "$RELEEM_MYSQL_MY_CNF_PATH" ]; then
        MYSQL_MY_CNF_PATH=$RELEEM_MYSQL_MY_CNF_PATH
        printf "\033[37m   Using provided my.cnf path: %s\033[0m\n" "$MYSQL_MY_CNF_PATH"
    else
        if [ -f "/etc/my.cnf" ]; then
            MYSQL_MY_CNF_PATH="/etc/my.cnf"
        elif [ -f "/etc/mysql/my.cnf" ]; then
            MYSQL_MY_CNF_PATH="/etc/mysql/my.cnf"
        else
            read -p "File my.cnf not found in default path. Please set the current location of the configuration file: " -r
            echo    # move to a new line
            MYSQL_MY_CNF_PATH=$REPLY
        fi
    fi
    if [ ! -f "$MYSQL_MY_CNF_PATH" ]; then
        printf "\033[31m * File $MYSQL_MY_CNF_PATH not found. The automatic applying configuration is disabled. Please, reinstall the Releem Agent.\033[0m\n"
    else
        printf "\033[37m\n * The $MYSQL_MY_CNF_PATH file is being used to apply Releem is recommended settings.\n\033[0m"
        printf "\033[37m\n * Adding directive includedir to the MySQL configuration $MYSQL_MY_CNF_PATH.\n\033[0m"
        $sudo_cmd mkdir -p $MYSQL_CONF_DIR
        $sudo_cmd chmod 755 $MYSQL_CONF_DIR
        #Исключить дублирование
        if [ `$sudo_cmd grep -cE "!includedir $MYSQL_CONF_DIR" $MYSQL_MY_CNF_PATH` -eq 0 ];
        then
            echo -e "\n!includedir $MYSQL_CONF_DIR" | $sudo_cmd tee -a $MYSQL_MY_CNF_PATH >/dev/null
        fi
    fi    
}

function setup_mysql_root_password_option() {
    mysql_root_password_option=()
    if [[ -n "${RELEEM_MYSQL_ROOT_PASSWORD+x}" ]]; then
        mysql_root_password_option=("--password=${RELEEM_MYSQL_ROOT_PASSWORD}")
    fi
}

function mysql_root_ping() {
    $mysqladmincmd ${root_connection_string} --user=root "${mysql_root_password_option[@]}" ping
}

function mysql_root_exec() {
    $mysqlcmd ${root_connection_string} --user=root "${mysql_root_password_option[@]}" -Be "$1"
}

function mysql_root_connection_successful() {
    [[ $(mysql_root_ping 2>/dev/null || true) == "mysqld is alive" ]]
}

function prompt_mysql_root_password_until_success() {
    while true; do
        if ! read -s -p "Please enter MySQL root password: " RELEEM_MYSQL_ROOT_PASSWORD; then
            echo
            printf "\033[31m\n Unable to read MySQL root password.\033[0m\n"
            return 1
        fi
        echo
        setup_mysql_root_password_option

        if mysql_root_connection_successful; then
            return 0
        fi

        printf "\033[31m\n MySQL connection failed with the entered root password.\033[0m\n"
    done
}

function setup_postgresql_config_directory() {
    printf "\033[37m\n * Setting up PostgreSQL configuration directory.\033[0m\n"
    
    # Find PostgreSQL configuration file
    if [ -n "$RELEEM_PG_CONF_DIR" ]; then
        PG_CONF_DIR="$RELEEM_PG_CONF_DIR"
        printf "\033[37m   Using provided conf.d directory: %s\033[0m\n" "$PG_CONF_DIR"
    else
        # Try common PostgreSQL configuration paths
        for pg_version in 18 17 16 15 14 13 12 11 10 9.6 9.5; do
            if [ -f "/etc/postgresql/${pg_version}/main/postgresql.conf" ]; then
                PG_CONF_DIR="/etc/postgresql/${pg_version}/main/conf.d"
                break
            elif [ -f "/var/lib/pgsql/${pg_version}/data/postgresql.conf" ]; then
                PG_CONF_DIR="/var/lib/pgsql/${pg_version}/data/conf.d"
                break
            fi
        done
        
        # Try generic paths
        if [ -z "$PG_CONF_DIR" ]; then
            if [ -f "/etc/postgresql/postgresql.conf" ]; then
                PG_CONF_DIR="/etc/postgresql/conf.d"
            elif [ -f "/var/lib/pgsql/data/postgresql.conf" ]; then
                PG_CONF_DIR="/var/lib/pgsql/data/conf.d"
            else
                printf "\033[33m Warning: PostgreSQL configuration directory not found in standard locations.\033[0m\n"
                printf "\033[33m Please set RELEEM_PG_CONF_DIR environment variable.\033[0m\n"
                return
            fi
        fi
    fi
    
    if [ ! -d "$PG_CONF_DIR" ]; then
        printf "\033[31m * File $PG_CONF_DIR not found. The automatic applying PostgreSQL configuration is disabled.\033[0m\n"
    else
        printf "\033[37m   Using PostgreSQL configuration directory: %s\033[0m\n" "$PG_CONF_DIR"
    fi
}

function create_mysql_user() {
    printf "\033[37m\n * Configuring the MySQL user for metrics collection.\033[0m\n"
    FLAG_SUCCESS=0
    if [ -n "$RELEEM_MYSQL_PASSWORD" ] && [ -n "$RELEEM_MYSQL_LOGIN" ]; then
        printf "\033[37m\n * Using MySQL login and password from environment variables\033[0m\n"
        FLAG_SUCCESS=1
    #elif [ -n "$RELEEM_MYSQL_ROOT_PASSWORD" ]; then
    else
        printf "\033[37m\n * Using MySQL root user.\033[0m\n"
        setup_mysql_root_password_option

        if ! mysql_root_connection_successful && [[ -z "${RELEEM_MYSQL_ROOT_PASSWORD+x}" ]]; then
            prompt_mysql_root_password_until_success || true
        fi

        if mysql_root_connection_successful;
        then
            printf "\033[37m\n MySQL connection successful.\033[0m\n"
            RELEEM_MYSQL_LOGIN="releem"
            RELEEM_MYSQL_PASSWORD=$(cat /dev/urandom | tr -cd '%*)?@#~' | head -c2 ; cat /dev/urandom | tr -cd '%*)?@#~A-Za-z0-9%*)?@#~' | head -c16 ; cat /dev/urandom | tr -cd '%*)?@#~' | head -c2 )
            mysql_root_exec "DROP USER '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}' ;" 2>/dev/null || true
            mysql_root_exec "CREATE USER '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}' identified by '${RELEEM_MYSQL_PASSWORD}';"
            mysql_root_exec "GRANT PROCESS ON *.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"
            mysql_root_exec "GRANT REPLICATION CLIENT ON *.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"
            if mysql_root_exec "GRANT REPLICA MONITOR ON *.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';" 2>/dev/null
            then
                echo "Successfully GRANT" > /dev/null
            elif mysql_root_exec "GRANT REPLICATION SLAVE ADMIN ON *.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';" 2>/dev/null
            then
                echo "Successfully GRANT" > /dev/null
            fi
            mysql_root_exec "GRANT SHOW VIEW ON *.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"
            mysql_root_exec "GRANT SELECT ON mysql.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"

            if mysql_root_exec "GRANT SELECT ON performance_schema.events_statements_summary_by_digest TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"
            then
                echo "Successfully GRANT" > /dev/null
            else
                printf "\033[31m\n This database version is too old, and it doesn't collect SQL Queries Latency metrics. You will not see Latency on the Dashboard.\033[0m\n"
            fi
            if mysql_root_exec "GRANT SELECT ON performance_schema.table_io_waits_summary_by_index_usage TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"
            then
                echo "Successfully GRANT" > /dev/null
            else
                printf "\033[31m\n This database version is too old.\033[0m\n"
            fi   

            if mysql_root_exec "GRANT SELECT ON performance_schema.file_summary_by_instance TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"
            then
                echo "Successfully GRANT" > /dev/null
            else
                printf "\033[31m\n This database version is too old.\033[0m\n"
            fi

            if mysql_root_exec "GRANT SELECT ON performance_schema.replication_group_members TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';" 2>/dev/null
            then
                echo "Successfully GRANT" > /dev/null
            else
                printf "\033[33m\n Group Replication topology metrics are unavailable on this database version.\033[0m\n"
            fi

            if mysql_root_exec "GRANT SYSTEM_VARIABLES_ADMIN ON *.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';" 2>/dev/null
            then
                echo "Successfully GRANT" > /dev/null
            else
                if mysql_root_exec "GRANT SUPER ON *.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"
                then
                    echo "Successfully GRANT" > /dev/null
                else
                    printf "\033[31m\n Error granting privileges to apply without restarting.\033[0m\n"
                fi         
            fi 

            if [ -n "$RELEEM_QUERY_OPTIMIZATION" ];
            then
                mysql_root_exec "GRANT SELECT ON *.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"
            fi        

            #$mysqlcmd  ${root_connection_string} --user=root --password=${RELEEM_MYSQL_ROOT_PASSWORD} -Be "GRANT SELECT, PROCESS,EXECUTE, REPLICATION CLIENT,SHOW DATABASES,SHOW VIEW ON *.* TO '${RELEEM_MYSQL_LOGIN}'@'${mysql_user_host}';"
            printf "\033[32m\n Created new user \`${RELEEM_MYSQL_LOGIN}\`\033[0m\n"
            FLAG_SUCCESS=1
        else
            printf "\033[31m\n%s\n%s\033[0m\n" "MySQL connection failed with user root." "Check that the password is correct, the execution of the command \`${mysqladmincmd} ${root_connection_string} --user=root --password=<MYSQL_ROOT_PASSWORD> ping\` and reinstall the agent."
            mysql_root_ping || true
            on_error
            exit 1
        fi
    fi
    if [ "$FLAG_SUCCESS" == "1" ]; then
        if [[ $($mysqladmincmd ${connection_string} --user=${RELEEM_MYSQL_LOGIN} --password=${RELEEM_MYSQL_PASSWORD} ping 2>/dev/null || true) == "mysqld is alive" ]];
        then
            printf "\033[32m\n MySQL connection with user \`${RELEEM_MYSQL_LOGIN}\` - successful. \033[0m\n"
            MYSQL_LOGIN=$RELEEM_MYSQL_LOGIN
            MYSQL_PASSWORD=$RELEEM_MYSQL_PASSWORD
        else
            printf "\033[31m\n%s\n%s\033[0m\n" "MySQL connection failed with user \`${RELEEM_MYSQL_LOGIN}\`." "Check that the user and password is correct, the execution of the command \`${mysqladmincmd} ${connection_string} --user=${RELEEM_MYSQL_LOGIN} --password='${RELEEM_MYSQL_PASSWORD}' ping\` and reinstall the agent."
            $mysqladmincmd ${connection_string} --user=${RELEEM_MYSQL_LOGIN} --password=${RELEEM_MYSQL_PASSWORD} ping || true
            on_error
            exit 1
        fi
    fi
}

function create_postgresql_user() {
    printf "\033[37m\n * Configuring the PostgreSQL user for metrics collection.\033[0m\n"
    FLAG_SUCCESS=0
    
    if [ -n "$RELEEM_PG_PASSWORD" ] && [ -n "$RELEEM_PG_LOGIN" ]; then
        printf "\033[37m - Using PostgreSQL login and password from environment variables\033[0m\n"
        FLAG_SUCCESS=1
    else
        printf "\033[37m - Using PostgreSQL superuser for user creation.\033[0m\n"        
        # Test connection with superuser (usually postgres)
        pg_superuser="${RELEEM_PG_ROOT_LOGIN:-postgres}"            
        if PGPASSWORD=${RELEEM_PG_ROOT_PASSWORD} ${pg_root_peer_connection} $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "SELECT VERSION()" >/dev/null 2>&1; then

            printf "\033[37m - PostgreSQL connection successful.\033[0m\n"
            
            # Set default user and generate password
            RELEEM_PG_LOGIN="releem"
            RELEEM_PG_PASSWORD=$(cat /dev/urandom | tr -cd '%*)?@#~' | head -c2 ; cat /dev/urandom | tr -cd '%*)?@#~A-Za-z0-9%*)?@#~' | head -c16 ; cat /dev/urandom | tr -cd '%*)?@#~' | head -c2 )
            
            # Update the password for an existing role, otherwise create it.
            if PGPASSWORD=${RELEEM_PG_ROOT_PASSWORD} ${pg_root_peer_connection} $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -tAc "SELECT 1 FROM pg_roles WHERE rolname = '${RELEEM_PG_LOGIN}';" 2>/dev/null | grep -q "1"; then
                PGPASSWORD=${RELEEM_PG_ROOT_PASSWORD} ${pg_root_peer_connection} $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "ALTER USER ${RELEEM_PG_LOGIN} WITH PASSWORD '${RELEEM_PG_PASSWORD}';" 2>/dev/null
                printf "\033[32m   Updated password for existing PostgreSQL user \`${RELEEM_PG_LOGIN}\`\033[0m\n"
            else
                PGPASSWORD=${RELEEM_PG_ROOT_PASSWORD} ${pg_root_peer_connection} $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "CREATE USER ${RELEEM_PG_LOGIN} WITH PASSWORD '${RELEEM_PG_PASSWORD}';" 2>/dev/null
                printf "\033[32m   Created new PostgreSQL user \`${RELEEM_PG_LOGIN}\`\033[0m\n"
            fi
            
            # Grant necessary permissions
            PGPASSWORD=${RELEEM_PG_ROOT_PASSWORD} ${pg_root_peer_connection} $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "GRANT pg_monitor TO ${RELEEM_PG_LOGIN};" 2>/dev/null 
            PGPASSWORD=${RELEEM_PG_ROOT_PASSWORD} ${pg_root_peer_connection} $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "GRANT SELECT ON pg_hba_file_rules TO ${RELEEM_PG_LOGIN};" 2>/dev/null 
            PGPASSWORD=${RELEEM_PG_ROOT_PASSWORD} ${pg_root_peer_connection} $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "GRANT EXECUTE ON FUNCTION pg_hba_file_rules TO ${RELEEM_PG_LOGIN};" 2>/dev/null 


            # # Try to grant access to pg_stat_statements if available
            # if $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements';" | grep -q 1; then
            #     $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "GRANT SELECT ON pg_stat_statements TO ${RELEEM_PG_LOGIN};" 2>/dev/null || true
            #     printf "\033[37m   Granted access to pg_stat_statements extension.\033[0m\n"
            # fi
            # Check if pg_stat_statements extension is available
            FLAG_PG_STAT_STATEMENTS=1
            if PGPASSWORD=${RELEEM_PG_ROOT_PASSWORD} ${pg_root_peer_connection} $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements';" 2>/dev/null  | grep -q "1" 2>/dev/null; then
                printf "\033[32m - pg_stat_statements extension is available for query performance monitoring.\033[0m\n"
            else
                printf "\033[37m - Installing pg_stat_statements extension.\033[0m\n"
                
                if PGPASSWORD=${RELEEM_PG_ROOT_PASSWORD} ${pg_root_peer_connection} $psqlcmd ${pg_root_connection_string} -U ${pg_superuser} -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;" 2>/dev/null; then
                    printf "\033[32m   Successfully installed pg_stat_statements extension.\033[0m\n"
                else
                    FLAG_PG_STAT_STATEMENTS=0
                    printf "\033[33m   Warning: Failed to install pg_stat_statements extension. Query performance monitoring may be limited.\033[0m\n"
                fi
            fi            
            FLAG_SUCCESS=1
        else
            printf "\033[31m\n%s\n%s\033[0m\n" "PostgreSQL connection failed with superuser ${pg_superuser}." "Check that PostgreSQL is running and accessible, or set RELEEM_PG_ROOT_PASSWORD if authentication is required."
            on_error
            exit 1        
        fi
    fi
    
    # Test connection with the monitoring user
    if [ "$FLAG_SUCCESS" == "1" ]; then
        if PGPASSWORD=${RELEEM_PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${RELEEM_PG_LOGIN} -c "SELECT VERSION()" >/dev/null 2>&1; then
            printf "\033[32m\n   PostgreSQL connection with user \`${RELEEM_PG_LOGIN}\` - successful. \033[0m\n"
            PG_LOGIN=$RELEEM_PG_LOGIN
            PG_PASSWORD=$RELEEM_PG_PASSWORD

            if [ -z "${FLAG_PG_STAT_STATEMENTS+x}" ]; then
                FLAG_PG_STAT_STATEMENTS=1
                if PGPASSWORD=${RELEEM_PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${RELEEM_PG_LOGIN} -tAc "SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements';" 2>/dev/null | grep -q "1" 2>/dev/null; then
                    printf "\033[32m   pg_stat_statements extension is available for query performance monitoring.\033[0m\n"
                else
                    FLAG_PG_STAT_STATEMENTS=0
                    printf "\033[33m   Warning: pg_stat_statements extension is unavailable. Query performance monitoring may be limited.\033[0m\n"
                fi
            fi

            printf "\033[37m - Validating PostgreSQL access required for security collectors.\033[0m\n"

            if PGPASSWORD=${RELEEM_PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${RELEEM_PG_LOGIN} -t -c "SELECT 1 FROM pg_extension LIMIT 1;" >/dev/null 2>&1; then
                printf "\033[32m   Access to pg_extension is available.\033[0m\n"
            else
                printf "\033[33m   Warning: access to pg_extension is unavailable. Extension-based security checks may be incomplete.\033[0m\n"
            fi

            if PGPASSWORD=${RELEEM_PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${RELEEM_PG_LOGIN} -t -c "SELECT 1 FROM pg_roles LIMIT 1;" >/dev/null 2>&1; then
                printf "\033[32m   Access to pg_roles is available.\033[0m\n"
            else
                printf "\033[33m   Warning: access to pg_roles is unavailable. Role-based security checks may be incomplete.\033[0m\n"
            fi

            if PGPASSWORD=${RELEEM_PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${RELEEM_PG_LOGIN} -t -c "SELECT 1 FROM pg_auth_members LIMIT 1;" >/dev/null 2>&1; then
                printf "\033[32m   Access to pg_auth_members is available.\033[0m\n"
            else
                printf "\033[33m   Warning: access to pg_auth_members is unavailable. Role membership checks may be incomplete.\033[0m\n"
            fi

            if PGPASSWORD=${RELEEM_PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${RELEEM_PG_LOGIN} -t -c "SELECT * FROM pg_hba_file_rules LIMIT 1;" >/dev/null 2>&1; then
                printf "\033[32m   Access to pg_hba_file_rules is available.\033[0m\n"
            else
                printf "\033[33m   Warning: access to pg_hba_file_rules is unavailable. pg_hba-based security checks may be incomplete.\033[0m\n"
            fi

            if PGPASSWORD=${RELEEM_PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${RELEEM_PG_LOGIN} -t -c "SELECT has_schema_privilege('public', 'public', 'USAGE');" >/dev/null 2>&1; then
                printf "\033[32m   Access to schema privilege inspection is available.\033[0m\n"
            else
                printf "\033[33m   Warning: schema privilege inspection is unavailable. PUBLIC schema permission checks may be incomplete.\033[0m\n"
            fi

            if PGPASSWORD=${RELEEM_PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${RELEEM_PG_LOGIN} -t -c "SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN ('r', 'p') AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND c.relrowsecurity);" >/dev/null 2>&1; then
                printf "\033[32m   Access to RLS metadata is available.\033[0m\n"
            else
                printf "\033[33m   Warning: access to RLS metadata is unavailable. RLS reporting may be incomplete.\033[0m\n"
            fi
        else
            printf "\033[31m\n%s\n%s\033[0m\n" "PostgreSQL connection failed with user \`${RELEEM_PG_LOGIN}\`." "Check that the host, user and password are correct and the user has necessary permissions."
            PGPASSWORD=${RELEEM_PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${RELEEM_PG_LOGIN} -c "SELECT VERSION()" || true
            on_error
            exit 1
        fi
    fi
}

function configure_connection_parameters() {
    # Setting up local instance using dedicated function
    if [ "$instance_type" == "local" ]; then
        if [ "$database_type" == "postgresql" ]; then
            configure_local_postgresql_instance
        elif [ "$database_type" == "mysql" ]; then
            configure_local_mysql_instance
        fi
    fi
}

function configure_local_mysql_instance() {   
    # Step 1: Detect MySQL commands
    detect_mysql_commands
    
    # Step 2: Setup connection parameters
    setup_mysql_connection_string
}

function configure_local_postgresql_instance() {   
    # Step 1: Detect PostgreSQL commands
    detect_postgresql_commands
    
    # Step 2: Setup connection parameters
    setup_postgresql_connection_string
}

function setting_up_database_instance() {
    # Setting up local instance using dedicated function
    if [ "$instance_type" == "local" ]; then
        if [ "$database_type" == "postgresql" ]; then
            setting_up_local_postgresql_instance
        elif [ "$database_type" == "mysql" ]; then
            setting_up_local_mysql_instance
        fi
    else
        printf "\033[37m\n * Using login and password from environment variables\033[0m\n"
        if [ "$database_type" == "postgresql" ]; then
            PG_LOGIN=$RELEEM_PG_LOGIN
            PG_PASSWORD=$RELEEM_PG_PASSWORD
        elif [ "$database_type" == "mysql" ]; then
            MYSQL_LOGIN=$RELEEM_MYSQL_LOGIN
            MYSQL_PASSWORD=$RELEEM_MYSQL_PASSWORD
        fi
    fi
}

function setting_up_local_mysql_instance() {
    # Step 3: Detect MySQL service
    detect_mysql_service
    
    # Step 4: Setup configuration directory
    setup_mysql_config_directory
    
    # Step 5: Create MySQL user
    create_mysql_user    
}

function setting_up_local_postgresql_instance() {
    # Step 3: Detect PostgreSQL service
    detect_postgresql_service
    
    # Step 4: Setup configuration directory
    setup_postgresql_config_directory
    
    # Step 5: Create PostgreSQL user
    create_postgresql_user    
}

function restart_service_releem_agent() {
    printf "\033[37m\n * Starting Releem Agent service to collect metrics..\033[0m\n"
    set +e
    trap - ERR
    releem_agent_stop=$($sudo_cmd $RELEEM_WORKDIR/releem-agent  stop)
    releem_agent_start=$($sudo_cmd $RELEEM_WORKDIR/releem-agent  start)
    if [ $? -eq 0 ]; then
        printf "\033[32m\n Restarting Releem Agent - successful\033[0m\n"
    else
        echo $releem_agent_stop
        echo $releem_agent_start
        printf "\033[31m\n Restarting Releem Agent - failed\033[0m\n"
    fi
    trap on_error ERR
    set -e
    sleep 3
    releem_agent_pid=$(pgrep releem-agent || true)
    if [ -z "$releem_agent_pid" ]; then
        printf "\033[31m\n The releem-agent process was not found! Check the system log for an error.\033[0m\n"
        on_error
        exit 1;
    fi
}

function reinstall_service_releem_agent() {
    printf "\033[37m\n * Installing Releem Agent service to collect metrics..\033[0m\n"
    set +e
    trap - ERR    
    releem_agent_remove=$($sudo_cmd $RELEEM_WORKDIR/releem-agent remove)
    releem_agent_install=$($sudo_cmd $RELEEM_WORKDIR/releem-agent install)
    if [ $? -eq 0 ]; then
        printf "\033[32m\n The Releem Agent installation successful.\033[0m\n"
    else
        echo $releem_agent_remove
        echo $releem_agent_install
        printf "\033[31m\n The Releem Agent installation failed.\033[0m\n"
    fi
    trap on_error ERR
    set -e    
}    

function configure_crontab() {
    printf "\033[37m\n * Configuring crontab.\033[0m\n"
    RELEEM_CRON="00 00 * * * PATH=/bin:/sbin:/usr/bin:/usr/sbin $RELEEM_COMMAND -u"
    if [ -z "$RELEEM_CRON_ENABLE" ]; then
        printf "\033[37m Please add the following string in crontab to get recommendations:\033[0m\n"
        printf "\033[32m$RELEEM_CRON\033[0m\n\n"
        read -p "Can we do it automatically? (Y/N) " -n 1 -r
        echo    # move to a new line
        if [[ $REPLY =~ ^[Yy]$ ]]
        then
            releem_set_cron
        fi
    elif [ "$RELEEM_CRON_ENABLE" -gt 0 ]; then
        releem_set_cron
        if [ `$sudo_cmd crontab -l 2>/dev/null | grep -c "$RELEEM_WORKDIR/mysqlconfigurer.sh" || true` -eq 0 ]; then
            printf "\033[31m   Crontab configuration failed. Automatic updates are disabled.\033[0m\n"
        else
            printf "\033[32m   Crontab configuration complete. Automatic updates are enabled.\033[0m\n"
        fi
    else
        printf "\033[31m   Crontab isn't configured. Automatic updates are disabled.\033[0m\n"
    fi    
}


function configure_db_memory_limit() {
    printf "\033[37m\n * Configuring DB memory limit\033[0m\n"
    if [ -n "$RELEEM_DB_MEMORY_LIMIT" ]; then
        if [ "$RELEEM_DB_MEMORY_LIMIT" -gt 0 ]; then
            DB_MEMORY_LIMIT=$RELEEM_DB_MEMORY_LIMIT
        fi
    elif [ -n "$RELEEM_MYSQL_MEMORY_LIMIT" ]; then
        if [ "$RELEEM_MYSQL_MEMORY_LIMIT" -gt 0 ]; then
            DB_MEMORY_LIMIT=$RELEEM_MYSQL_MEMORY_LIMIT
        fi    
    else
        echo
        printf "\033[37m\n In case you are using Database in Docker or it isn't dedicated server for Database.\033[0m\n"
        read -p "Should we limit memory for Database? (Y/N) " -n 1 -r
        echo    # move to a new line
        if [[ $REPLY =~ ^[Yy]$ ]]
        then
            read -p "Please set Database Memory Limit (megabytes):" -r
            echo    # move to a new line
            DB_MEMORY_LIMIT=$REPLY
        fi
    fi
}    

function download_releem_agent() {
    $sudo_cmd rm -rf $RELEEM_WORKDIR
    # Create work directory
    if [ ! -e $RELEEM_CONF_FILE ]; then
        $sudo_cmd mkdir -p $RELEEM_WORKDIR
        $sudo_cmd mkdir -p $RELEEM_WORKDIR/conf
    fi

    printf "\033[37m\n * Downloading Releem Agent, architecture $(arch).\033[0m\n"
    $sudo_cmd curl -L -o $RELEEM_WORKDIR/releem-agent $RELEEM_AGENT_BINARY_URL
    $sudo_cmd curl -L -o $RELEEM_WORKDIR/mysqlconfigurer.sh $RELEEM_AGENT_SCRIPT_URL
    $sudo_cmd chmod 755 $RELEEM_WORKDIR/mysqlconfigurer.sh $RELEEM_WORKDIR/releem-agent
}

function configure_releem_agent() {
    printf "\033[37m\n * Saving variables to Releem Agent configuration\033[0m\n"

    printf "\033[37m - Adding API key to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
    echo "apikey=\"$apikey\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    printf "\033[37m - Adding Releem Agent directory $RELEEM_WORKDIR to Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
    echo "releem_dir=\"$RELEEM_WORKDIR\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    if [ -d "$RELEEM_WORKDIR/conf" ]; then
        printf "\033[37m - Adding Releem Configuration Directory $RELEEM_WORKDIR/conf to Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
        echo "releem_cnf_dir=\"$RELEEM_WORKDIR/conf\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    fi
    # Add database-specific configuration based on detected database type
    if [ "$database_type" == "postgresql" ]; then
        # PostgreSQL configuration
        if [ -n "$PG_LOGIN" ] && [ -n "$PG_PASSWORD" ]; then
            printf "\033[37m - Adding PostgreSQL user and password to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "pg_user=\"$PG_LOGIN\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            echo "pg_password=\"$PG_PASSWORD\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
        if [ -n "$RELEEM_PG_HOST" ]; then
            printf "\033[37m - Adding PostgreSQL host to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "pg_host=\"$RELEEM_PG_HOST\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
        if [ -n "$RELEEM_PG_PORT" ]; then
            printf "\033[37m - Adding PostgreSQL port to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "pg_port=\"$RELEEM_PG_PORT\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
        if [ -n "$RELEEM_PG_SSL_MODE" ]; then
            printf "\033[37m - Adding PostgreSQL SSL mode to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "pg_ssl_mode=\"$RELEEM_PG_SSL_MODE\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
        if [ -n "$pg_service_name_cmd" ]; then
            printf "\033[37m - Adding PostgreSQL restart command to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "pg_restart_service=\"$pg_service_name_cmd\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
        if [ -d "$PG_CONF_DIR" ]; then
            printf "\033[37m - Adding PostgreSQL conf.d directory to the Releem Agent configuration $RELEEM_CONF_FILE.\n\033[0m"
            echo "pg_cnf_dir=\"$PG_CONF_DIR\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi    
    elif [ "$database_type" == "mysql" ]; then
        # MySQL configuration (default)
        if [ -n "$MYSQL_LOGIN" ] && [ -n "$MYSQL_PASSWORD" ]; then
            printf "\033[37m - Adding user and password mysql to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "mysql_user=\"$MYSQL_LOGIN\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            echo "mysql_password=\"$MYSQL_PASSWORD\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
        if [ -n "$RELEEM_MYSQL_HOST" ]; then
            printf "\033[37m - Adding MySQL host to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "mysql_host=\"$RELEEM_MYSQL_HOST\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
        if [ -n "$RELEEM_MYSQL_PORT" ]; then
            printf "\033[37m - Adding MySQL port to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "mysql_port=\"$RELEEM_MYSQL_PORT\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
        if [ -n "$RELEEM_MYSQL_SSL_MODE" ]; then
            echo "mysql_ssl_mode=$RELEEM_MYSQL_SSL_MODE" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi    
        if [ -n "$service_name_cmd" ]; then
            printf "\033[37m - Adding MySQL restart command to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "mysql_restart_service=\"$service_name_cmd\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
        if [ -d "$MYSQL_CONF_DIR" ]; then
            printf "\033[37m - Adding MySQL include directory to the Releem Agent configuration $RELEEM_CONF_FILE.\n\033[0m"
            echo "mysql_cnf_dir=\"$MYSQL_CONF_DIR\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        fi
    fi
    if [ -n "$DB_MEMORY_LIMIT" ]; then
        printf "\033[37m - Adding Memory Limit to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
        echo "memory_limit=\"$DB_MEMORY_LIMIT\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    fi
    if [ -n "$RELEEM_HOSTNAME" ]; then
        printf "\033[37m - Adding hostname to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
        echo "hostname=\"$RELEEM_HOSTNAME\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    else
        RELEEM_HOSTNAME=$(hostname 2>&1)
        if [ $? -eq 0 ];
        then
            printf "\033[37m - Adding autodetected hostname to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "hostname=\"$RELEEM_HOSTNAME\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null        
        else
            printf "\033[31m The variable RELEEM_HOSTNAME is not defined and the hostname could not be determined automatically with error\033[0m\n $RELEEM_HOSTNAME.\n\033[0m"
        fi
    fi
    if [ -n "$RELEEM_ENV" ]; then
        echo "env=\"$RELEEM_ENV\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    fi
    if [ -n "$RELEEM_DEBUG" ]; then
        echo "debug=$RELEEM_DEBUG" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    fi
    if [ -n "$RELEEM_QUERY_OPTIMIZATION" ]; then
        printf "\033[37m - Adding query optimization parameter to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
        echo "query_optimization=$RELEEM_QUERY_OPTIMIZATION" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    fi
    if [ -n "$RELEEM_DATABASES_QUERY_OPTIMIZATION" ]; then
        printf "\033[37m - Adding list databases for query optimization ${RELEEM_DATABASES_QUERY_OPTIMIZATION} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
        echo "databases_query_optimization=\"$RELEEM_DATABASES_QUERY_OPTIMIZATION\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    fi
    if [ -n "$RELEEM_REGION" ]; then
        printf "\033[37m - Adding releem region ${RELEEM_REGION} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
        echo "releem_region=\"$RELEEM_REGION\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
    fi
    printf "\033[37m - Adding releem instance type ${instance_type} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
    echo "instance_type=\"$instance_type\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null

    if [ "$instance_type" == "aws/rds" ]; then
        if [ -n "$RELEEM_AWS_REGION" ] && [ -n "$RELEEM_AWS_RDS_DB" ] && [ -n "$RELEEM_AWS_RDS_PARAMETER_GROUP" ]; then
            printf "\033[37m - Adding AWS region ${RELEEM_AWS_REGION} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "aws_region=\"$RELEEM_AWS_REGION\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            printf "\033[37m - Adding AWS RDS DB ${RELEEM_AWS_RDS_DB} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "aws_rds_db=\"$RELEEM_AWS_RDS_DB\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            printf "\033[37m - Adding AWS RDS Parameter Group ${RELEEM_AWS_RDS_PARAMETER_GROUP} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "aws_rds_parameter_group=\"$RELEEM_AWS_RDS_PARAMETER_GROUP\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
        else
            printf "\033[31m - AWS region, AWS RDS DB or AWS RDS Parameter Group is not set. Please set the variables RELEEM_AWS_REGION, RELEEM_AWS_RDS_DB and RELEEM_AWS_RDS_PARAMETER_GROUP\033[0m\n"
            exit 1
        fi
    elif [ "$instance_type" == "gcp/cloudsql" ]; then
        if [ -n "$RELEEM_GCP_PROJECT_ID" ] && [ -n "$RELEEM_GCP_REGION" ] && [ -n "$RELEEM_GCP_CLOUDSQL_INSTANCE" ]; then
            printf "\033[37m - Adding GCP project ID ${RELEEM_GCP_PROJECT_ID} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "gcp_project_id=\"$RELEEM_GCP_PROJECT_ID\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            printf "\033[37m - Adding GCP region ${RELEEM_GCP_REGION} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "gcp_region=\"$RELEEM_GCP_REGION\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            printf "\033[37m - Adding GCP Cloud SQL instance ${RELEEM_GCP_CLOUDSQL_INSTANCE} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "gcp_cloudsql_instance=\"$RELEEM_GCP_CLOUDSQL_INSTANCE\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null        
            if [ -n "$RELEEM_GCP_CLOUDSQL_PUBLIC_CONNECTION" ]; then
                printf "\033[37m - Adding GCP Cloud SQL public connection ${RELEEM_GCP_CLOUDSQL_PUBLIC_CONNECTION} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
                echo "gcp_cloudsql_public_connection=$RELEEM_GCP_CLOUDSQL_PUBLIC_CONNECTION" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            fi        
        else
            printf "\033[31m - GCP project ID, GCP region or GCP Cloud SQL instance is not set. Please set the variables RELEEM_GCP_PROJECT_ID, RELEEM_GCP_REGION and RELEEM_GCP_CLOUDSQL_INSTANCE\033[0m\n"
            exit 1
        fi
    elif [ "$instance_type" == "azure/mysql" ]; then
        if [ -n "$RELEEM_AZURE_SUBSCRIPTION_ID" ] && [ -n "$RELEEM_AZURE_RESOURCE_GROUP" ] && [ -n "$RELEEM_AZURE_MYSQL_SERVER" ]; then
            printf "\033[37m - Adding Azure subscription ID ${RELEEM_AZURE_SUBSCRIPTION_ID} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "azure_subscription_id=\"$RELEEM_AZURE_SUBSCRIPTION_ID\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            printf "\033[37m - Adding Azure resource group ${RELEEM_AZURE_RESOURCE_GROUP} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "azure_resource_group=\"$RELEEM_AZURE_RESOURCE_GROUP\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            printf "\033[37m - Adding Azure MySQL server ${RELEEM_AZURE_MYSQL_SERVER} to the Releem Agent configuration: $RELEEM_CONF_FILE\n\033[0m"
            echo "azure_mysql_server=\"$RELEEM_AZURE_MYSQL_SERVER\"" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            if [ -z "$RELEEM_MYSQL_SSL_MODE" ]; then
                printf "\033[37m - Enabling MySQL SSL mode by default for Azure Database for MySQL: $RELEEM_CONF_FILE\n\033[0m"
                echo "mysql_ssl_mode=true" | $sudo_cmd tee -a $RELEEM_CONF_FILE >/dev/null
            fi
        else
            printf "\033[31m - Azure subscription ID, resource group or MySQL server is not set. Please set RELEEM_AZURE_SUBSCRIPTION_ID, RELEEM_AZURE_RESOURCE_GROUP and RELEEM_AZURE_MYSQL_SERVER\033[0m\n"
            exit 1
        fi
    fi
    # Secure the configuration file
    $sudo_cmd chmod 640 $RELEEM_CONF_FILE
}

function install_dependencies() {
    # OS/Distro Detection
    # Try lsb_release, fallback with /etc/issue then uname command
    KNOWN_DISTRIBUTION="(Debian|Ubuntu|RedHat|CentOS|Amazon)"
    DISTRIBUTION=$(lsb_release -d 2>/dev/null | grep -Eo $KNOWN_DISTRIBUTION  || grep -Eo $KNOWN_DISTRIBUTION /etc/issue 2>/dev/null || grep -Eo $KNOWN_DISTRIBUTION /etc/Eos-release 2>/dev/null || grep -m1 -Eo $KNOWN_DISTRIBUTION /etc/os-release 2>/dev/null || uname -s)

    if [ -f /etc/debian_version ] || [ "$DISTRIBUTION" == "Debian" ] || [ "$DISTRIBUTION" == "Ubuntu" ]; then
        OS="Debian"
    elif [ -f /etc/redhat-release ] || [ "$DISTRIBUTION" == "RedHat" ] || [ "$DISTRIBUTION" == "CentOS" ] || [ "$DISTRIBUTION" == "Amazon" ]; then
        OS="RedHat"
    # Some newer distros like Amazon may not have a redhat-release file
    elif [ -f /etc/system-release ] || [ "$DISTRIBUTION" == "Amazon" ]; then
        OS="RedHat"
    # Arista is based off of Fedora14/18 but do not have /etc/redhat-release
    elif [ -f /etc/Eos-release ] || [ "$DISTRIBUTION" == "Arista" ]; then
        OS="RedHat"
    fi

    # Install the necessary package sources
    if [ "$OS" = "RedHat" ]; then
        echo -e "\033[37m\n * Checking installed dependencies.\n\033[0m"

        if [ -x "/usr/bin/dnf" ]; then
            package_manager='dnf'
        else
            package_manager='yum'
        fi
        which curl &> /dev/null || $sudo_cmd $package_manager -y install curl
        which crontab &> /dev/null || $sudo_cmd $package_manager -y install cronie
    elif [ "$OS" = "Debian" ]; then
        printf "\033[37m\n * Checking installed dependencies.\n\033[0m\n"
        which curl &> /dev/null || ($sudo_cmd apt-get update ; $sudo_cmd apt-get install -y --force-yes curl)
        which crontab &> /dev/null || ($sudo_cmd apt-get update ; $sudo_cmd apt-get install -y --force-yes cron)
    else
        printf "\033[31mYour OS or distribution are not supported by this install script.\033[0m\n"
        exit 0
    fi
}    

# Detect API key based on environment variables
function detect_releem_api_key() {
    apikey=
    if [ -n "$RELEEM_API_KEY" ]; then
        apikey=$RELEEM_API_KEY
    else
        if test -f $RELEEM_CONF_FILE ; then
            . $RELEEM_CONF_FILE
        fi
    fi
    if [ ! "$apikey" ]; then
        printf "\033[31mReleem API key is not available in RELEEM_API_KEY environment variable. Please sign up at https://releem.com\033[0m\n"
        on_error
        exit 1;
    fi    
}

function first_run_releem_agent() {
    set +e
    trap - ERR
    if [ -z "$RELEEM_AGENT_DISABLE" ]; then
        # First run of Releem Agent to check Queries monitoring
        printf "\033[37m\n * Executing Releem Agent for the first time.\033[0m\n"
        printf "\033[37m This may take up to 15 minutes on servers with many databases.\033[0m\n\n"
        $sudo_cmd $RELEEM_WORKDIR/releem-agent -f
        $sudo_cmd timeout --preserve-status 10 $RELEEM_WORKDIR/releem-agent
    fi
    trap on_error ERR
    set -e
}

function enable_collect_queries() {
    # Enable monitoring of queries for local instances
    if [ "$instance_type" == "local" ]; then
        if [ "$database_type" == "postgresql" ]; then
            if [ "$FLAG_PG_STAT_STATEMENTS" -eq 1 ]; then
                $sudo_cmd $RELEEM_COMMAND -p
            else
                printf "\033[31m\n pg_stat_statements extension is not enabled. \n Please install the postgresql-contrib package for your version of Postgresql and reinstall the Releem Agent.\033[0m\n"
                exit 1
            fi
        elif [ "$database_type" == "mysql" ]; then
            $sudo_cmd $RELEEM_COMMAND -p
        fi
    fi
}    

function main() {
    detect_releem_api_key
    detect_instance_type
    detect_database_type
    configure_connection_parameters
    install_dependencies
    download_releem_agent
    configure_db_memory_limit
    setting_up_database_instance
    configure_releem_agent
    configure_crontab
    enable_collect_queries
    first_run_releem_agent
    reinstall_service_releem_agent
    restart_service_releem_agent
}

if [[ -n "${RELEEM_TEST_MODE}" && "${BASH_SOURCE[0]}" != "$0" ]]; then
    return 0
fi

# Parse parameters
while getopts "u" option
do
case "${option}"
in
u) releem_update;;
esac
done

INSTALL_MODE="$(basename "$0")"
if [ "$INSTALL_MODE" == "uninstall" ] || [ "$1" == "uninstall" ];
then
    trap - EXIT
    apikey=
    if [ -n "$RELEEM_API_KEY" ]; then
        apikey=$RELEEM_API_KEY
    elif test -f "$RELEEM_CONF_FILE" ; then
        . "$RELEEM_CONF_FILE"
    fi
    if [ -n "$apikey" ] && [ -x "$RELEEM_WORKDIR/releem-agent" ]; then
        $RELEEM_WORKDIR/releem-agent --event=agent_uninstall > /dev/null || true
    fi
    printf "\033[37m\n * Configuring crontab\033[0m\n"
    ($sudo_cmd crontab -l 2>/dev/null | grep -v "$RELEEM_WORKDIR/mysqlconfigurer.sh" || true) | $sudo_cmd crontab -
    printf "\033[37m\n * Stopping Releem Agent service.\033[0m\n"
    releem_agent_stop=$($sudo_cmd $RELEEM_WORKDIR/releem-agent  stop)
    if [ $? -eq 0 ]; then
        printf "\033[32m\n Releem Agent stopped successfully.\033[0m\n"
    else
        echo $releem_agent_stop
        printf "\033[31m\n Releem Agent failed to stop.\033[0m\n"
    fi
    printf "\033[37m\n * Uninstalling Releem Agent service.\033[0m\n"
    releem_agent_remove=$($sudo_cmd $RELEEM_WORKDIR/releem-agent remove)
    if [ $? -eq 0 ]; then
        printf "\033[32m\n Releem Agent uninstalled successfully.\033[0m\n"
    else
        echo $releem_agent_remove
        printf "\033[31m\n Releem Agent failed to  uninstall.\033[0m\n"
    fi
    printf "\033[37m\n * Removing files Releem Agent\033[0m\n"
    $sudo_cmd rm -rf $RELEEM_WORKDIR
    exit 0

elif [ "$INSTALL_MODE" == "update" ] || [ "$1" == "update" ];
then
    releem_update

elif [ "$INSTALL_MODE" == "enable_query_optimization" ] || [ "$1" == "enable_query_optimization" ];
then
    #Enable Query Optimitsation
    detect_releem_api_key
    detect_instance_type
    detect_database_type
    configure_connection_parameters

    grant_privileges_sql=$($mysqlcmd  ${root_connection_string} --user=root --password=${RELEEM_MYSQL_ROOT_PASSWORD} -NBe 'select Concat("GRANT SELECT on *.* to `",User,"`@`", Host,"`;") from mysql.user where User="releem"')
    for query in  "${grant_privileges_sql[@]}";
    do
        echo "${query}"
        $mysqlcmd  ${root_connection_string} --user=root --password=${RELEEM_MYSQL_ROOT_PASSWORD} -Be "${query}"
    done
    if [ -z "$query_optimization" ]; then
        echo "query_optimization=true" | $sudo_cmd tee -a $RELEEM_CONF_FILE
    else
        $sudo_cmd sed -i 's/query_optimization=.*/query_optimization=true/g' $RELEEM_CONF_FILE
    fi
    restart_service_releem_agent
    # Enable perfomance schema
    $sudo_cmd $RELEEM_COMMAND -p
    exit 0

else
    main
    printf "\033[37m\n\033[0m"
    printf "\033[37m * Releem Agent has been successfully installed.\033[0m\n"
    printf "\033[37m\n\033[0m"
    printf "\033[37m * To view Releem recommendations and Database metrics, visit https://app.releem.com/dashboard\033[0m"
    printf "\033[37m\n\033[0m"
fi
