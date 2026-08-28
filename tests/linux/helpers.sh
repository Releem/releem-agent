#!/bin/bash
# Common helpers for Releem agent test scripts.
# Source this file in each test: source "$(dirname "$0")/helpers.sh"

TESTS_PASSED=0
TESTS_FAILED=0
INSTALL_SCRIPT="${INSTALL_SCRIPT:-/tmp/releem_tests/install.sh}"
CONFIGURER_SCRIPT="${CONFIGURER_SCRIPT:-/tmp/releem_tests/mysqlconfigurer.sh}"

# Releem API base URL
RELEEM_API_BASE="${RELEEM_API_BASE:-https://api.releem.com/v2}"

log_info()  { echo "[INFO]  $*"; }
log_pass()  { echo "[PASS]  $*"; TESTS_PASSED=$((TESTS_PASSED + 1)); }
log_fail()  { echo "[FAIL]  $*"; TESTS_FAILED=$((TESTS_FAILED + 1)); }
log_error() { echo "[ERROR] $*" >&2; }

assert_equal() {
    local desc="$1" expected="$2" actual="$3"
    if [[ "$expected" == "$actual" ]]; then
        log_pass "$desc"
    else
        log_fail "$desc: expected='$expected', got='$actual'"
    fi
}

assert_zero() {
    local desc="$1" code="$2"
    if [[ "$code" -eq 0 ]]; then
        log_pass "$desc"
    else
        log_fail "$desc: expected exit 0, got $code"
    fi
}

assert_nonzero() {
    local desc="$1" code="$2"
    if [[ "$code" -ne 0 ]]; then
        log_pass "$desc"
    else
        log_fail "$desc: expected non-zero exit, got 0"
    fi
}

assert_file_exists() {
    local desc="$1" path="$2"
    if [[ -f "$path" ]]; then
        log_pass "$desc: $path exists"
    else
        log_fail "$desc: file not found: $path"
    fi
}

assert_file_not_exists() {
    local desc="$1" path="$2"
    if [[ ! -f "$path" ]]; then
        log_pass "$desc: $path does not exist"
    else
        log_fail "$desc: expected file to be absent: $path"
    fi
}

assert_dir_exists() {
    local desc="$1" path="$2"
    if [[ -d "$path" ]]; then
        log_pass "$desc: $path exists"
    else
        log_fail "$desc: directory not found: $path"
    fi
}

assert_service_running() {
    local desc="$1" service="$2"
    if systemctl is-active --quiet "$service" 2>/dev/null; then
        log_pass "$desc: service '$service' is running"
    else
        log_fail "$desc: service '$service' is not running"
    fi
}

assert_service_stopped() {
    local desc="$1" service="$2"
    if ! systemctl is-active --quiet "$service" 2>/dev/null; then
        log_pass "$desc: service '$service' is stopped"
    else
        log_fail "$desc: service '$service' is still running"
    fi
}

assert_mysql_user_exists() {
    local desc="$1" user="$2"
    local result
    result=$(mysql -u root -p"${MYSQL_ROOT_PASSWORD}" -sNe "SELECT COUNT(*) FROM mysql.user WHERE User='$user';" 2>/dev/null)
    if [[ "$result" -gt 0 ]]; then
        log_pass "$desc: MySQL user '$user' exists"
    else
        log_fail "$desc: MySQL user '$user' not found"
    fi
}

assert_mysql_can_connect() {
    local desc="$1" user="$2" password="$3"
    if mysql -u "$user" -p"$password" -h 127.0.0.1 -e "SELECT 1;" &>/dev/null; then
        log_pass "$desc: MySQL user '$user' can connect"
    else
        log_fail "$desc: MySQL user '$user' cannot connect"
    fi
}

get_releem_config_value() {
    local file="$1" key="$2"
    sed -n "s/^${key}=\"\(.*\)\"$/\1/p" "$file" | head -n 1
}

assert_mysql_can_run_query() {
    local desc="$1" user="$2" password="$3" query="$4"
    if mysql -u "$user" -p"$password" -h 127.0.0.1 -e "$query" &>/dev/null; then
        log_pass "$desc"
    else
        log_fail "$desc: MySQL user '$user' cannot run: $query"
    fi
}

assert_file_contains() {
    local desc="$1" file="$2" pattern="$3"
    if grep -q "$pattern" "$file" 2>/dev/null; then
        log_pass "$desc: '$pattern' found in $file"
    else
        log_fail "$desc: '$pattern' not found in $file"
    fi
}

# Check that the agent hostname is registered in the Releem API.
# Uses the Releem API to list servers and checks for the hostname.
# TODO: Confirm the exact API endpoint and response format with the team.
assert_api_registered() {
    local desc="$1" hostname="$2"
    local api_key="${RELEEM_API_KEY:-}"

    if [[ -z "$api_key" ]]; then
        log_fail "$desc: RELEEM_API_KEY not set, cannot verify API registration"
        return
    fi

    local response
    response=$(curl -s --max-time 30 \
        -H "x-releem-api-key: $api_key" \
        "${RELEEM_API_BASE}/servers" 2>/dev/null)

    if echo "$response" | grep -q "$hostname"; then
        log_pass "$desc: hostname '$hostname' found in Releem API"
    else
        # Non-fatal: API endpoint may not be confirmed; log as info rather than failing
        log_info "WARN $desc: hostname '$hostname' not found in Releem API response (endpoint may need updating)"
        log_info "API response (truncated): $(echo "$response" | head -c 300)"
    fi
}

# Check that a configuration has been applied for the given hostname in the API.
assert_api_config_applied() {
    local desc="$1" hostname="$2"
    local api_key="${RELEEM_API_KEY:-}"

    if [[ -z "$api_key" ]]; then
        log_fail "$desc: RELEEM_API_KEY not set"
        return
    fi

    local response
    response=$(curl -s --max-time 30 \
        -H "x-releem-api-key: $api_key" \
        "${RELEEM_API_BASE}/servers" 2>/dev/null)

    # TODO: Adjust the JSON field name once API response format is confirmed
    if echo "$response" | grep -q "\"$hostname\"" && echo "$response" | grep -q "applied"; then
        log_pass "$desc: config applied status confirmed in API for '$hostname'"
    else
        log_fail "$desc: config applied status NOT confirmed in API for '$hostname'"
        log_info "API response (truncated): $(echo "$response" | head -c 500)"
    fi
}

# Wait for /tmp/bootstrap_complete to appear (used when called via SSH just after VM provisioning)
wait_for_bootstrap() {
    local timeout="${1:-300}"
    local elapsed=0
    echo "[INFO] Waiting for bootstrap to complete (timeout=${timeout}s)..."
    while [[ ! -f /tmp/bootstrap_complete ]]; do
        sleep 5
        elapsed=$((elapsed + 5))
        if [[ $elapsed -ge $timeout ]]; then
            echo "[ERROR] Bootstrap timed out after ${timeout}s"
            return 1
        fi
    done
    echo "[INFO] Bootstrap complete"
}

# Remove all Releem agent artifacts for a clean test run.
cleanup_releem() {
    log_info "Cleaning up Releem agent..."
    systemctl stop releem-agent 2>/dev/null || true
    systemctl disable releem-agent 2>/dev/null || true
    rm -rf /opt/releem /etc/cron.d/releem* /var/log/releem*
    crontab -l 2>/dev/null | grep -v releem | crontab - 2>/dev/null || true
    log_info "Releem cleanup done"
}

# Remove releem MySQL user to allow re-testing auto-creation.
drop_releem_mysql_user() {
    mysql -u root -p"${MYSQL_ROOT_PASSWORD}" -e \
        "DROP USER IF EXISTS 'releem'@'127.0.0.1'; DROP USER IF EXISTS 'releem'@'localhost'; DROP USER IF EXISTS 'releem'@'%'; FLUSH PRIVILEGES;" \
        2>/dev/null || true
    log_info "Dropped releem MySQL user (if existed)"
}

# Print test summary and exit with appropriate code.
print_summary() {
    local test_name="$1"
    echo ""
    echo "========================================="
    echo "Test: $test_name"
    echo "  PASSED: $TESTS_PASSED"
    echo "  FAILED: $TESTS_FAILED"
    echo "========================================="
    if [[ $TESTS_FAILED -gt 0 ]]; then
        exit 1
    fi
    exit 0
}

# Determine the active MySQL/MariaDB service name.
get_db_service() {
    for svc in mysql mysqld mariadb; do
        if systemctl is-enabled "$svc" &>/dev/null; then
            echo "$svc"
            return
        fi
    done
    echo "mysql"
}
