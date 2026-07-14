#!/usr/bin/env bash
# mysqlconfigurer.sh - Version 1.24.0
# (C) Releem, Inc 2022
# All rights reserved

export PATH=$PATH:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin
export EXTEND_TIMEOUT_USEC=18000000000

# Set sudo command
if [ "$EUID" -ne 0 ]; then
    sudo_cmd="sudo"
else
    sudo_cmd=""
fi

# Variables
VERSION="1.24.0"
RELEEM_WORKDIR="${RELEEM_WORKDIR:-/opt/releem}"
RELEEM_CONF_DIR="$RELEEM_WORKDIR/conf/"
RELEEM_CONF_FILE="$RELEEM_WORKDIR/releem.conf"
RELEEM_DB_VERSION_FILE="${RELEEM_CONF_DIR}db_version"
RELEEM_INSTALL_FILE="${RELEEM_CONF_DIR}install.sh"
DB_MEMORY_LIMIT=0
RELEEM_AGENT_BINARY_URL="https://releem.s3.amazonaws.com/v2/releem-agent-$(arch)"
RELEEM_AGENT_SCRIPT_URL="https://releem.s3.amazonaws.com/v2/mysqlconfigurer.sh"
RELEEM_AGENT_INSTALL_SCRIPT_URL="https://releem.s3.amazonaws.com/v2/install.sh"


logfile="/var/log/releem-mysqlconfigurer.log"
npipe=""

function setup_logging() {
    if [ -n "$RELEEM_TEST_MODE" ]; then
        return 0
    fi
    # Set up a named pipe for logging 
    npipe=/tmp/$$.mysqlconfigurer.tmp
    mknod "$npipe" p
    # Log all output to a log for error checking
    tee <"$npipe" "$logfile" &
    exec 1>&-
    exec 1>"$npipe" 2>&1
}

function on_exit() {
    if [ -n "$RELEEM_TEST_MODE" ]; then
        return 0
    fi
    if [[ "${RELEEM_REGION}" == "EU" ]]; then
        API_DOMAIN="api.queries.eu.releem.com"
    else
        API_DOMAIN="api.queries.releem.com"
    fi
    curl -s -L -d @"$logfile" -H "x-releem-api-key: $RELEEM_API_KEY" -H "Content-Type: application/json" -X POST "https://${API_DOMAIN}/v2/events/configurer_log"
    [ -n "$npipe" ] && rm -f "$npipe"
}
trap on_exit EXIT

function update_agent() {
    trap - EXIT
    $RELEEM_WORKDIR/releem-agent start > /dev/null || true
    NEW_VER=$(curl  -s -L https://releem.s3.amazonaws.com/v2/current_version_agent)
    if [ "$NEW_VER" != "$VERSION" ]; then
        if [ "$(printf '%s\n' "$NEW_VER" "$VERSION" | sort -V | head -n1)" = "$VERSION" ];
        then
            printf "\033[37m\n * Updating script \e[31;1m%s\e[0m -> \e[32;1m%s\e[0m\n" "$VERSION" "$NEW_VER"
            curl -s -L $RELEEM_AGENT_INSTALL_SCRIPT_URL > "$RELEEM_INSTALL_FILE"
            RELEEM_INSTANCE_TYPE=$RELEEM_INSTANCE_TYPE RELEEM_API_KEY=$RELEEM_API_KEY exec bash "$RELEEM_INSTALL_FILE" update
            $RELEEM_WORKDIR/releem-agent --event=agent_updated > /dev/null
        fi
    fi
}

function non_blocking_wait() {
    PID=$1
    if [ ! -d "/proc/$PID" ]; then
        wait $PID
        CODE=$?
    else
        CODE=150
    fi
    return $CODE
}


function wait_restart() {
  sleep 1
  flag=0
  spin[0]="-"
  spin[1]="\\"
  spin[2]="|"
  spin[3]="/"
  printf "\033[37m\n Waiting for service to start 1200 seconds ${spin[0]}"
  while /bin/true; do
    PID=$1
    non_blocking_wait $PID
    CODE=$?
    if [ $CODE -ne 150 ]; then
        printf "\033[0m\n PID $PID terminated with exit code $CODE"
        if [ $CODE -eq 0 ]; then
                RETURN_CODE=0
        else
                RETURN_CODE=7
        fi
        break
    fi
    flag=$(($flag + 1))
    if [ $flag == 1200 ]; then
        RETURN_CODE=6
        break
    fi
    i=`expr $flag % 4`
    printf "\b${spin[$i]}"
    sleep 1
  done
  printf "\033[0m\n"
  return $RETURN_CODE
}


function check_db_version() {
    if [ "$DATABASE_TYPE" == "postgresql" ];
    then
        return 0
    fi

    if [ -f "$RELEEM_DB_VERSION_FILE" ]; then
        db_version=$(cat $RELEEM_DB_VERSION_FILE)
    else
        printf "\033[37m\n * Please try again later or run Releem Agent manually:\033[0m"
        printf "\033[32m\n  $RELEEM_WORKDIR/releem-agent -f \033[0m\n\n"
        exit 1;
    fi
    if [ -z $db_version ]; then
        printf "\033[37m\n * Please try again later or run Releem Agent manually:\033[0m"
        printf "\033[32m\n $RELEEM_WORKDIR/releem-agent -f \033[0m\n\n"
        exit 1;
    fi
    requiredver="5.6.8"
    if [ "$(printf '%s\n' "$db_version" "$requiredver" | sort -V | head -n1)" = "$requiredver" ]; then
        return 0
    else
        return 1
    fi
}


function releem_rollback_config() {
    printf "\033[31m\n * Rolling back ${DATABASE_NAME} configuration.\033[0m\n"
    if [ "$DATABASE_TYPE" == "mysql" ]; then
        if ! check_db_version; then        
                printf "\033[31m\n * MySQL version is lower than 5.6.7. Check the documentation https://github.com/Releem/mysqlconfigurer#how-to-apply-the-recommended-configuration for applying the configuration. \033[0m\n"        
            exit 2
        fi
    fi
    if [ -z "$RELEEM_DB_CONFIG_DIR" -o ! -d "$RELEEM_DB_CONFIG_DIR" ]; then
        printf "\033[37m\n * ${DATABASE_NAME} configuration directory was not found.\033[0m"
        printf "\033[37m\n * Try to reinstall Releem Agent, and set the my.cnf location.\033[0m"
        exit 3;
    fi

    FLAG_RESTART_SERVICE=1
    if [ -z "$RELEEM_RESTART_SERVICE" ]; then
    	read -p "Restart ${DATABASE_NAME} service? (Y/N) " -n 1 -r
      echo    # move to a new line
      if [[ ! $REPLY =~ ^[Yy]$ ]]
      then
        printf "\033[37m\n * Confirmation to restart the service has not been received. Releem recommended configuration has not been rolled back.\033[0m\n"
        FLAG_RESTART_SERVICE=0
      fi
    elif [ "$RELEEM_RESTART_SERVICE" -eq 0 ]; then
      FLAG_RESTART_SERVICE=0
    fi
    if [ "$FLAG_RESTART_SERVICE" -eq 0 ]; then
        exit 5
    fi

    printf "\033[31m\n * Deleting the configuration file. \033[0m\n"
    rm -rf $RELEEM_DB_CONFIG_DIR/$RELEEM_DB_CONFIG_FILE_NAME
    #echo "----Test config-------"
    if [ -f "${RELEEM_CONF_DIR}${RELEEM_DB_CONFIG_FILE_NAME}.bkp" ]; then
        printf "\033[31m\n * Restoring the backup copy of the configuration file ${RELEEM_CONF_DIR}${RELEEM_DB_CONFIG_FILE_NAME}.bkp. \033[0m\n"
        cp -f "${RELEEM_CONF_DIR}${RELEEM_DB_CONFIG_FILE_NAME}.bkp" "${RELEEM_DB_CONFIG_DIR}/${RELEEM_DB_CONFIG_FILE_NAME}"
    fi

    if [ -z "$RELEEM_COMMAND_RESTART_SERVICE" ]; then
        printf "\033[37m\n * The command to restart the ${DATABASE_NAME} service was not found. Try to reinstall Releem Agent.\033[0m"
        exit 4;
    fi
    printf "\033[31m\n * Restarting ${DATABASE_NAME} with command '$RELEEM_COMMAND_RESTART_SERVICE'.\033[0m\n"
    eval "$RELEEM_COMMAND_RESTART_SERVICE" &
    wait_restart $!
    RESTART_CODE=$?

    if [ $RESTART_CODE -eq 0 ];
    then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m The ${DATABASE_NAME} service restarted successfully!\033[0m\n"
        rm -f "${RELEEM_CONF_DIR}${RELEEM_DB_CONFIG_FILE_NAME}.bkp"
    elif [ $RESTART_CODE -eq 6 ];
    then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m The ${DATABASE_NAME} service failed to restart in 1200 seconds. Check the ${DATABASE_NAME} error log. \033[0m\n"
    elif [ $RESTART_CODE -eq 7 ];
    then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m The ${DATABASE_NAME} service failed to restart. Check the ${DATABASE_NAME} error log. \033[0m\n" 
    fi
    $RELEEM_WORKDIR/releem-agent --event=config_rollback > /dev/null
    exit "${RESTART_CODE}"
}



function releem_configure_database() {
    printf "\033[37m\n * Detected database type: $DATABASE_TYPE\033[0m\n"
    
    if [ "$DATABASE_TYPE" == "mysql" ]; then
        releem_configure_mysql
    elif [ "$DATABASE_TYPE" == "postgresql" ]; then
        releem_configure_postgresql_libraries
        releem_configure_postgresql
    else
        printf "\033[31m\n * Unknown database type: $DATABASE_TYPE. Supported types: mysql, postgresql\033[0m\n"
        printf "\033[37m\n * Please configure either mysql_user/mysql_password or pg_user/pg_password in releem.conf\033[0m\n"
        exit 1
    fi
}


function releem_configure_mysql() {
    FLAG_CONFIGURE=1
    status_ps=$($mysqlcmd  ${mysql_connection_string}  --user=${MYSQL_LOGIN} --password=${MYSQL_PASSWORD} -BNe "show global variables like 'performance_schema'" 2>/dev/null | awk '{print $2}')
    if [ "$status_ps" != "ON" ]; then
        FLAG_CONFIGURE=0
    fi

    status_slowlog=$($mysqlcmd  ${mysql_connection_string}  --user=${MYSQL_LOGIN} --password=${MYSQL_PASSWORD} -BNe "show global variables like 'slow_query_log'" 2>/dev/null | awk '{print $2}')
    if [ "$status_slowlog" != "ON" ]; then
        FLAG_CONFIGURE=0
    fi

    ps_digest_size=$($mysqlcmd  ${mysql_connection_string}  --user=${MYSQL_LOGIN} --password=${MYSQL_PASSWORD} -BNe "show global variables like 'performance_schema_digests_size'" 2>/dev/null | awk '{print $2}')
    if [ "$ps_digest_size" -lt 10000 ]; then
        FLAG_CONFIGURE=0
    fi

    if [ -z "$RELEEM_DB_CONFIG_DIR" ] || [ ! -d "$RELEEM_DB_CONFIG_DIR" ]; then
        printf "\033[31m\n MySQL configuration directory was not found.\n Try to reinstall Releem Agent.\033[0m"
        exit 3;
    fi
    if [ -f "$MYSQL_MY_CNF_PATH" ]; then
        if [ `$sudo_cmd grep -cE "!includedir $RELEEM_DB_CONFIG_DIR" $MYSQL_MY_CNF_PATH` -eq 0 ]; then
            printf "\033[31m\n Directive includedir was not found in the MySQL configuration file $MYSQL_MY_CNF_PATH.\n Try to reinstall Releem Agent.\n\033[0m"
            exit 11;
        fi
    fi
    printf "\033[37m\n * Enabling and configuring Performance schema and SlowLog to collect metrics and queries.\n\033[0m"
    echo "### This configuration was recommended by Releem. https://releem.com" | $sudo_cmd tee "$RELEEM_DB_CONFIG_DIR/collect_metrics.cnf" >/dev/null
    echo "[mysqld]" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.cnf" >/dev/null
    echo "performance_schema = 1" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.cnf" >/dev/null
    echo "slow_query_log = 1" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.cnf" >/dev/null
    if [ "$ps_digest_size" -le 10000 ]; then
        echo "performance_schema_digests_size = 10000" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.cnf" >/dev/null
    fi
    if [ -n "$RELEEM_QUERY_OPTIMIZATION" -a "$RELEEM_QUERY_OPTIMIZATION" = true ]; then
        performance_schema_setup_consumers_events_statements_current=$($mysqlcmd ${mysql_connection_string}  --user=${MYSQL_LOGIN} --password=${MYSQL_PASSWORD} -BNe "SELECT ENABLED FROM performance_schema.setup_consumers WHERE NAME = 'events_statements_current';" 2>/dev/null )
        performance_schema_setup_consumers_events_statements_history=$($mysqlcmd ${mysql_connection_string}  --user=${MYSQL_LOGIN} --password=${MYSQL_PASSWORD} -BNe "SELECT ENABLED FROM performance_schema.setup_consumers WHERE NAME = 'events_statements_history';" 2>/dev/null )
        # performance_schema_events_statements_history_size=$($mysqlcmd  ${mysql_connection_string}  --user=${MYSQL_LOGIN} --password=${MYSQL_PASSWORD} -BNe "show global variables like 'performance_schema_events_statements_history_size'" 2>/dev/null | awk '{print $2}')

        if [ "$performance_schema_setup_consumers_events_statements_current" != "YES" ]; then
            FLAG_CONFIGURE=0
        fi
        if [ "$performance_schema_setup_consumers_events_statements_history" != "YES" ]; then
            FLAG_CONFIGURE=0
        fi
        # if [ "$performance_schema_events_statements_history_size" != "150" ]; then
        #     FLAG_CONFIGURE=0
        # fi         
        echo "performance-schema-consumer-events-statements-history = ON" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.cnf" >/dev/null
        echo "performance-schema-consumer-events-statements-current = ON" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.cnf" >/dev/null
        # echo "performance_schema_events_statements_history_size = 500" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.cnf" >/dev/null
    fi        
    chmod 644 $RELEEM_DB_CONFIG_DIR/collect_metrics.cnf

    if [ "$FLAG_CONFIGURE" -eq 1 ]; then
        printf "\033[37m\n * Performance schema and SlowLog are enabled and configured to collect metrics and queries.\033[0m\n"
        exit 0
    fi
    printf "\033[37m\n To apply changes to the MySQL configuration, you need to restart the service\n\033[0m\n"
    FLAG_RESTART_SERVICE=1
    if [ -z "$RELEEM_RESTART_SERVICE" ]; then
        read -p " Restart MySQL service? (Y/N) " -n 1 -r
        echo    # move to a new line
        if [[ ! $REPLY =~ ^[Yy]$ ]]
        then
            printf "\033[31m\n Confirmation to restart the service has not been received. \033[0m\n"
            FLAG_RESTART_SERVICE=0
        fi
    elif [ "$RELEEM_RESTART_SERVICE" -eq 0 ]; then
        FLAG_RESTART_SERVICE=0
    fi
    if [ "$FLAG_RESTART_SERVICE" -eq 0 ]; then
        printf "\033[31m\n For appling change in configuration MySQL need to restart service.\n Run the command \`bash $RELEEM_WORKDIR/mysqlconfigurer.sh -p\` when it is possible to restart the service.\033[0m\n"
        exit 0
    fi
    #echo "-------Test config-------"
    printf "\033[37m\n Restarting MySQL service with command '$RELEEM_COMMAND_RESTART_SERVICE'.\033[0m\n"
    eval "$RELEEM_COMMAND_RESTART_SERVICE" &
    wait_restart $!
    RESTART_CODE=$?

    #if [[ $($mysqladmincmd  ${mysql_connection_string}  --user=${MYSQL_LOGIN} --password=${MYSQL_PASSWORD} ping 2>/dev/null || true) == "mysqld is alive" ]];
    if [ $RESTART_CODE -eq 0 ];
    then
        printf "\033[32m\n The MySQL service restarted successfully!\n Performance schema and SlowLog are enabled and configured to collect metrics and queries.\033[0m\n"
    elif [ $RESTART_CODE -eq 6 ];
    then
        printf "\033[31m\n The MySQL service failed to restart in 1200 seconds. Check the MySQL error log.\033[0m\n"
    elif [ $RESTART_CODE -eq 7 ];
    then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m The MySQL service failed to restart with error. Check the MySQL error log. \033[0m\n" 
    fi
    
    exit "${RESTART_CODE}"
}

function releem_configure_postgresql_libraries() {
    FLAG_CONFIGURE=1    
    shared_preload_libraries=$(PGPASSWORD=${PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${PG_LOGIN} -t -c "SHOW shared_preload_libraries;" 2>/dev/null | xargs)
    if [[ "$shared_preload_libraries" != *"pg_stat_statements"* ]]; then
        FLAG_CONFIGURE=0
    fi    
    printf "\033[37m\n * Enabling PostgreSQL Library pg_stat_statements for performance monitoring.\033[0m\n"
    if [ -z "$RELEEM_DB_CONFIG_DIR" ] || [ ! -d "$RELEEM_DB_CONFIG_DIR" ]; then
        printf "\033[31m\n PostgreSQL configuration directory was not found: $RELEEM_DB_CONFIG_DIR\033[0m\n"
        printf "\033[37m\n * Please ensure PostgreSQL is installed and the configuration directory exists.\033[0m\n"    
        exit 3;
    fi    
    echo "# This configuration was recommended by Releem. https://releem.com" | $sudo_cmd tee "$RELEEM_DB_CONFIG_DIR/collect_metrics.conf" >/dev/null
    echo "# PostgreSQL configuration for Releem metrics collection" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.conf" >/dev/null
    echo "" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.conf" >/dev/null
        
    if [[ "$shared_preload_libraries" == *"pg_stat_statements"* ]]; then
        echo "shared_preload_libraries = '${shared_preload_libraries}'" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.conf" >/dev/null
    else    
        if [ "$shared_preload_libraries" == "" ]; then
            echo "shared_preload_libraries = 'pg_stat_statements'" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.conf" >/dev/null
        else
            echo "shared_preload_libraries = '${shared_preload_libraries}, pg_stat_statements'" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.conf" >/dev/null
        fi
    fi
    chmod 644 $RELEEM_DB_CONFIG_DIR/collect_metrics.conf

    if [ "$FLAG_CONFIGURE" -eq 1 ]; then
        printf "\033[37m\n   The Extention pg_stat_statements is enabled to collect metrics and queries.\033[0m\n"
        return
    fi
    printf "\033[37m To apply changes to the Postgresql configuration, you need to restart the service\n\033[0m\n"
    FLAG_RESTART_SERVICE=1
    if [ -z "$RELEEM_RESTART_SERVICE" ]; then
        read -p " Restart Postgresql service? (Y/N) " -n 1 -r
        echo    # move to a new line
        if [[ ! $REPLY =~ ^[Yy]$ ]]
        then
            printf "\033[31m Confirmation to restart the service has not been received. \033[0m\n"
            FLAG_RESTART_SERVICE=0
        fi
    elif [ "$RELEEM_RESTART_SERVICE" -eq 0 ]; then
        FLAG_RESTART_SERVICE=0
    fi
    if [ "$FLAG_RESTART_SERVICE" -eq 0 ]; then
        printf "\033[31m\n For appling change in configuration Postgresql need to restart service.\n Run the command \`bash $RELEEM_WORKDIR/mysqlconfigurer.sh -p\` when it is possible to restart the service.\033[0m\n"
        exit 0
    fi
    #echo "-------Test config-------"
    printf "\033[37m Restarting Postgresql service with command '$RELEEM_COMMAND_RESTART_SERVICE'.\033[0m\n"
    eval "$RELEEM_COMMAND_RESTART_SERVICE" &
    wait_restart $!
    RESTART_CODE=$?

    if [ $RESTART_CODE -eq 0 ];
    then
        printf "\033[32m\n The Postgresql service restarted successfully!\n The Extention pg_stat_statements is enabled to collect metrics and queries.\033[0m\n"
    elif [ $RESTART_CODE -eq 6 ];
    then
        printf "\033[31m\n The Postgresql service failed to restart in 1200 seconds. Check the Postgresql error log.\033[0m\n"
        exit "${RESTART_CODE}"
    elif [ $RESTART_CODE -eq 7 ];
    then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m The Postgresql service failed to restart with error. Check the Postgresql error log. \033[0m\n" 
        exit "${RESTART_CODE}"
    fi
}

function releem_configure_postgresql() {
    FLAG_CONFIGURE=1
    
    # Check if logging is properly configured
    pg_stat_statements_track=$(PGPASSWORD=${PG_PASSWORD} $psqlcmd ${pg_connection_string} -U ${PG_LOGIN} -t -c "SHOW pg_stat_statements.track;"  | xargs)
    if [ "$pg_stat_statements_track" != "all" ]; then
        FLAG_CONFIGURE=0
    fi

    pg_stat_statements_max=$(PGPASSWORD=${PG_PASSWORD} $psqlcmd ${pg_connection_string}  -U ${PG_LOGIN} -t -c "SHOW pg_stat_statements.max;" | xargs)
    if [ "$pg_stat_statements_max" -lt 10000 ]; then
        FLAG_CONFIGURE=0
    fi


    printf "\033[37m\n * Configuring PostgreSQL to collect metrics and queries.\033[0m\n"
    if [ -z "$RELEEM_DB_CONFIG_DIR" ] || [ ! -d "$RELEEM_DB_CONFIG_DIR" ]; then
        printf "\033[31m\n The PostgreSQL configuration directory was not found: $RELEEM_DB_CONFIG_DIR\033[0m\n"
        printf "\033[37m\n Please ensure PostgreSQL is installed and the configuration directory exists.\033[0m\n"    
        exit 3;
    fi    
    echo "pg_stat_statements.track = all" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.conf" >/dev/null
    if [ "$pg_stat_statements_max" -le 10000 ]; then
        echo "pg_stat_statements.max = 10000" | $sudo_cmd tee -a "$RELEEM_DB_CONFIG_DIR/collect_metrics.conf" >/dev/null
    fi
    chmod 644 $RELEEM_DB_CONFIG_DIR/collect_metrics.conf

    if [ "$FLAG_CONFIGURE" -eq 1 ]; then
        printf "\033[37m\n   The PostgreSQL is configured to collect metrics and queries.\033[0m\n"
        exit 0
    fi
    printf "\033[37m To apply changes to the Postgresql configuration, you need to restart the service\n\033[0m\n"
    FLAG_RESTART_SERVICE=1
    if [ -z "$RELEEM_RESTART_SERVICE" ]; then
        read -p " Restart Postgresql service? (Y/N) " -n 1 -r
        echo    # move to a new line
        if [[ ! $REPLY =~ ^[Yy]$ ]]
        then
            printf "\033[31m Confirmation to restart the service has not been received. \033[0m\n"
            FLAG_RESTART_SERVICE=0
        fi
    elif [ "$RELEEM_RESTART_SERVICE" -eq 0 ]; then
        FLAG_RESTART_SERVICE=0
    fi
    if [ "$FLAG_RESTART_SERVICE" -eq 0 ]; then
        printf "\033[31m\n For appling change in configuration Postgresql need to restart service.\n Run the command \`bash $RELEEM_WORKDIR/mysqlconfigurer.sh -p\` when it is possible to restart the service.\033[0m\n"
        exit 0
    fi
    #echo "-------Test config-------"
    printf "\033[37m Restarting Postgresql service with command '$RELEEM_COMMAND_RESTART_SERVICE'.\033[0m\n"
    eval "$RELEEM_COMMAND_RESTART_SERVICE" &
    wait_restart $!
    RESTART_CODE=$?

    if [ $RESTART_CODE -eq 0 ];
    then
        printf "\033[32m\n The Postgresql service restarted successfully!\n The PostgreSQL is configured to collect metrics and queries.\033[0m\n"
    elif [ $RESTART_CODE -eq 6 ];
    then
        printf "\033[31m\n The Postgresql service failed to restart in 1200 seconds. Check the Postgresql error log.\033[0m\n"
    elif [ $RESTART_CODE -eq 7 ];
    then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m The Postgresql service failed to restart with error. Check the Postgresql error log. \033[0m\n" 
    fi

    exit "${RESTART_CODE}"
}


function releem_apply_config() {
    if [ "$1" == "auto" ];
    then
        releem_apply_auto
    elif [ "$1" == "automatic" ]; 
    then
        releem_apply_automatic
    elif [ "$1" == "initial" ]; 
    then        
        releem_apply_automatic "initial"
    else
        releem_apply_manual
    fi
}
 
function releem_apply_auto() {
    $RELEEM_WORKDIR/releem-agent --task=apply_config > /dev/null
    printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m Sending request to create a job to apply the configuration.\033[0m\n"
    exit 0
}

function releem_apply_manual() {
    if [ "$1" == "initial" ]; then
        DB_CONFIG_FILE_NAME="${RELEEM_DB_INITIAL_CONFIG_FILE_NAME}"
        DB_CONFIG_FILE="${RELEEM_DB_INITIAL_CONFIG_FILE}"
    else
        DB_CONFIG_FILE_NAME="${RELEEM_DB_CONFIG_FILE_NAME}"
        DB_CONFIG_FILE="${RELEEM_DB_CONFIG_FILE}"
    fi    
    if [ "$DATABASE_TYPE" == "mysql" ]; then
        if ! check_db_version; then        
                printf "\033[31m\n * MySQL version is lower than 5.6.7. Check the documentation https://github.com/Releem/mysqlconfigurer#how-to-apply-the-recommended-configuration for applying the configuration. \033[0m\n"        
            exit 2
        fi
        if [ -f "$MYSQL_MY_CNF_PATH" ]; then
            if [ `$sudo_cmd grep -cE "!includedir $RELEEM_DB_CONFIG_DIR" $MYSQL_MY_CNF_PATH` -eq 0 ]; then
                printf "\033[31m\n Directive includedir was not found in the MySQL configuration file $MYSQL_MY_CNF_PATH.\n Try to reinstall Releem Agent.\n\033[0m"
                exit 11;
            fi
        fi        
    fi
    if [ -z "$RELEEM_DB_CONFIG_DIR" -o ! -d "$RELEEM_DB_CONFIG_DIR" ]; then
        printf "\033[37m\n * ${DATABASE_NAME} configuration directory was not found.\033[0m"
        printf "\033[37m\n * Try to reinstall Releem Agent, and please set the my.cnf location.\033[0m"
        exit 3;
    fi

    printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Applying the recommended ${DATABASE_NAME} configuration.\033[0m\n"
    printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Getting the latest up-to-date configuration.\033[0m\n"
    $RELEEM_WORKDIR/releem-agent -c >/dev/null 2>&1 || true
    if [ ! -f $RELEEM_DB_CONFIG_FILE ]; then
        printf "\033[37m\n * Recommended ${DATABASE_NAME} configuration was not found.\033[0m"
        printf "\033[37m\n * Please apply recommended configuration later or run Releem Agent manually:\033[0m"
        printf "\033[32m\n $RELEEM_WORKDIR/releem-agent -f \033[0m\n\n"
        exit 1;
    fi
    diff_cmd=$(which diff || true)
    if [ -n "$diff_cmd" ];then
        diff "${RELEEM_DB_CONFIG_DIR}/${RELEEM_DB_CONFIG_FILE_NAME}" "$RELEEM_DB_CONFIG_FILE" > /dev/null 2>&1
        retVal=$?
        if [ $retVal -eq 0 ];
        then
            printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m The new configuration is identical to the current configuration. No restart is required!\033[0m\n"
            exit 0
        fi
    fi

    FLAG_RESTART_SERVICE=1
    if [ -z "$RELEEM_RESTART_SERVICE" ]; then
      read -p "Restart ${DATABASE_NAME} service? (Y/N) " -n 1 -r
      echo    # move to a new line
      if [[ ! $REPLY =~ ^[Yy]$ ]]
      then
          printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Confirmation to restart the service has not been received. Releem recommended configuration has not been applied.\033[0m\n"
          FLAG_RESTART_SERVICE=0
      fi
    elif [ "$RELEEM_RESTART_SERVICE" -eq 0 ]; then
        FLAG_RESTART_SERVICE=0
    fi
    if [ "$FLAG_RESTART_SERVICE" -eq 0 ]; then
        exit 5
    fi

    printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Copying file $RELEEM_DB_CONFIG_FILE to directory $RELEEM_DB_CONFIG_DIR/.\033[0m\n"
    if [ ! -f "${RELEEM_CONF_DIR}${RELEEM_DB_CONFIG_FILE_NAME}.bkp" ]; then
        yes | cp -f "${RELEEM_DB_CONFIG_DIR}/${RELEEM_DB_CONFIG_FILE_NAME}" "${RELEEM_CONF_DIR}${RELEEM_DB_CONFIG_FILE_NAME}.bkp"
    fi    
    yes | cp -fr $RELEEM_DB_CONFIG_FILE $RELEEM_DB_CONFIG_DIR/
    chmod 644 $RELEEM_DB_CONFIG_DIR/*

    if [ -z "$RELEEM_COMMAND_RESTART_SERVICE" ]; then
        printf "\033[37m\n * The command to restart the ${DATABASE_NAME} service was not found. Try to reinstall Releem Agent.\033[0m"
        exit 4;
    fi

    #echo "-------Test config-------"
    printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Restarting ${DATABASE_NAME} with the command '$RELEEM_COMMAND_RESTART_SERVICE'.\033[0m\n"
    eval "$RELEEM_COMMAND_RESTART_SERVICE" &
    wait_restart $!
    RESTART_CODE=$?

    if [ $RESTART_CODE -eq 0 ];
    then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m The ${DATABASE_NAME} service restarted successfully!\033[0m\n"
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m Recommended configuration applied successfully!\033[0m\n"
        printf "\n`date +%Y%m%d-%H:%M:%S` Releem Score and Unapplied recommendations in the Releem Dashboard will be updated in a few minutes.\n"
        rm -f "${RELEEM_CONF_DIR}${RELEEM_DB_CONFIG_FILE_NAME}.bkp"
    elif [ $RESTART_CODE -eq 6 ];
    then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m ${DATABASE_NAME} service failed to restart in 1200 seconds. \033[0m\n"
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m Wait for the ${DATABASE_NAME} service to start and Check the ${DATABASE_NAME} error log.\033[0m\n"

        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m Try to roll back the configuration application using the command: \033[0m\n"
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m bash $RELEEM_WORKDIR/mysqlconfigurer.sh -r\033[0m\n\n"
    elif [ $RESTART_CODE -eq 7 ];
    then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m ${DATABASE_NAME} service failed to restart! Check the ${DATABASE_NAME} error log! \033[0m\n"
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m Try to roll back the configuration application using the command: \033[0m\n"
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m bash $RELEEM_WORKDIR/mysqlconfigurer.sh -r\033[0m\n\n"
    fi
    printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m Sending notification to Releem Platform. \033[0m\n"
    $RELEEM_WORKDIR/releem-agent --event=config_applied > /dev/null

    exit "${RESTART_CODE}"
}

function releem_apply_automatic() {
    if [ "$1" == "initial" ]; then
        DB_CONFIG_FILE_NAME="${RELEEM_DB_INITIAL_CONFIG_FILE_NAME}"
        DB_CONFIG_FILE="${RELEEM_DB_INITIAL_CONFIG_FILE}"
    else
        DB_CONFIG_FILE_NAME="${RELEEM_DB_CONFIG_FILE_NAME}"
        DB_CONFIG_FILE="${RELEEM_DB_CONFIG_FILE}"
    fi
    if [ "$DATABASE_TYPE" == "mysql" ]; then
        if ! check_db_version; then        
                printf "\033[31m\n * MySQL version is lower than 5.6.7. Check the documentation https://github.com/Releem/mysqlconfigurer#how-to-apply-the-recommended-configuration for applying the configuration. \033[0m\n"        
            exit 2
        fi
        if [ -f "$MYSQL_MY_CNF_PATH" ]; then
            if [ `$sudo_cmd grep -cE "!includedir $RELEEM_DB_CONFIG_DIR" $MYSQL_MY_CNF_PATH` -eq 0 ]; then
                printf "\033[31m\n Directive includedir was not found in the MySQL configuration file $MYSQL_MY_CNF_PATH.\n Try to reinstall Releem Agent.\n\033[0m"
                exit 11;
            fi
        fi        
    fi
    if [ -z "$RELEEM_DB_CONFIG_DIR" -o ! -d "$RELEEM_DB_CONFIG_DIR" ]; then
        printf "\033[37m\n * ${DATABASE_NAME} configuration directory was not found.\033[0m"
        printf "\033[37m\n * Try to reinstall Releem Agent, and set the my.cnf location.\033[0m"
        exit 3;
    fi

    printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Applying the recommended ${DATABASE_NAME} configuration.\033[0m\n"
    if [ "$1" == "initial" ]; then
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Getting the initial configuration.\033[0m\n"
        $RELEEM_WORKDIR/releem-agent --initial >/dev/null 2>&1 || true
    else
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Getting the latest up-to-date configuration.\033[0m\n"
        $RELEEM_WORKDIR/releem-agent -c >/dev/null 2>&1 || true
    fi
    if [ ! -f $DB_CONFIG_FILE ]; then
        printf "\033[37m\n * Recommended ${DATABASE_NAME} configuration was not found.\033[0m"
        printf "\033[37m\n * Please apply recommended configuration later or run Releem Agent manually:\033[0m"
        printf "\033[32m\n $RELEEM_WORKDIR/releem-agent -f \033[0m\n\n"
        exit 1;
    fi
    FLAG_RESTART_SERVICE=1
    if [ -z "$RELEEM_RESTART_SERVICE" ]; then
      read -p "Restart ${DATABASE_NAME} service? (Y/N) " -n 1 -r
      echo    # move to a new line
      if [[ ! $REPLY =~ ^[Yy]$ ]]
      then
          printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Confirmation to restart the service has not been received. Releem recommended configuration has not been applied.\033[0m\n"
          FLAG_RESTART_SERVICE=0
      fi
    elif [ "$RELEEM_RESTART_SERVICE" -eq 0 ]; then
        FLAG_RESTART_SERVICE=0
    fi


    printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Copying file $DB_CONFIG_FILE to directory $RELEEM_DB_CONFIG_DIR/.\033[0m\n"
    if [ ! -f "${DB_CONFIG_FILE}.bkp" ]; then
        yes | cp -f "${RELEEM_DB_CONFIG_DIR}/${DB_CONFIG_FILE_NAME}" "${DB_CONFIG_FILE}.bkp"
    fi    
    yes | cp -fr $DB_CONFIG_FILE $RELEEM_DB_CONFIG_DIR/
    chmod 644 $RELEEM_DB_CONFIG_DIR/*

    if [ "$FLAG_RESTART_SERVICE" -ne 0 ]; then
        if [ -z "$RELEEM_COMMAND_RESTART_SERVICE" ]; then
            printf "\033[37m\n * The command to restart the ${DATABASE_NAME} service was not found. Try to reinstall Releem Agent.\033[0m"
            exit 4;
        fi    
        #echo "-------Test config-------"
        printf "\n`date +%Y%m%d-%H:%M:%S`\033[37m Restarting ${DATABASE_NAME} with the command '$RELEEM_COMMAND_RESTART_SERVICE'.\033[0m\n"
        eval "$RELEEM_COMMAND_RESTART_SERVICE" &
        wait_restart $!
        RESTART_CODE=$?

        if [ $RESTART_CODE -eq 0 ];
        then
            printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m The ${DATABASE_NAME} service restarted successfully!\033[0m\n"
            printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m Recommended configuration applied successfully!\033[0m\n"
            printf "\n`date +%Y%m%d-%H:%M:%S` Releem Score and Unapplied recommendations in the Releem Dashboard will be updated in a few minutes.\n"
            rm -f "${DB_CONFIG_FILE}.bkp"
        elif [ $RESTART_CODE -eq 6 ];
        then
            printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m ${DATABASE_NAME} service failed to restart in 1200 seconds. \033[0m\n"
            printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m Wait for the ${DATABASE_NAME} service to start and Check the ${DATABASE_NAME} error log.\033[0m\n"

            printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m Try to roll back the configuration application using the command: \033[0m\n"
            printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m bash $RELEEM_WORKDIR/mysqlconfigurer.sh -r\033[0m\n\n"
        elif [ $RESTART_CODE -eq 7 ];
        then
            printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m ${DATABASE_NAME} service failed to restart. Check the ${DATABASE_NAME} error log. \033[0m\n"
            printf "\n`date +%Y%m%d-%H:%M:%S`\033[31m Try to roll back the configuration application using the command: \033[0m\n"
            printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m bash $RELEEM_WORKDIR/mysqlconfigurer.sh -r\033[0m\n\n"
        fi
    else
        RESTART_CODE=0
    fi
    printf "\n`date +%Y%m%d-%H:%M:%S`\033[32m Sending notification to Releem Platform. \033[0m\n"
    $RELEEM_WORKDIR/releem-agent --event=config_applied > /dev/null

    exit "${RESTART_CODE}"
}

function detect_mysql_commands() {
    local mysqladmin_cmd=""
    local mysql_cmd=""

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
    
    if [ -f "/etc/my.cnf" ]; then
        MYSQL_MY_CNF_PATH="/etc/my.cnf"
    elif [ -f "/etc/mysql/my.cnf" ]; then
        MYSQL_MY_CNF_PATH="/etc/mysql/my.cnf"
    else
        MYSQL_MY_CNF_PATH=""
    fi    
}

function detect_postgresql_commands() {
    local psql_cmd=""
    local pg_isready_cmd=""
   
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
}


function load_runtime_config() {
    mysql_connection_string=""
    pg_connection_string=""
    if test -f "$RELEEM_CONF_FILE" ; then
        . "$RELEEM_CONF_FILE"

        if [ ! -z "$apikey" ]; then
            RELEEM_API_KEY=$apikey
        fi
        if [ ! -z "$memory_limit" ]; then
            DB_MEMORY_LIMIT=$memory_limit
        fi
        if [ ! -z "$query_optimization" ]; then
            RELEEM_QUERY_OPTIMIZATION=$query_optimization
        fi
        if [ ! -z "$releem_region" ]; then
            RELEEM_REGION=$releem_region
        fi
        if [ ! -z "$instance_type" ]; then
            RELEEM_INSTANCE_TYPE=$instance_type
        else
            RELEEM_INSTANCE_TYPE="local"
        fi

        # MySQL Configuration Variables
        if [ ! -z "$mysql_cnf_dir" ]; then
            RELEEM_MYSQL_CONFIG_DIR=$mysql_cnf_dir
        fi
        if [ ! -z "$mysql_restart_service" ]; then
            RELEEM_MYSQL_RESTART_SERVICE=$mysql_restart_service
        fi
        if [ ! -z "$mysql_user" ]; then
            MYSQL_LOGIN=$mysql_user
        fi
        if [ ! -z "$mysql_password" ]; then
            MYSQL_PASSWORD=$mysql_password
        fi
        if [ ! -z "$mysql_host" ]; then
            if [ -S "$mysql_host" ]; then
                mysql_connection_string="${mysql_connection_string} --socket=$mysql_host"
            else
                mysql_connection_string="${mysql_connection_string} --host=$mysql_host"
            fi
        else
            mysql_connection_string="${mysql_connection_string} --host=127.0.0.1"
        fi
        if [ ! -z "$mysql_port" ]; then
            mysql_connection_string="${mysql_connection_string} --port=$mysql_port"
        else
            mysql_connection_string="${mysql_connection_string} --port=3306"
        fi

        # PostgreSQL Configuration Variables
        if [ ! -z "$pg_cnf_dir" ]; then
            RELEEM_PG_CONFIG_DIR=$pg_cnf_dir
        fi
        if [ ! -z "$pg_restart_service" ]; then
            RELEEM_PG_RESTART_SERVICE=$pg_restart_service
        fi
        if [ ! -z "$pg_user" ]; then
            PG_LOGIN=$pg_user
        fi
        if [ ! -z "$pg_password" ]; then
            PG_PASSWORD=$pg_password
        fi
        if [ ! -z "$pg_host" ]; then
            pg_connection_string="${pg_connection_string} -h $pg_host"
        else
            pg_connection_string="${pg_connection_string} -h 127.0.0.1"
        fi
        if [ ! -z "$pg_port" ]; then
            pg_connection_string="${pg_connection_string} -p $pg_port"
        else
            pg_connection_string="${pg_connection_string} -p 5432"
        fi
        if [ ! -z "$pg_database" ]; then
            pg_connection_string="${pg_connection_string} -d $pg_database"
        else
            pg_connection_string="${pg_connection_string} -d postgres"
        fi
    fi

    # Database Type Detection
    DATABASE_TYPE="mysql"  # Default to MySQL for backward compatibility
    if [ ! -z "$PG_LOGIN" ] && [ ! -z "$PG_PASSWORD" ]; then
        DATABASE_TYPE="postgresql"
        DATABASE_NAME="PostgreSQL"
        RELEEM_DB_CONFIG_FILE_NAME="z_aiops_postgresql.conf"
        RELEEM_DB_INITIAL_CONFIG_FILE_NAME="initial_config_postgresql.conf"
        RELEEM_DB_CONFIG_DIR="${RELEEM_PG_CONFIG_DIR}"
        RELEEM_COMMAND_RESTART_SERVICE="${RELEEM_PG_RESTART_SERVICE}"
    elif [ ! -z "$MYSQL_LOGIN" ] && [ ! -z "$MYSQL_PASSWORD" ]; then
        DATABASE_TYPE="mysql"
        DATABASE_NAME="MySQL"
        RELEEM_DB_CONFIG_FILE_NAME="z_aiops_mysql.cnf"
        RELEEM_DB_INITIAL_CONFIG_FILE_NAME="initial_config_mysql.cnf"
        RELEEM_DB_CONFIG_DIR="${RELEEM_MYSQL_CONFIG_DIR}"
        RELEEM_COMMAND_RESTART_SERVICE="${RELEEM_MYSQL_RESTART_SERVICE}"
    fi
    RELEEM_DB_CONFIG_FILE="${RELEEM_CONF_DIR}${RELEEM_DB_CONFIG_FILE_NAME}"
    RELEEM_DB_INITIAL_CONFIG_FILE="${RELEEM_CONF_DIR}${RELEEM_DB_INITIAL_CONFIG_FILE_NAME}"
}

function main() {
    setup_logging
    load_runtime_config

    if [ "$RELEEM_INSTANCE_TYPE" == "local" ]; then
        if [ "$DATABASE_TYPE" == "mysql" ]; then
            detect_mysql_commands
        elif [ "$DATABASE_TYPE" == "postgresql" ]; then
            detect_postgresql_commands
        fi
    fi

    while getopts "k:m:s:arpu" option
    do
      case "${option}" in
        k) RELEEM_API_KEY=${OPTARG};;
        m) DB_MEMORY_LIMIT=${OPTARG};;
        a) releem_apply_manual;;
        s) releem_apply_config ${OPTARG};;
        r) releem_rollback_config;;
        p) releem_configure_database;;
        u) update_agent; exit 0;;
      esac
    done

    printf "\033[37m\n\033[0m"
    printf "\033[37m * To run Releem Agent manually please use the following command:\033[0m\n"
    printf "\033[32m $RELEEM_WORKDIR/releem-agent -f\033[0m\n\n"
}

if [[ -n "${RELEEM_TEST_MODE}" && "${BASH_SOURCE[0]}" != "$0" ]]; then
    return 0
fi

main "$@"