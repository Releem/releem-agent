#!/bin/bash
# Windows test runner without SSH: run tests in startup script and read serial output.

set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TERRAFORM_DIR="$SCRIPT_DIR/gcp/terraform"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

DB_VERSION="mysql-8.0"
TEST_NUM="all"
KEEP_VM=false
OS_VERSION="windows-2022"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --os)      OS_VERSION="$2"; shift 2 ;;
        --db)      DB_VERSION="$2"; shift 2 ;;
        --test)    TEST_NUM="$2";   shift 2 ;;
        --keep-vm) KEEP_VM=true;      shift ;;
        -h|--help)
            echo "Usage: $0 [--os windows-server-2022] [--db mysql-8.0|mysql-8.4|mariadb-10] [--test 1|2|3|4|5|6|7|8|9|10|11|12|all] [--keep-vm]"
            exit 0
            ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

: "${RELEEM_API_KEY:?RELEEM_API_KEY env var must be set}"
: "${GCP_PROJECT:?GCP_PROJECT env var must be set}"

MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD:-ReleemRootPw$(date +%s)!}"
GCP_ZONE="${GCP_ZONE:-us-central1-a}"
GCP_USE_SPOT="${GCP_USE_SPOT:-false}"
OS_IMAGE_FAMILY="windows-2022"
OS_IMAGE_PROJECT="windows-cloud"

OS_SLUG="$(echo "$OS_VERSION" | tr '.' '-')"
DB_SLUG="$(echo "$DB_VERSION" | tr '.' '-')"
VM_NAME="releem-test-${OS_SLUG}-${DB_SLUG}"

echo "[INFO] Checking for stale GCP resources..."
if gcloud compute instances describe "$VM_NAME" --zone="$GCP_ZONE" --project="$GCP_PROJECT" &>/dev/null; then
    echo "[INFO] Deleting stale VM: $VM_NAME"
    gcloud compute instances delete "$VM_NAME" --zone="$GCP_ZONE" --project="$GCP_PROJECT" --quiet
fi

PAYLOAD_DIR="$(mktemp -d /tmp/releem-win-payload-XXXX)"
trap 'rm -rf "$PAYLOAD_DIR"' EXIT

cp "$REPO_ROOT/windows/install.ps1" "$PAYLOAD_DIR/install.ps1"
cp "$REPO_ROOT/windows/mysqlconfigurer.ps1" "$PAYLOAD_DIR/mysqlconfigurer.ps1"
cp "$SCRIPT_DIR/windows/helpers.ps1" "$PAYLOAD_DIR/helpers.ps1"
cp "$SCRIPT_DIR/windows/run_all.ps1" "$PAYLOAD_DIR/run_all.ps1"
cp "$SCRIPT_DIR/windows/test_01_install_auto.ps1" "$PAYLOAD_DIR/test_01_install_auto.ps1"
cp "$SCRIPT_DIR/windows/test_02_install_existing_user.ps1" "$PAYLOAD_DIR/test_02_install_existing_user.ps1"
cp "$SCRIPT_DIR/windows/test_03_apply_config.ps1" "$PAYLOAD_DIR/test_03_apply_config.ps1"
cp "$SCRIPT_DIR/windows/test_04_rollback_config.ps1" "$PAYLOAD_DIR/test_04_rollback_config.ps1"
cp "$SCRIPT_DIR/windows/test_05_update_delegation.ps1" "$PAYLOAD_DIR/test_05_update_delegation.ps1"
cp "$SCRIPT_DIR/windows/test_06_reinstall_existing_install.ps1" "$PAYLOAD_DIR/test_06_reinstall_existing_install.ps1"
cp "$SCRIPT_DIR/windows/test_07_apply_without_restart.ps1" "$PAYLOAD_DIR/test_07_apply_without_restart.ps1"
cp "$SCRIPT_DIR/windows/test_08_queue_apply.ps1" "$PAYLOAD_DIR/test_08_queue_apply.ps1"
cp "$SCRIPT_DIR/windows/test_09_reinstall_rewrites_config_without_prompt.ps1" "$PAYLOAD_DIR/test_09_reinstall_rewrites_config_without_prompt.ps1"
cp "$SCRIPT_DIR/windows/test_10_install_prompt_root_password.ps1" "$PAYLOAD_DIR/test_10_install_prompt_root_password.ps1"
cp "$SCRIPT_DIR/windows/test_11_install_custom_root_login.ps1" "$PAYLOAD_DIR/test_11_install_custom_root_login.ps1"
cp "$SCRIPT_DIR/windows/test_12_install_wrong_root_password.ps1" "$PAYLOAD_DIR/test_12_install_wrong_root_password.ps1"

PAYLOAD_ZIP="/tmp/releem-win-tests-${DB_SLUG}-$$.zip"
rm -f "$PAYLOAD_ZIP"
(
    cd "$PAYLOAD_DIR"
    zip -qr "$PAYLOAD_ZIP" .
)
TEST_PAYLOAD_B64="$(base64 < "$PAYLOAD_ZIP" | tr -d '\n')"
rm -f "$PAYLOAD_ZIP"

TF_WORKSPACE_DIR="$(mktemp -d /tmp/releem-tf-win-${DB_SLUG}-XXXX)"
trap 'rm -rf "$TF_WORKSPACE_DIR" "$PAYLOAD_DIR"' EXIT
cp -r "$TERRAFORM_DIR/." "$TF_WORKSPACE_DIR/"

cat > "$TF_WORKSPACE_DIR/terraform.tfvars" <<EOFVARS
gcp_project      = "$GCP_PROJECT"
zone             = "$GCP_ZONE"
os_image_family  = "$OS_IMAGE_FAMILY"
os_image_project = "$OS_IMAGE_PROJECT"
os_version       = "$OS_SLUG"
os_type          = "windows"
db_version       = "$DB_VERSION"
db_root_password = "$MYSQL_ROOT_PASSWORD"
releem_api_key   = "$RELEEM_API_KEY"
test_selection   = "$TEST_NUM"
test_payload_b64 = "$TEST_PAYLOAD_B64"
machine_type     = "e2-standard-2"
use_spot         = $GCP_USE_SPOT
EOFVARS

export GOOGLE_OAUTH_ACCESS_TOKEN
GOOGLE_OAUTH_ACCESS_TOKEN=$(gcloud auth print-access-token 2>/dev/null) || {
    echo "[ERROR] Failed to get GCP access token. Run: gcloud auth login"
    exit 1
}

echo "[INFO] Provisioning Windows VM: DB=$DB_VERSION test=$TEST_NUM"
pushd "$TF_WORKSPACE_DIR" >/dev/null

TF_CACHE="${TF_CACHE_DIR:-$HOME/.terraform.d/releem-test-cache}"
if [[ -d "$TF_CACHE/.terraform/providers" ]]; then
    cp -f "$TF_CACHE/.terraform.lock.hcl" . 2>/dev/null || true
    terraform init -input=false -no-color -plugin-dir="$TF_CACHE/.terraform/providers" >/dev/null
else
    terraform init -input=false -no-color >/dev/null
    mkdir -p "$TF_CACHE"
    cp -r .terraform "$TF_CACHE/"
    cp -f .terraform.lock.hcl "$TF_CACHE/" 2>/dev/null || true
fi

for _tf_attempt in 1 2 3; do
    if terraform apply -auto-approve -input=false -no-color; then
        break
    fi
    if [[ $_tf_attempt -lt 3 ]]; then
        echo "[INFO] terraform apply failed (attempt $_tf_attempt/3), retrying in 30s..."
        sleep 30
    else
        echo "[ERROR] terraform apply failed after 3 attempts"
        exit 1
    fi
done

VM_IP=$(terraform output -raw vm_external_ip)
HOSTNAME_OUT=$(terraform output -raw hostname)
popd >/dev/null

echo "[INFO] VM provisioned: $VM_IP (hostname: $HOSTNAME_OUT)"

if [[ "$KEEP_VM" == "false" ]]; then
    trap 'echo "[INFO] Destroying Windows VM..."; GOOGLE_OAUTH_ACCESS_TOKEN=$(gcloud auth print-access-token 2>/dev/null); export GOOGLE_OAUTH_ACCESS_TOKEN; cd "$TF_WORKSPACE_DIR" && terraform destroy -auto-approve -input=false -no-color; rm -rf "$TF_WORKSPACE_DIR" "$PAYLOAD_DIR"' EXIT
fi

echo "[INFO] Waiting for serial result markers (up to 60 min)..."
SERIAL_TIMEOUT=3600
SERIAL_ELAPSED=0
TEST_EXIT=1
EXPECTED_SUITE_PASSES=12
if [[ "$TEST_NUM" != "all" ]]; then
    EXPECTED_SUITE_PASSES=1
fi

while [[ $SERIAL_ELAPSED -lt $SERIAL_TIMEOUT ]]; do
    SERIAL_OUT=$(gcloud compute instances get-serial-port-output "$VM_NAME" --zone="$GCP_ZONE" --project="$GCP_PROJECT" --port=1 2>/dev/null || true)

    # Primary markers emitted by bootstrap script.
    if echo "$SERIAL_OUT" | grep -q "RELEEM_TEST_RESULT:PASS"; then
        echo "[SUCCESS] Windows tests passed for DB=$DB_VERSION"
        TEST_EXIT=0
        break
    fi
    if echo "$SERIAL_OUT" | grep -q "RELEEM_TEST_RESULT:FAIL"; then
        echo "[FAILURE] Windows tests failed for DB=$DB_VERSION"
        TEST_EXIT=1
        break
    fi
    if echo "$SERIAL_OUT" | grep -q 'Script "windows-startup-script-ps1" failed'; then
        echo "[FAILURE] Windows startup script failed before tests"
        TEST_EXIT=1
        break
    fi

    # Fallback detection from run_all.ps1 output when marker lines are missing.
    if echo "$SERIAL_OUT" | grep -q "OVERALL TEST SUITE RESULTS"; then
        if echo "$SERIAL_OUT" | grep -qE "FAILED:[[:space:]]*0"; then
            echo "[SUCCESS] Windows tests passed for DB=$DB_VERSION (summary fallback)"
            TEST_EXIT=0
            break
        fi
        if echo "$SERIAL_OUT" | grep -qE "FAILED:[[:space:]]*[1-9][0-9]*"; then
            echo "[FAILURE] Windows tests failed for DB=$DB_VERSION (summary fallback)"
            TEST_EXIT=1
            break
        fi
    fi
    if echo "$SERIAL_OUT" | grep -qE "\\[SUITE\\].*: FAILED"; then
        echo "[FAILURE] Windows tests failed for DB=$DB_VERSION (suite fallback)"
        TEST_EXIT=1
        break
    fi

    # Single-test scripts print their own summary before run_all.ps1 emits suite
    # markers. On Windows serial output those final suite markers can be missing.
    if [[ "$TEST_NUM" != "all" ]] && echo "$SERIAL_OUT" | grep -qE "Test:[[:space:]]+Test[[:space:]]+$TEST_NUM:"; then
        if echo "$SERIAL_OUT" | grep -qE "\\[FAIL[[:space:]]*\\]|FAILED:[[:space:]]*[1-9][0-9]*"; then
            echo "[FAILURE] Windows tests failed for DB=$DB_VERSION (single-test summary fallback)"
            TEST_EXIT=1
            break
        fi
        if echo "$SERIAL_OUT" | grep -qE "PASSED:[[:space:]]*[1-9][0-9]*"; then
            echo "[SUCCESS] Windows tests passed for DB=$DB_VERSION (single-test summary fallback)"
            TEST_EXIT=0
            break
        fi
    fi

    # Fast-path success: all expected suite tests reported PASSED and no suite-level failures.
    SUITE_PASSED_COUNT=$(printf "%s" "$SERIAL_OUT" | grep -cE "\\[SUITE\\].*: PASSED" || true)
    if [[ $SUITE_PASSED_COUNT -ge $EXPECTED_SUITE_PASSES ]] && ! echo "$SERIAL_OUT" | grep -qE "\\[SUITE\\].*: FAILED"; then
        echo "[SUCCESS] Windows tests passed for DB=$DB_VERSION (suite pass count)"
        TEST_EXIT=0
        break
    fi

    sleep 20
    SERIAL_ELAPSED=$((SERIAL_ELAPSED + 20))
    echo "[INFO] still waiting... ${SERIAL_ELAPSED}s"
done

if [[ $SERIAL_ELAPSED -ge $SERIAL_TIMEOUT ]]; then
    echo "[ERROR] Timed out waiting for Windows test result marker"
    TEST_EXIT=1
fi

LOG_DIR="$SCRIPT_DIR/../test-results"
mkdir -p "$LOG_DIR"
SERIAL_LOG="$LOG_DIR/serial-windows-${DB_VERSION}-$(date +%Y%m%d-%H%M%S).log"
gcloud compute instances get-serial-port-output "$VM_NAME" --zone="$GCP_ZONE" --project="$GCP_PROJECT" --port=1 > "$SERIAL_LOG" 2>/dev/null || true
echo "[INFO] Serial log saved to $SERIAL_LOG"

exit $TEST_EXIT
