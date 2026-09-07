#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly PROJECT="static-mediator-400907"
readonly LABEL_KEY="releem-topology-run"
readonly MANAGED_LABEL="releem-topology-managed=true"
readonly RUN_LABEL="${GCP_TOPOLOGY_RUN_LABEL:-}"
readonly NETWORK_NAME="releem-ic-net-${RUN_LABEL}"
readonly SUBNET_NAME="releem-ic-subnet-${RUN_LABEL}"
readonly ROUTER_NAME="releem-ic-router-${RUN_LABEL}"
readonly NAT_NAME="releem-ic-nat-${RUN_LABEL}"
readonly SUBNET_CIDR="10.212.0.0/24"
readonly INTERNAL_TAG="releem-ic-internal-${RUN_LABEL}"
readonly IAP_SSH_TAG="releem-ic-iap-ssh-${RUN_LABEL}"
readonly INTERNAL_FIREWALL_NAME="releem-ic-internal-${RUN_LABEL}"
readonly IAP_FIREWALL_NAME="releem-ic-iap-ssh-${RUN_LABEL}"
readonly REQUIRED_NODES=12
readonly DISK_GB_PER_NODE=20
readonly EVIDENCE_DIR="${GCP_TOPOLOGY_EVIDENCE_DIR:-/tmp/releem-db-topology-evidence/gcp}"
readonly STATE_DIR="${GCP_TOPOLOGY_STATE_DIR:-/tmp/releem-db-topology-state/gcp}"
readonly MYSQL_ADMIN_USER="releem_cluster_admin"
readonly MYSQL_REPO_KEY_URL="https://repo.mysql.com/RPM-GPG-KEY-mysql-2025"
readonly SSH_TIMEOUT_SECONDS="${TOPOLOGY_SSH_TIMEOUT_SECONDS:-45}"
readonly SSH_READY_TIMEOUT_SECONDS="${TOPOLOGY_SSH_READY_TIMEOUT_SECONDS:-300}"
readonly BOOTSTRAP_TIMEOUT_SECONDS="${TOPOLOGY_BOOTSTRAP_TIMEOUT_SECONDS:-1800}"
readonly ADMINAPI_TIMEOUT_SECONDS="${TOPOLOGY_ADMINAPI_TIMEOUT_SECONDS:-180}"
readonly SWITCHOVER_TIMEOUT_SECONDS="${TOPOLOGY_SWITCHOVER_TIMEOUT_SECONDS:-180}"
readonly MYSQL_PERSISTENCE_TIMEOUT_SECONDS="${TOPOLOGY_MYSQL_PERSISTENCE_TIMEOUT_SECONDS:-300}"
readonly CLICKHOUSE_PERSISTENCE_TIMEOUT_SECONDS="${TOPOLOGY_CLICKHOUSE_PERSISTENCE_TIMEOUT_SECONDS:-300}"
readonly MYSQL_CONNECT_TIMEOUT_SECONDS="${TOPOLOGY_MYSQL_CONNECT_TIMEOUT_SECONDS:-10}"
readonly MYSQL_QUERY_TIMEOUT_SECONDS="${TOPOLOGY_MYSQL_QUERY_TIMEOUT_SECONDS:-30}"
readonly CLICKHOUSE_CONNECT_TIMEOUT_SECONDS="${TOPOLOGY_CLICKHOUSE_CONNECT_TIMEOUT_SECONDS:-10}"
readonly CLICKHOUSE_QUERY_TIMEOUT_SECONDS="${TOPOLOGY_CLICKHOUSE_QUERY_TIMEOUT_SECONDS:-30}"
readonly CLICKHOUSE_MARKER_WINDOW_SECONDS="${TOPOLOGY_CLICKHOUSE_MARKER_WINDOW_SECONDS:-900}"

readonly -a MACHINE_TYPES=(e2-small e2-medium)

readonly -a NODES=(
  releem-ic-single-1 releem-ic-single-2 releem-ic-single-3
  releem-ic-multi-1 releem-ic-multi-2 releem-ic-multi-3
  releem-ic-cs-primary-1 releem-ic-cs-primary-2 releem-ic-cs-primary-3
  releem-ic-cs-replica-1 releem-ic-cs-replica-2 releem-ic-cs-replica-3
)

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
log() { printf '[%s] %s\n' "$(date -u +%FT%TZ)" "$*" >&2; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

usage() {
  cat <<'EOF'
Usage: tests/topology/gcp_innodb_cluster.sh COMMAND [OPTIONS]

Commands:
  preflight             Read-only project, quota, zone, collision, and inventory checks
  create                Run preflight, then create or verify the 12 labeled VMs
  configure             Install/configure MySQL 8.4, clusters, and the dev Agent
  exercise              Execute and record all required topology transitions
  collect [MARKER]      Trigger collection and validate Platform persistence
  inventory [--names-only]
                         Print the expected names or sanitized live inventory
  destroy               Delete only resources owned by the run label
  help                   Show this help

Required runtime variables:
  GCP_TOPOLOGY_RUN_LABEL       Lowercase run ownership label (preflight/lifecycle)
  RELEEM_API_KEY              Required by configure, exercise, and collect
  MYSQL_CLUSTER_PASSWORD      Ephemeral AdminAPI password; mode-0600 /tmp storage is allowed

Persistence validation variables for collect/exercise:
  TOPOLOGY_MYSQL_HOST, TOPOLOGY_MYSQL_USER, TOPOLOGY_MYSQL_PASSWORD
  TOPOLOGY_MYSQL_DATABASE (default: releemdb), TOPOLOGY_MYSQL_PORT (default: 3306)
  TOPOLOGY_CLICKHOUSE_HOST, TOPOLOGY_CLICKHOUSE_USER, TOPOLOGY_CLICKHOUSE_PASSWORD
  TOPOLOGY_CLICKHOUSE_DATABASE (default: releemdb_dev), PORT (default: 8123), SCHEME (default: http)
  TOPOLOGY_UID                Exact positive numeric dev tenant UID

Set GCP_TOPOLOGY_CONFIRM_DESTROY to the exact run label before destroy.
EOF
}

validate_run_label() {
  [[ "$RUN_LABEL" =~ ^[a-z][a-z0-9-]{0,39}$ ]] ||
    die "GCP_TOPOLOGY_RUN_LABEL must match ^[a-z][a-z0-9-]{0,39}$"
}

require_api_key() {
  [[ "${RELEEM_API_KEY:-}" =~ ^[A-Za-z0-9._-]{1,255}$ ]] ||
    die "RELEEM_API_KEY must be set to a valid runtime token"
}

require_cluster_password() {
  [[ "${MYSQL_CLUSTER_PASSWORD:-}" =~ ^[A-Za-z0-9._-]{16,128}$ ]] ||
    die "MYSQL_CLUSTER_PASSWORD must be 16-128 characters from A-Za-z0-9._-"
}

gcloud_project() { gcloud --project="$PROJECT" "$@"; }

state_file() { printf '%s/%s.env\n' "$STATE_DIR" "$RUN_LABEL"; }

load_state() {
  validate_run_label
  local file
  file="$(state_file)"
  [[ -f "$file" ]] || die "run state is absent; run preflight first"
  # State contains only validated non-secret identifiers written by this script.
  # shellcheck disable=SC1090
  source "$file"
  [[ "${STATE_PROJECT:-}" == "$PROJECT" && "${STATE_RUN_LABEL:-}" == "$RUN_LABEL" ]] ||
    die "state ownership mismatch"
  [[ "${STATE_ZONE:-}" =~ ^[a-z0-9-]+$ && "${STATE_MACHINE_TYPE:-}" =~ ^[a-z0-9-]+$ ]] ||
    die "invalid state file"
}

write_state() {
  local zone="$1" region="$2" machine_type="$3" file
  mkdir -p "$STATE_DIR" "$EVIDENCE_DIR/$RUN_LABEL"
  file="$(state_file)"
  printf 'STATE_PROJECT=%q\nSTATE_RUN_LABEL=%q\nSTATE_ZONE=%q\nSTATE_REGION=%q\nSTATE_MACHINE_TYPE=%q\n' \
    "$PROJECT" "$RUN_LABEL" "$zone" "$region" "$machine_type" >"$file"
  chmod 600 "$file"
}

assert_owned_or_absent() {
  local kind="$1" records="$2" name owner managed rest
  while IFS='|' read -r name _ _ owner managed rest; do
    [[ -n "$name" ]] || continue
    [[ "$owner" == "$RUN_LABEL" && "$managed" == "true" ]] ||
      die "$kind $name has another run ownership or is missing managed ownership"
  done <<<"$records"
}

quota_values() {
  local quotas="$1" metric="$2" row limit usage
  if jq -e . >/dev/null 2>&1 <<<"$quotas"; then
    row="$(jq -r --arg metric "$metric" '.quotas[]? | select(.metric == $metric) | [.limit, .usage] | @tsv' <<<"$quotas" | head -n1)"
    read -r limit usage <<<"$row"
  else
    row="$(printf '%s\n' "$quotas" | tr ';' '\n' | awk -v m="$metric" '$1 == m {print $2, $3; exit}')"
    read -r limit usage <<<"$row"
  fi
  [[ -n "${limit:-}" && -n "${usage:-}" ]] || return 1
  printf '%s %s\n' "$limit" "$usage"
}

quota_available() {
  local quotas="$1" metric="$2" required="$3" limit usage
  read -r limit usage < <(quota_values "$quotas" "$metric") || return 1
  awk -v l="$limit" -v u="$usage" -v r="$required" 'BEGIN { exit !((l-u) >= r) }'
}

quota_json() {
  local quotas="$1" metric="$2" limit usage
  read -r limit usage < <(quota_values "$quotas" "$metric") || return 1
  jq -n --argjson limit "$limit" --argjson usage "$usage" \
    '{limit:$limit,usage:$usage,available:($limit-$usage)}'
}

resource_records_json() {
  jq -Rsc 'split("\n") | map(select(length > 0) | split("|") |
    {name:.[0],zone:.[1],status:.[2],run_label:.[3],managed:(.[4] == "true"),detail:.[5]})'
}

is_expected_node() {
  local candidate="$1" node
  for node in "${NODES[@]}"; do [[ "$candidate" == "$node" ]] && return 0; done
  return 1
}

expected_resource_records() {
  local record name
  while IFS= read -r record; do
    [[ -n "$record" ]] || continue
    name="${record%%|*}"
    if is_expected_node "$name"; then
      printf '%s\n' "$record"
    fi
  done
  return 0
}

validate_disk_inventory() {
  local instances="$1" disks="$2" name zone owner managed users instance_zone
  declare -A instance_zones=()
  while IFS='|' read -r name zone _ _ _ _; do
    [[ -n "$name" ]] && instance_zones["$name"]="$zone"
  done <<<"$instances"
  while IFS='|' read -r name zone _ owner managed users; do
    [[ -n "$name" ]] || continue
    is_expected_node "$name" || die "disk ownership mismatch: unexpected disk $name"
    [[ "$owner" == "$RUN_LABEL" && "$managed" == "true" ]] ||
      die "disk $name has another run ownership"
    instance_zone="${instance_zones[$name]:-}"
    [[ -n "$instance_zone" ]] || die "disk ownership mismatch: orphan disk $name must be removed before create"
    [[ "$zone" == "$instance_zone" ]] || die "disk ownership mismatch: $name is in $zone, instance is in $instance_zone"
    [[ "$users" == */instances/"$name" ]] ||
      die "disk ownership mismatch: $name is unattached or attached to another instance"
  done <<<"$disks"
}

validate_private_instances() {
  local instances="$1" name nat_ip
  while IFS='|' read -r name _ _ _ _ _ nat_ip _ _; do
    [[ -n "$name" ]] || continue
    [[ -z "$nat_ip" ]] || die "owned instance $name has a public address"
  done <<<"$instances"
}

firewall_owned() {
  local records="$1" name="$2"
  awk -F'|' -v n="$name" -v marker="owner=${LABEL_KEY}:${RUN_LABEL}" \
    '$1 == n && index($2, marker) {found=1} END {exit !found}' <<<"$records"
}

firewall_records_json() {
  jq -Rsc 'split("\n") | map(select(length > 0) | split("|") |
    {name:.[0],ownership_marker:.[1]})'
}

ownership_description() {
  printf 'owner=%s:%s; managed-by=tests/topology/gcp_innodb_cluster.sh\n' "$LABEL_KEY" "$RUN_LABEL"
}

normalize_json_array() {
  local value="$1"
  [[ -n "$value" ]] || value='[]'
  jq -e 'type == "array"' <<<"$value" >/dev/null || die "gcloud returned invalid JSON inventory"
  printf '%s\n' "$value"
}

validate_network_stack() {
  local region="$1" network_file="$2" subnet_file="$3" router_file="$4" nat_file="$5" firewall_file="$6" mode="$7"
  local network_required=false nat_required=false nat_forbidden=false firewall_required=false
  case "$mode" in
    optional) ;;
    pre-nat) network_required=true; nat_forbidden=true; firewall_required=true ;;
    required) network_required=true; nat_required=true; firewall_required=true ;;
    *) die "invalid network validation mode: $mode" ;;
  esac
  jq -e --arg name "$NETWORK_NAME" --arg owner "$(ownership_description)" --argjson required "$network_required" '
    (length == 1 or ($required == false and length == 0)) and
    all(.name == $name and .description == ($owner|rtrimstr("\n")) and .autoCreateSubnetworks == false and .routingConfig.routingMode == "REGIONAL")
  ' "$network_file" >/dev/null || die "network ownership or semantics mismatch"
  jq -e --arg name "$SUBNET_NAME" --arg network "$NETWORK_NAME" --arg region "$region" --arg cidr "$SUBNET_CIDR" --arg owner "$(ownership_description)" --argjson required "$network_required" '
    (length == 1 or ($required == false and length == 0)) and
    all(.name == $name and .description == ($owner|rtrimstr("\n")) and (.network|endswith("/"+$network)) and
        (.region|endswith("/"+$region)) and .ipCidrRange == $cidr and .privateIpGoogleAccess == true and
        ((.secondaryIpRanges // []) == []))
  ' "$subnet_file" >/dev/null || die "subnet ownership or semantics mismatch"
  jq -e --arg name "$ROUTER_NAME" --arg network "$NETWORK_NAME" --arg region "$region" --arg owner "$(ownership_description)" --argjson required "$network_required" '
    (length == 1 or ($required == false and length == 0)) and
    all(.name == $name and .description == ($owner|rtrimstr("\n")) and (.network|endswith("/"+$network)) and (.region|endswith("/"+$region)))
  ' "$router_file" >/dev/null || die "router ownership or semantics mismatch"
  jq -e --arg name "$NAT_NAME" --arg subnet "$SUBNET_NAME" --argjson required "$nat_required" --argjson forbidden "$nat_forbidden" '
    (if $forbidden then length == 0 else (length == 1 or ($required == false and length == 0)) end) and
    all(.name == $name and .natIpAllocateOption == "AUTO_ONLY" and
        .sourceSubnetworkIpRangesToNat == "LIST_OF_SUBNETWORKS" and
        (.subnetworks|length) == 1 and (.subnetworks[0].name|endswith("/"+$subnet)) and
        ((.subnetworks[0].sourceIpRangesToNat == ["PRIMARY_IP_RANGE"]) or
         (.subnetworks[0].sourceIpRangesToNat == ["ALL_IP_RANGES"])))
  ' "$nat_file" >/dev/null || die "Cloud NAT ownership or semantics mismatch"
  jq -e --arg network "$NETWORK_NAME" --arg internal "releem-ic-internal-${RUN_LABEL}" --arg iap "releem-ic-iap-ssh-${RUN_LABEL}" \
    --arg internal_tag "$INTERNAL_TAG" --arg iap_tag "$IAP_SSH_TAG" --arg cidr "$SUBNET_CIDR" --arg owner "$(ownership_description)" --argjson required "$firewall_required" '
    def has_exact_tcp_ports($expected):
      (.allowed | type) == "array" and
      (.allowed | length) > 0 and
      all(.allowed[];
        type == "object" and
        (keys | sort) == ["IPProtocol", "ports"] and
        .IPProtocol == "tcp" and
        (.ports | type) == "array" and
        (.ports | length) > 0 and
        all(.ports[]; type == "string" and test("^[0-9]+$"))) and
      ([.allowed[].ports[]] as $ports |
        ($ports | length) == ($ports | unique | length) and
        ($ports | sort) == ($expected | sort));
    def has_no_extra_selectors:
      ((.sourceTags // []) == []) and
      ((.sourceServiceAccounts // []) == []) and
      ((.targetServiceAccounts // []) == []) and
      ((.destinationRanges // []) == []) and
      ((.sourceSecureTags // []) == []) and
      ((.targetSecureTags // []) == []) and
      ((.params.resourceManagerTags // {}) == {}) and
      ((.denied // []) == []);
    ((($required == true) and length == 2) or (($required == false) and length <= 2)) and
    all(.description == ($owner|rtrimstr("\n")) and (.network|endswith("/"+$network)) and .direction == "INGRESS" and
        (.priority // 1000) == 1000 and (.disabled // false) == false and has_no_extra_selectors) and
    (if length == 0 then true else
      (map(select(.name == $internal and .sourceRanges == [$cidr] and .targetTags == [$internal_tag] and
        has_exact_tcp_ports(["3306", "33060", "33061"])))|length) == (if $required then 1 else (map(select(.name==$internal))|length) end) and
      (map(select(.name == $iap and .sourceRanges == ["35.235.240.0/20"] and .targetTags == [$iap_tag] and
        has_exact_tcp_ports(["22"])))|length) == (if $required then 1 else (map(select(.name==$iap))|length) end) and
      (map(.name)|unique|length) == length and all(.name == $internal or .name == $iap)
     end)
  ' "$firewall_file" >/dev/null || die "firewall ownership or semantics mismatch"
}

inventory_network_stack() {
  local region="$1" dir="$2" value
  mkdir -p "$dir"
  value="$(gcloud_project compute networks list --filter="name=${NETWORK_NAME}" --format=json)"; normalize_json_array "$value" >"$dir/network.json"
  value="$(gcloud_project compute networks subnets list --filter="name=${SUBNET_NAME}" --format=json)"; normalize_json_array "$value" >"$dir/subnet.json"
  value="$(gcloud_project compute routers list --filter="name=${ROUTER_NAME}" --format=json)"; normalize_json_array "$value" >"$dir/router.json"
  value="$(gcloud_project compute firewall-rules list --filter="network=${NETWORK_NAME}" --format=json)"; normalize_json_array "$value" >"$dir/firewall.json"
  if jq -e 'length == 1' "$dir/router.json" >/dev/null; then
    [[ -n "$region" ]] || region="$(jq -r '.[0].region|split("/")[-1]' "$dir/router.json")"
    value="$(gcloud_project compute routers nats list --router="$ROUTER_NAME" --region="$region" --format=json)"
  else
    value='[]'
  fi
  normalize_json_array "$value" >"$dir/nat.json"
}

validate_live_network_stack() {
  local region="$1" mode="${2:-required}" dir="$EVIDENCE_DIR/$RUN_LABEL/network-stack"
  inventory_network_stack "$region" "$dir"
  validate_network_stack "$region" "$dir/network.json" "$dir/subnet.json" "$dir/router.json" "$dir/nat.json" "$dir/firewall.json" "$mode"
}

validate_instance_inventory() {
  local records="$1" zone="$2" machine_type="$3" name instance_zone owner managed found_type nat network subnet
  local types
  types="$(awk -F'|' 'NF && $6 != "" {print $6}' <<<"$records" | sort -u)"
  [[ "$(sed '/^$/d' <<<"$types" | wc -l)" -le 1 ]] || die "owned instances use mixed machine types"
  while IFS='|' read -r name instance_zone _ owner managed found_type nat network subnet; do
    [[ -n "$name" ]] || continue
    is_expected_node "$name" || die "unexpected matching instance: $name"
    [[ "$owner" == "$RUN_LABEL" && "$managed" == true ]] || die "instance ownership mismatch"
    [[ "$instance_zone" == "$zone" && "$found_type" == "$machine_type" ]] || die "instance zone or machine type mismatch"
    [[ "$found_type" == "${MACHINE_TYPES[0]}" || "$found_type" == "${MACHINE_TYPES[1]}" ]] || die "unsupported machine type"
    [[ -z "$nat" ]] || die "owned instance $name has a public address"
    [[ "$network" == "$NETWORK_NAME" && "$subnet" == "$SUBNET_NAME" ]] || die "instance network attachment mismatch"
  done <<<"$records"
}

preflight() {
  validate_run_label
  require_command gcloud
  require_command jq
  local lifecycle zones zone region quotas machine_type machine_info existing_count missing_count candidate_region candidate_zone candidate_machine
  local instances disks evidence existing_zones cpu_required disk_required selected=0 stack_dir stack_region

  lifecycle="$(gcloud_project projects describe "$PROJECT" --format='value(lifecycleState)')"
  [[ "$lifecycle" == "ACTIVE" ]] || die "project $PROJECT is not ACTIVE"

  instances="$(gcloud_project compute instances list \
    --format="csv[no-heading,separator='|'](name,zone.basename(),status,labels.${LABEL_KEY},labels.releem-topology-managed,machineType.basename(),networkInterfaces[0].accessConfigs[0].natIP,networkInterfaces[0].network.basename(),networkInterfaces[0].subnetwork.basename())" |
    expected_resource_records)"
  disks="$(gcloud_project compute disks list \
    --format="csv[no-heading,separator='|'](name,zone.basename(),status,labels.${LABEL_KEY},labels.releem-topology-managed,users)" |
    expected_resource_records)"
  stack_dir="$EVIDENCE_DIR/$RUN_LABEL/preflight-network"
  inventory_network_stack '' "$stack_dir"
  stack_region="$(jq -r 'if length == 1 then .[0].region|split("/")[-1] else "" end' "$stack_dir/subnet.json")"

  assert_owned_or_absent instance "$instances"
  validate_private_instances "$instances"
  validate_disk_inventory "$instances" "$disks"

  zones="$(gcloud_project compute zones list --filter='status=UP' --format='value(name,status)')"
  existing_zones="$(cut -d'|' -f2 <<<"$instances" | sed '/^$/d' | sort -u)"
  if [[ "$(printf '%s\n' "$existing_zones" | sed '/^$/d' | wc -l)" -gt 1 ]]; then
    die "owned instances span multiple zones"
  fi
  existing_count="$(printf '%s\n' "$instances" | sed '/^$/d' | wc -l)"
  (( existing_count <= REQUIRED_NODES )) || die "matching instance inventory exceeds $REQUIRED_NODES"
  missing_count=$((REQUIRED_NODES - existing_count))
  cpu_required=$((missing_count * 2))
  disk_required=$((missing_count * DISK_GB_PER_NODE))

  if [[ -n "$existing_zones" ]]; then
    zone="$existing_zones"
    grep -Eq "^${zone}[[:space:]]+UP$" <<<"$zones" || die "selected zone $zone is not UP"
    region="${zone%-*}"
    quotas="$(gcloud_project compute regions describe "$region" --format=json)"
    quota_available "$quotas" CPUS "$cpu_required" ||
      die "region $region lacks conservative CPU quota for $missing_count missing nodes"
    quota_available "$quotas" DISKS_TOTAL_GB "$disk_required" ||
      die "region $region lacks $disk_required GB standard disk quota"
    machine_type="$(awk -F'|' 'NF >= 6 && $6 != "" {print $6; exit}' <<<"$instances")"
    [[ -n "$machine_type" ]] || machine_type="${MACHINE_TYPES[0]}"
    machine_info="$(gcloud_project compute machine-types describe "$machine_type" --zone="$zone" --format='value(guestCpus,memoryMb)')" ||
      die "existing machine type $machine_type is unavailable in $zone"
  else
    zone=''
    while read -r candidate_zone _; do
      [[ -n "$candidate_zone" ]] || continue
      candidate_region="${candidate_zone%-*}"
      [[ -z "$stack_region" || "$candidate_region" == "$stack_region" ]] || continue
      if ! quotas="$(gcloud_project compute regions describe "$candidate_region" --format=json 2>/dev/null)"; then continue; fi
      quota_available "$quotas" CPUS "$cpu_required" || continue
      quota_available "$quotas" DISKS_TOTAL_GB "$disk_required" || continue
      for candidate_machine in "${MACHINE_TYPES[@]}"; do
        if machine_info="$(gcloud_project compute machine-types describe "$candidate_machine" --zone="$candidate_zone" --format='value(guestCpus,memoryMb)' 2>/dev/null)" && [[ -n "$machine_info" ]]; then
          zone="$candidate_zone"
          region="$candidate_region"
          machine_type="$candidate_machine"
          selected=1
          break 2
        fi
      done
    done <<<"$zones"
    (( selected )) || die "no UP zone has a quota-supported machine type and $disk_required GB disk quota"
  fi

  validate_network_stack "$region" "$stack_dir/network.json" "$stack_dir/subnet.json" "$stack_dir/router.json" "$stack_dir/nat.json" "$stack_dir/firewall.json" optional
  validate_instance_inventory "$instances" "$zone" "$machine_type"

  write_state "$zone" "$region" "$machine_type"
  evidence="$EVIDENCE_DIR/$RUN_LABEL/preflight.json"
  jq -n \
    --arg project "$PROJECT" --arg run_label "$RUN_LABEL" --arg zone "$zone" \
    --arg region "$region" --arg machine_type "$machine_type" \
    --argjson expected_nodes "$REQUIRED_NODES" \
    --argjson existing_instances "$(printf '%s\n' "$instances" | sed '/^$/d' | wc -l)" \
    --argjson existing_disks "$(printf '%s\n' "$disks" | sed '/^$/d' | wc -l)" \
    --argjson existing_firewalls "$(jq length "$stack_dir/firewall.json")" \
    --argjson cpu_quota "$(quota_json "$quotas" CPUS)" \
    --argjson disk_quota "$(quota_json "$quotas" DISKS_TOTAL_GB)" \
    --argjson instance_inventory "$(resource_records_json <<<"$instances")" \
    --argjson disk_inventory "$(resource_records_json <<<"$disks")" \
    --argjson network_inventory "$(jq -s '{networks:.[0],subnets:.[1],routers:.[2],nats:.[3],firewalls:.[4]}' "$stack_dir/network.json" "$stack_dir/subnet.json" "$stack_dir/router.json" "$stack_dir/nat.json" "$stack_dir/firewall.json")" \
    '{project:$project,run_label:$run_label,zone:$zone,region:$region,machine_type:$machine_type,expected_nodes:$expected_nodes,selected_quota:{CPUS:$cpu_quota,DISKS_TOTAL_GB:$disk_quota},existing:{instances:$existing_instances,disks:$existing_disks,firewalls:$existing_firewalls},inventory:{instances:$instance_inventory,disks:$disk_inventory,network_stack:$network_inventory},result:"PASS"}' \
    >"$evidence"
  chmod 600 "$evidence"
  log "preflight passed: project=$PROJECT zone=$zone machine=$machine_type nodes=$REQUIRED_NODES"
}

resource_exists() {
  local kind="$1" name="$2" scope="${3:-}"
  case "$kind" in
    instances) gcloud_project compute instances describe "$name" --zone="$scope" >/dev/null 2>&1 ;;
    networks) gcloud_project compute networks describe "$name" >/dev/null 2>&1 ;;
    subnetworks) gcloud_project compute networks subnets describe "$name" --region="$scope" >/dev/null 2>&1 ;;
    routers) gcloud_project compute routers describe "$name" --region="$scope" >/dev/null 2>&1 ;;
    nats) gcloud_project compute routers nats describe "$name" --router="$ROUTER_NAME" --region="$scope" >/dev/null 2>&1 ;;
    firewall-rules) gcloud_project compute firewall-rules describe "$name" >/dev/null 2>&1 ;;
    *) die "unsupported resource kind: $kind" ;;
  esac
}

rollback_created_network_stack() {
  local region="$1" created_network="$2" created_subnet="$3" created_router="$4"
  local created_internal="$5" created_iap="$6" created_nat="$7"
  set +e
  [[ "$created_nat" == true ]] && gcloud_project compute routers nats delete "$NAT_NAME" --router="$ROUTER_NAME" --region="$region" --quiet
  [[ "$created_iap" == true ]] && gcloud_project compute firewall-rules delete "$IAP_FIREWALL_NAME" --quiet
  [[ "$created_internal" == true ]] && gcloud_project compute firewall-rules delete "$INTERNAL_FIREWALL_NAME" --quiet
  [[ "$created_router" == true ]] && gcloud_project compute routers delete "$ROUTER_NAME" --region="$region" --quiet
  [[ "$created_subnet" == true ]] && gcloud_project compute networks subnets delete "$SUBNET_NAME" --region="$region" --quiet
  [[ "$created_network" == true ]] && gcloud_project compute networks delete "$NETWORK_NAME" --quiet
  set -e
}

create() {
  validate_run_label
  require_api_key
  preflight
  load_state
  local node created_network=false created_subnet=false created_router=false
  local created_internal=false created_iap=false created_nat=false nat_preexisting=false
  if ! resource_exists networks "$NETWORK_NAME"; then
    if gcloud_project compute networks create "$NETWORK_NAME" --subnet-mode=custom --bgp-routing-mode=regional \
      --description="$(ownership_description)"; then
      created_network=true
    else
      die "failed to create dedicated network"
    fi
  fi
  if ! resource_exists subnetworks "$SUBNET_NAME" "$STATE_REGION"; then
    if gcloud_project compute networks subnets create "$SUBNET_NAME" --network="$NETWORK_NAME" --region="$STATE_REGION" \
      --range="$SUBNET_CIDR" --enable-private-ip-google-access --description="$(ownership_description)"; then
      created_subnet=true
    else
      rollback_created_network_stack "$STATE_REGION" "$created_network" "$created_subnet" "$created_router" "$created_internal" "$created_iap" "$created_nat"
      die "failed to create dedicated subnet"
    fi
  fi
  if ! resource_exists routers "$ROUTER_NAME" "$STATE_REGION"; then
    if gcloud_project compute routers create "$ROUTER_NAME" --network="$NETWORK_NAME" --region="$STATE_REGION" \
      --description="$(ownership_description)"; then
      created_router=true
    else
      rollback_created_network_stack "$STATE_REGION" "$created_network" "$created_subnet" "$created_router" "$created_internal" "$created_iap" "$created_nat"
      die "failed to create Cloud Router"
    fi
  fi
  if ! resource_exists firewall-rules "$INTERNAL_FIREWALL_NAME"; then
    if gcloud_project compute firewall-rules create "$INTERNAL_FIREWALL_NAME" \
      --network="$NETWORK_NAME" --direction=INGRESS --action=ALLOW --priority=1000 \
      --rules=tcp:3306,tcp:33060,tcp:33061 \
      --source-ranges="$SUBNET_CIDR" --target-tags="$INTERNAL_TAG" \
      --description="$(ownership_description)"; then
      created_internal=true
    else
      rollback_created_network_stack "$STATE_REGION" "$created_network" "$created_subnet" "$created_router" "$created_internal" "$created_iap" "$created_nat"
      die "failed to create internal database firewall rule"
    fi
  fi
  if ! resource_exists firewall-rules "$IAP_FIREWALL_NAME"; then
    if gcloud_project compute firewall-rules create "$IAP_FIREWALL_NAME" \
      --network="$NETWORK_NAME" --direction=INGRESS --action=ALLOW --priority=1000 --rules=tcp:22 \
      --source-ranges=35.235.240.0/20 --target-tags="$IAP_SSH_TAG" \
      --description="$(ownership_description)"; then
      created_iap=true
    else
      rollback_created_network_stack "$STATE_REGION" "$created_network" "$created_subnet" "$created_router" "$created_internal" "$created_iap" "$created_nat"
      die "failed to create IAP firewall rule"
    fi
  fi
  if resource_exists nats "$NAT_NAME" "$STATE_REGION"; then
    nat_preexisting=true
  fi
  if [[ "$nat_preexisting" == true ]]; then
    if ! (validate_live_network_stack "$STATE_REGION" required); then
      rollback_created_network_stack "$STATE_REGION" "$created_network" "$created_subnet" "$created_router" "$created_internal" "$created_iap" "$created_nat"
      die "pre-existing network stack failed validation"
    fi
  else
    if ! (validate_live_network_stack "$STATE_REGION" pre-nat); then
      rollback_created_network_stack "$STATE_REGION" "$created_network" "$created_subnet" "$created_router" "$created_internal" "$created_iap" "$created_nat"
      die "non-billable network stack failed validation before Cloud NAT"
    fi
    if gcloud_project compute routers nats create "$NAT_NAME" --router="$ROUTER_NAME" --region="$STATE_REGION" \
      --nat-custom-subnet-ip-ranges="$SUBNET_NAME" --auto-allocate-nat-external-ips; then
      created_nat=true
    else
      rollback_created_network_stack "$STATE_REGION" "$created_network" "$created_subnet" "$created_router" "$created_internal" "$created_iap" "$created_nat"
      die "failed to create Cloud NAT"
    fi
    if ! (validate_live_network_stack "$STATE_REGION" required); then
      rollback_created_network_stack "$STATE_REGION" "$created_network" "$created_subnet" "$created_router" "$created_internal" "$created_iap" "$created_nat"
      die "network stack failed validation after Cloud NAT creation"
    fi
  fi

  for node in "${NODES[@]}"; do
    if resource_exists instances "$node" "$STATE_ZONE"; then
      log "$node already exists and preflight verified ownership"
      continue
    fi
    gcloud_project compute instances create "$node" \
      --zone="$STATE_ZONE" --machine-type="$STATE_MACHINE_TYPE" \
      --image-family=ubuntu-2204-lts --image-project=ubuntu-os-cloud \
      --boot-disk-size=20GB --boot-disk-type=pd-standard \
      --subnet="$SUBNET_NAME" --no-address --labels="${LABEL_KEY}=${RUN_LABEL},${MANAGED_LABEL}" \
      --tags="$INTERNAL_TAG,$IAP_SSH_TAG" \
      --format='value(name,status)'
    gcloud_project compute disks add-labels "$node" --zone="$STATE_ZONE" \
      --labels="${LABEL_KEY}=${RUN_LABEL},${MANAGED_LABEL}"
  done
  inventory
}

private_ip() {
  gcloud_project compute instances describe "$1" --zone="$STATE_ZONE" \
    --format='value(networkInterfaces[0].networkIP)'
}

ssh_node() {
  local node="$1"; shift
  ssh_node_with_timeout "$SSH_TIMEOUT_SECONDS" "$node" "$@"
}

ssh_node_with_timeout() {
  local operation_timeout="$1" node="$2" remote_command; shift 2
  printf -v remote_command '%q ' "$@"
  remote_command="${remote_command% }"
  CLOUDSDK_CORE_DISABLE_PROMPTS=1 timeout --foreground --kill-after=5s "${operation_timeout}s" \
    gcloud --project="$PROJECT" compute ssh "$node" --zone="$STATE_ZONE" \
    --tunnel-through-iap --quiet --ssh-flag=-oBatchMode=yes \
    --ssh-flag=-oConnectTimeout=10 --ssh-flag=-oServerAliveInterval=5 \
    --ssh-flag=-oServerAliveCountMax=2 --command="$remote_command"
}

scp_node() {
  local source="$1" destination="$2"
  CLOUDSDK_CORE_DISABLE_PROMPTS=1 timeout --foreground --kill-after=5s "${SSH_TIMEOUT_SECONDS}s" \
    gcloud --project="$PROJECT" compute scp "$source" "$destination" \
    --zone="$STATE_ZONE" --tunnel-through-iap --quiet \
    --scp-flag=-oBatchMode=yes --scp-flag=-oConnectTimeout=10
}

wait_for_ssh() {
  local node="$1" deadline now
  deadline=$(( $(date +%s) + SSH_READY_TIMEOUT_SECONDS ))
  while :; do
    if ssh_node "$node" true >/dev/null 2>&1; then return 0; fi
    now="$(date +%s)"; (( now < deadline )) || break
    sleep 5
  done
  die "SSH did not become ready for $node"
}

require_running_inventory() {
  local records name status owner managed count=0 expected
  records="$(gcloud_project compute instances list \
    --format="csv[no-heading,separator='|'](name,status,labels.${LABEL_KEY},labels.releem-topology-managed)" |
    expected_resource_records)"
  while IFS='|' read -r name status owner managed; do
    [[ -n "$name" ]] || continue
    expected=0
    for node in "${NODES[@]}"; do [[ "$name" == "$node" ]] && expected=1; done
    (( expected )) || die "unexpected matching instance: $name"
    [[ "$status" == "RUNNING" && "$owner" == "$RUN_LABEL" && "$managed" == "true" ]] ||
      die "instance $name is not RUNNING with exact run ownership"
    count=$((count + 1))
  done <<<"$records"
  [[ "$count" -eq "$REQUIRED_NODES" ]] || die "expected $REQUIRED_NODES running owned instances, found $count"
}

mysql_password_lines() {
  local count="${1:-16}" i
  for ((i=0; i<count; i++)); do printf '%s\n' "$MYSQL_CLUSTER_PASSWORD"; done
}

remote_bootstrap() {
  local node="$1" server_id="$2" hosts_file="$3"
  local mysqlsh_version_pattern='(^|[[:space:]])(Ver[[:space:]]+)?8\.4\.'
  {
    printf '%s\n' 'set -Eeuo pipefail'
    printf 'cluster_password=%q\n' "$MYSQL_CLUSTER_PASSWORD"
    printf 'releem_api_key=%q\n' "$RELEEM_API_KEY"
    printf 'node_name=%q\n' "$node"
    printf 'server_id=%q\n' "$server_id"
    printf 'mysqlsh_version_pattern=%q\n' "$mysqlsh_version_pattern"
    printf 'hosts=%q\n' "$hosts_file"
    cat <<'REMOTE'
umask 077
mysql_admin_user=releem_cluster_admin
monitoring_user=releem

sudo env DEBIAN_FRONTEND=noninteractive apt-get update -qq
sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl gnupg lsb-release ufw
curl -fsSL https://repo.mysql.com/RPM-GPG-KEY-mysql-2025 | gpg --dearmor | sudo tee /usr/share/keyrings/mysql.gpg >/dev/null
printf '%s\n' \
  'deb [arch=amd64 signed-by=/usr/share/keyrings/mysql.gpg] https://repo.mysql.com/apt/ubuntu jammy mysql-8.4-lts' \
  'deb [arch=amd64 signed-by=/usr/share/keyrings/mysql.gpg] https://repo.mysql.com/apt/ubuntu jammy mysql-tools' |
  sudo tee /etc/apt/sources.list.d/mysql.list >/dev/null
sudo env DEBIAN_FRONTEND=noninteractive apt-get update -qq
sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq mysql-community-server mysql-shell

printf '%s' "$hosts" | sudo tee /etc/hosts.releem-topology >/dev/null
sudo sed -i '/# releem-topology begin/,/# releem-topology end/d' /etc/hosts
{
  printf '%s\n' '# releem-topology begin'
  printf '%s' "$hosts"
  printf '%s\n' '# releem-topology end'
} | sudo tee -a /etc/hosts >/dev/null

config_candidate=$(mktemp)
cat >"$config_candidate" <<CFG
[mysqld]
server_id=${server_id}
report_host=${node_name}
report_port=3306
bind_address=0.0.0.0
mysqlx_bind_address=0.0.0.0
gtid_mode=ON
enforce_gtid_consistency=ON
log_bin=binlog
binlog_format=ROW
binlog_checksum=NONE
relay_log_recovery=ON
plugin_load_add=group_replication.so
loose-group_replication_start_on_boot=OFF
loose-group_replication_bootstrap_group=OFF
loose-group_replication_local_address=${node_name}:33061
CFG
if ! sudo cmp -s "$config_candidate" /etc/mysql/mysql.conf.d/releem-innodb-cluster.cnf; then
  member_online=$(sudo mysql --protocol=socket --batch --skip-column-names \
    -e "SELECT COUNT(*) FROM performance_schema.replication_group_members WHERE MEMBER_ID=@@server_uuid AND MEMBER_STATE='ONLINE';" 2>/dev/null || true)
  [[ "${member_online:-0}" == 0 ]] || {
    echo "refusing to restart an ONLINE Group Replication member with changed managed config" >&2
    exit 1
  }
  sudo install -o root -g root -m 644 "$config_candidate" /etc/mysql/mysql.conf.d/releem-innodb-cluster.cnf
  sudo systemctl restart mysql
fi
rm -f "$config_candidate"

escaped_password=${cluster_password//\\/\\\\}
escaped_password=${escaped_password//\'/\'\'}
admin_exists=$(sudo mysql --protocol=socket --batch --skip-column-names -e "SELECT COUNT(*) FROM mysql.user WHERE user='${mysql_admin_user}' AND host='%';")
read_only=$(sudo mysql --protocol=socket --batch --skip-column-names -e "SELECT @@global.read_only;")
if [[ "$read_only" == 1 ]]; then
  [[ "$admin_exists" != 0 ]] || {
    echo "cluster admin account is missing on read-only member $node_name" >&2
    exit 1
  }
else
  sudo mysql --protocol=socket <<SQL
CREATE USER IF NOT EXISTS '${mysql_admin_user}'@'%' IDENTIFIED BY '${escaped_password}';
ALTER USER '${mysql_admin_user}'@'%' IDENTIFIED BY '${escaped_password}';
GRANT ALL PRIVILEGES ON *.* TO '${mysql_admin_user}'@'%' WITH GRANT OPTION;
CREATE USER IF NOT EXISTS '${monitoring_user}'@'localhost' IDENTIFIED BY '${escaped_password}';
ALTER USER '${monitoring_user}'@'localhost' IDENTIFIED BY '${escaped_password}';
GRANT SELECT, PROCESS, REPLICATION CLIENT, SHOW VIEW ON *.* TO '${monitoring_user}'@'localhost';
GRANT SYSTEM_VARIABLES_ADMIN ON *.* TO '${monitoring_user}'@'localhost';
FLUSH PRIVILEGES;
SQL
fi

monitoring_login_ready=0
for _ in $(seq 1 30); do
  if MYSQL_PWD="$cluster_password" mysqladmin --socket=/var/run/mysqld/mysqld.sock --user="$monitoring_user" ping >/dev/null 2>&1; then
    monitoring_login_ready=1
    break
  fi
  sleep 2
done
[[ "$monitoring_login_ready" == 1 ]] || {
  echo "shared monitoring account is unavailable on $node_name" >&2
  exit 1
}

sudo ufw allow OpenSSH >/dev/null
sudo ufw allow from 10.212.0.0/24 to any port 3306 proto tcp >/dev/null
sudo ufw allow from 10.212.0.0/24 to any port 33060 proto tcp >/dev/null
sudo ufw allow from 10.212.0.0/24 to any port 33061 proto tcp >/dev/null
sudo ufw --force enable >/dev/null

mysql_version=$(mysql --version)
[[ "$mysql_version" == *'Ver 8.4.'* ]]
mysqlsh --version | grep -Eq "$mysqlsh_version_pattern"
sudo mysql -NBe "SELECT @@server_id, @@server_uuid, @@report_host, @@gtid_mode, @@log_bin" |
  awk -v id="$server_id" -v host="$node_name" '$1 == id && $2 != "" && $3 == host && $4 == "ON" && $5 == 1 {ok=1} END {exit !ok}'

agent_config_matches=0
if sudo test -f /opt/releem/releem.conf &&
  sudo grep -Fqx 'mysql_user="releem"' /opt/releem/releem.conf &&
  sudo grep -Fqx "mysql_password=\"$cluster_password\"" /opt/releem/releem.conf &&
  sudo grep -Fqx 'mysql_host="/var/run/mysqld/mysqld.sock"' /opt/releem/releem.conf; then
  agent_config_matches=1
fi
if [[ ! -x /opt/releem/releem-agent ]] ||
  ! systemctl cat releem-agent.service >/dev/null 2>&1 ||
  [[ "$agent_config_matches" != 1 ]]; then
  export RELEEM_API_KEY="$releem_api_key" RELEEM_ENV=dev RELEEM_DB_MEMORY_LIMIT=0 RELEEM_CRON_ENABLE=1
  export RELEEM_QUERY_OPTIMIZATION=true RELEEM_HOSTNAME="$node_name" RELEEM_INSTANCE_TYPE=local
  export RELEEM_MYSQL_HOST=/var/run/mysqld/mysqld.sock RELEEM_MYSQL_ROOT_LOGIN=root RELEEM_MYSQL_ROOT_PASSWORD=''
  export RELEEM_MYSQL_LOGIN="$monitoring_user" RELEEM_MYSQL_PASSWORD="$cluster_password"
  if ! install_output=$(sudo --preserve-env=RELEEM_API_KEY,RELEEM_ENV,RELEEM_DB_MEMORY_LIMIT,RELEEM_CRON_ENABLE,RELEEM_QUERY_OPTIMIZATION,RELEEM_HOSTNAME,RELEEM_INSTANCE_TYPE,RELEEM_MYSQL_HOST,RELEEM_MYSQL_LOGIN,RELEEM_MYSQL_PASSWORD,RELEEM_MYSQL_ROOT_LOGIN,RELEEM_MYSQL_ROOT_PASSWORD \
    bash /tmp/releem-install.sh </dev/null 2>&1); then
    echo "noninteractive Releem Agent installation failed on $node_name" >&2
    exit 1
  fi
  [[ "$install_output" != *"$releem_api_key"* ]] || { echo "installer attempted to expose API key" >&2; exit 1; }
  unset install_output
  unset RELEEM_API_KEY RELEEM_ENV RELEEM_DB_MEMORY_LIMIT RELEEM_CRON_ENABLE RELEEM_QUERY_OPTIMIZATION
  unset RELEEM_HOSTNAME RELEEM_INSTANCE_TYPE RELEEM_MYSQL_HOST RELEEM_MYSQL_LOGIN RELEEM_MYSQL_PASSWORD
  unset RELEEM_MYSQL_ROOT_LOGIN RELEEM_MYSQL_ROOT_PASSWORD
fi
owner=$(stat -c %u /opt/releem/releem-agent)
group=$(stat -c %g /opt/releem/releem-agent)
mode=$(stat -c %a /opt/releem/releem-agent)
sudo install -o "$owner" -g "$group" -m "$mode" /tmp/releem-agent-db-topology-x86_64 /opt/releem/releem-agent
sudo systemctl restart releem-agent.service
sudo systemctl is-active --quiet releem-agent.service
sha256sum /opt/releem/releem-agent | awk '{print $1}'
unset cluster_password releem_api_key escaped_password
REMOTE
  } | ssh_node_with_timeout "$BOOTSTRAP_TIMEOUT_SECONDS" "$node" sudo bash -s
}

mysqlsh_on() {
  local node="$1" js="$2"
  mysqlsh_on_with_timeout "$ADMINAPI_TIMEOUT_SECONDS" "$node" "$js"
}

mysqlsh_on_with_timeout() {
  local operation_timeout="$1" node="$2" js="$3"
  mysql_password_lines 24 | ssh_node_with_timeout "$operation_timeout" "$node" \
    mysqlsh --js --quiet-start=2 --passwords-from-stdin \
      --uri "${MYSQL_ADMIN_USER}@${node}:3306" --execute "$js"
}

mysqlsh_on_first_available() {
  local js="$1" node
  shift
  for node in "$@"; do
    if mysqlsh_on "$node" "$js"; then return 0; fi
  done
  return 1
}

extract_mysqlsh_json_object() {
  local parsed
  parsed="$(jq -Rrc '
    (try (if startswith("{") then fromjson else (sub("^[^{]*"; "") | fromjson) end) catch empty) |
    select(type == "object")
  ' | tail -n1)"
  [[ -n "$parsed" ]] || return 1
  printf '%s\n' "$parsed"
}

extract_clusterset_primary_name() {
  local primary
  primary="$(grep -Eo '(releem_cs_primary|releem_cs_replica)$' | tail -n1)" || return 1
  [[ -n "$primary" ]] || return 1
  printf '%s\n' "$primary"
}

extract_expected_node_name() {
  local node
  node="$(grep -Eo 'releem-ic-(single|multi|cs-primary|cs-replica)-[0-9]+$' | tail -n1)" || return 1
  is_expected_node "$node" || return 1
  printf '%s\n' "$node"
}

cluster_seed() {
  case "$1" in
    releem_single) printf '%s\n' releem-ic-single-1 ;;
    releem_multi) printf '%s\n' releem-ic-multi-1 ;;
    releem_cs_primary) printf '%s\n' releem-ic-cs-primary-1 ;;
    releem_cs_replica) printf '%s\n' releem-ic-cs-replica-1 ;;
    *) die "unknown cluster: $1" ;;
  esac
}

adminapi_cluster_exists() {
  local cluster="$1" seed="$2"
  mysqlsh_on "$seed" "try { dba.getCluster('${cluster}'); } catch (e) { shell.exit(1); }" >/dev/null 2>&1
}

adminapi_cluster_status() {
  local cluster="$1" seed output parsed attempt
  seed="$(cluster_seed "$cluster")"
  for attempt in $(seq 1 30); do
    output="$(mysqlsh_on "$seed" "print(JSON.stringify(dba.getCluster('${cluster}').status({extended:1})));" 2>&1)" || true
    if parsed="$(extract_mysqlsh_json_object <<<"$output")"; then
      printf '%s\n' "$parsed"
      return 0
    fi
    (( attempt < 30 )) && sleep 2
  done
  printf '%s\n' "$output" >&2
  return 1
}

adminapi_create_cluster() {
  local cluster="$1" seed="$2" mode="$3" options="{gtidSetIsComplete:true}"
  [[ "$mode" == multi ]] && options="{multiPrimary:true,force:true,gtidSetIsComplete:true}"
  mysqlsh_on "$seed" "var c=dba.createCluster('${cluster}',${options}); print(JSON.stringify(c.status({extended:1})));" >/dev/null
}

adminapi_add_instance() {
  local cluster="$1" node="$2" seed
  seed="$(cluster_seed "$cluster")"
  mysqlsh_on "$seed" "var c=dba.getCluster('${cluster}'); c.addInstance('${MYSQL_ADMIN_USER}@${node}:3306',{recoveryMethod:'clone'}); print(JSON.stringify(c.status({extended:1})));" >/dev/null
}

adminapi_rejoin_instance() {
  local cluster="$1" node="$2" seed
  seed="$(cluster_seed "$cluster")"
  mysqlsh_on "$seed" "var c=dba.getCluster('${cluster}'); c.rejoinInstance('${MYSQL_ADMIN_USER}@${node}:3306',{recoveryMethod:'incremental'});" >/dev/null
}

assert_cluster_compatible() {
  local status="$1" cluster="$2" mode="$3"; shift 3
  local expected_mode expected_json
  [[ "$mode" == single ]] && expected_mode=Single-Primary || expected_mode=Multi-Primary
  expected_json="$(printf '%s\n' "$@" | jq -Rsc 'split("\n")|map(select(length>0))|sort')"
  jq -e --arg cluster "$cluster" --arg mode "$expected_mode" --argjson expected "$expected_json" '
    .clusterName == $cluster and .defaultReplicaSet.topologyMode == $mode and
    ((.defaultReplicaSet.topology|keys|map(sub(":3306$";"")) - $expected)|length == 0)
  ' <<<"$status" >/dev/null || die "cluster $cluster has incompatible mode or member metadata"
}

assert_cluster_online() {
  local status="$1" cluster="$2" mode="$3" writer_policy="$4"; shift 4
  local expected_mode expected_json
  [[ "$mode" == single ]] && expected_mode=Single-Primary || expected_mode=Multi-Primary
  expected_json="$(printf '%s\n' "$@" | jq -Rsc 'split("\n")|map(select(length>0))|sort')"
  jq -e --arg cluster "$cluster" --arg mode "$expected_mode" --arg writers "$writer_policy" --argjson expected "$expected_json" '
    .clusterName == $cluster and .defaultReplicaSet.topologyMode == $mode and
    ((.defaultReplicaSet.topology|keys|map(sub(":3306$";""))|sort) == $expected) and
    (.defaultReplicaSet.topology|to_entries|all(.value.status == "ONLINE")) and
    (if $mode == "Single-Primary" then
       (.defaultReplicaSet.primary as $p |
         (.defaultReplicaSet.topology | has($p)) and
         .defaultReplicaSet.topology[$p].memberRole == "PRIMARY" and
         ([.defaultReplicaSet.topology|to_entries[]|select(.key != $p)|.value.memberRole]|all(. == "SECONDARY")) and
         (if $writers == "writable" then
            .defaultReplicaSet.topology[$p].mode == "R/W" and
            ([.defaultReplicaSet.topology[]|select(.mode == "R/W")]|length) == 1 and
            ([.defaultReplicaSet.topology[]|select(.mode == "R/O")]|length) == 2
          elif $writers == "fenced" then
            [.defaultReplicaSet.topology[].mode]|all(. == "R/O")
          else false end))
     else
       $writers == "writable" and
       ([.defaultReplicaSet.topology[]]|all(.mode == "R/W" and .memberRole == "PRIMARY"))
     end)
  ' <<<"$status" >/dev/null || die "cluster $cluster did not reach its exact ONLINE $expected_mode topology"
}

assert_cluster_state_for_label() {
  local status="$1" cluster="$2" mode="$3" label="$4"; shift 4
  local expected_mode expected_json expected_online=3 expected_rw
  [[ "$mode" == single ]] && { expected_mode=Single-Primary; expected_rw=1; } || { expected_mode=Multi-Primary; expected_rw=3; }
  case "$cluster:$label" in
    releem_single:single-primary-stopped|releem_single:single-secondary-promoted)
      expected_online=2
      expected_rw=1
      ;;
    releem_multi:multi-writer-offline)
      expected_online=2
      expected_rw=2
      ;;
    *)
      assert_cluster_online "$status" "$cluster" "$mode" writable "$@"
      return
      ;;
  esac
  expected_json="$(printf '%s\n' "$@" | jq -Rsc 'split("\n")|map(select(length>0))|sort')"
  jq -e --arg cluster "$cluster" --arg mode "$expected_mode" --argjson expected "$expected_json" \
    --argjson online "$expected_online" --argjson rw "$expected_rw" '
    .clusterName == $cluster and .defaultReplicaSet.topologyMode == $mode and
    ((.defaultReplicaSet.topology|keys|map(sub(":3306$";""))|sort) == $expected) and
    ([.defaultReplicaSet.topology[]|select(.status == "ONLINE")]|length) == $online and
    ([.defaultReplicaSet.topology[]|select(.status == "ONLINE" and .mode == "R/W")]|length) == $rw and
    ([.defaultReplicaSet.topology[]|select(.status != "ONLINE" and
      (.status == "OFFLINE" or .status == "MISSING" or .status == "(MISSING)" or .status == "UNREACHABLE"))]|length) == (3-$online) and
    (if $mode == "Single-Primary" then
       (.defaultReplicaSet.primary as $p |
         .defaultReplicaSet.topology[$p].status == "ONLINE" and
         .defaultReplicaSet.topology[$p].mode == "R/W" and
         .defaultReplicaSet.topology[$p].memberRole == "PRIMARY" and
         ([.defaultReplicaSet.topology|to_entries[]|select(.key != $p)|.value.memberRole]|all(. == "SECONDARY")))
     else ([.defaultReplicaSet.topology[].memberRole]|all(. == "PRIMARY")) end)
  ' <<<"$status" >/dev/null || die "cluster $cluster has invalid expected transition state for $label"
}

assert_clusterset_state() {
  local status="$1" label="$2" expected_primary="$3"
  jq -e --arg label "$label" --arg expected_primary "$expected_primary" '
    .domainName == "releem_clusterset" and
    .primaryCluster == $expected_primary and
    ($expected_primary == "releem_cs_primary" or $expected_primary == "releem_cs_replica") and
    ((.clusters|keys|sort) == ["releem_cs_primary","releem_cs_replica"]) and
    (.primaryCluster as $primary |
      (if $primary == "releem_cs_primary" then "releem_cs_replica" else "releem_cs_primary" end) as $replica |
      .clusters[$primary].clusterRole == "PRIMARY" and
      .clusters[$replica].clusterRole == "REPLICA" and
      (.globalPrimaryInstance|startswith(if $primary == "releem_cs_primary" then "releem-ic-cs-primary-" else "releem-ic-cs-replica-" end)) and
      (if $label == "clusterset-replication-stopped" then
         .status == "AVAILABLE" and .clusters[$primary].globalStatus == "OK" and
         .clusters[$replica].globalStatus == "OK_NOT_REPLICATING" and
         .clusters[$replica].clusterSetReplicationStatus == "STOPPED"
       else
         .status == "HEALTHY" and .clusters[$primary].globalStatus == "OK" and
         .clusters[$replica].globalStatus == "OK" and
         .clusters[$replica].clusterSetReplicationStatus == "OK"
       end))
  ' <<<"$status" >/dev/null || die "ClusterSet has invalid health or role semantics for $label"
}

assert_clusterset_clusters() {
  local clusterset="$1" cs_primary="$2" cs_replica="$3" primary
  primary="$(jq -r '.primaryCluster' <<<"$clusterset")"
  if [[ "$primary" == releem_cs_primary ]]; then
    assert_cluster_online "$cs_primary" releem_cs_primary single writable \
      releem-ic-cs-primary-1 releem-ic-cs-primary-2 releem-ic-cs-primary-3
    assert_cluster_online "$cs_replica" releem_cs_replica single fenced \
      releem-ic-cs-replica-1 releem-ic-cs-replica-2 releem-ic-cs-replica-3
  else
    assert_cluster_online "$cs_primary" releem_cs_primary single fenced \
      releem-ic-cs-primary-1 releem-ic-cs-primary-2 releem-ic-cs-primary-3
    assert_cluster_online "$cs_replica" releem_cs_replica single writable \
      releem-ic-cs-replica-1 releem-ic-cs-replica-2 releem-ic-cs-replica-3
  fi
  jq -e --arg primary "$primary" --argjson cs_primary "$cs_primary" --argjson cs_replica "$cs_replica" '
    (if $primary == "releem_cs_primary" then $cs_primary else $cs_replica end) as $primary_status |
    .globalPrimaryInstance == $primary_status.defaultReplicaSet.primary
  ' <<<"$clusterset" >/dev/null || die "ClusterSet global primary does not match its primary cluster writer"
}

ensure_cluster() {
  local cluster="$1" seed="$2" mode="$3" writer_policy=writable node status member_status
  if [[ "${4:-}" == writable || "${4:-}" == fenced ]]; then
    writer_policy="$4"
    shift 4
  else
    shift 3
  fi
  if ! adminapi_cluster_exists "$cluster" "$seed"; then
    adminapi_create_cluster "$cluster" "$seed" "$mode"
  fi
  status="$(adminapi_cluster_status "$cluster")"
  [[ -n "$status" ]] || die "missing AdminAPI status for $cluster"
  assert_cluster_compatible "$status" "$cluster" "$mode" "$@"
  for node in "$@"; do
    member_status="$(jq -r --arg node "${node}:3306" '.defaultReplicaSet.topology[$node].status // "ABSENT"' <<<"$status")"
    case "$member_status" in
      ABSENT) adminapi_add_instance "$cluster" "$node" ;;
      ONLINE) ;;
      OFFLINE|MISSING|'(MISSING)'|UNREACHABLE) adminapi_rejoin_instance "$cluster" "$node" ;;
      *) die "cluster $cluster member $node has unsafe AdminAPI state $member_status" ;;
    esac
  done
  status="$(adminapi_cluster_status "$cluster")"
  assert_cluster_online "$status" "$cluster" "$mode" "$writer_policy" "$@"
}

adminapi_clusterset_exists() {
  mysqlsh_on releem-ic-cs-primary-1 \
    "try { dba.getCluster('releem_cs_primary').getClusterSet(); } catch (e) { shell.exit(1); }" >/dev/null 2>&1
}

adminapi_create_clusterset() {
  mysqlsh_on releem-ic-cs-primary-1 \
    "dba.getCluster('releem_cs_primary').createClusterSet('releem_clusterset');" >/dev/null
}

adminapi_replica_cluster_attached() {
  mysqlsh_on releem-ic-cs-primary-1 \
    "var s=dba.getCluster('releem_cs_primary').getClusterSet().status({extended:1}); if (!s.clusters || !s.clusters.releem_cs_replica) shell.exit(1);" >/dev/null 2>&1
}

adminapi_attach_replica_cluster() {
  if adminapi_cluster_exists releem_cs_replica releem-ic-cs-replica-1; then
    mysqlsh_on releem-ic-cs-replica-1 \
      "dba.getCluster('releem_cs_replica').dissolve({force:true});" >/dev/null
  fi
  mysqlsh_on releem-ic-cs-primary-1 \
    "var cs=dba.getCluster('releem_cs_primary').getClusterSet(); cs.createReplicaCluster('${MYSQL_ADMIN_USER}@releem-ic-cs-replica-1:3306','releem_cs_replica',{recoveryMethod:'clone'});" >/dev/null
}

ensure_clusterset() {
  adminapi_clusterset_exists || adminapi_create_clusterset
  adminapi_replica_cluster_attached || adminapi_attach_replica_cluster
}

configure_clusters() {
  local clusterset_primary
  ensure_cluster releem_single releem-ic-single-1 single writable \
    releem-ic-single-1 releem-ic-single-2 releem-ic-single-3
  ensure_cluster releem_multi releem-ic-multi-1 multi writable \
    releem-ic-multi-1 releem-ic-multi-2 releem-ic-multi-3
  if ! adminapi_clusterset_exists; then
    ensure_cluster releem_cs_primary releem-ic-cs-primary-1 single writable \
      releem-ic-cs-primary-1 releem-ic-cs-primary-2 releem-ic-cs-primary-3
  fi
  ensure_clusterset
  clusterset_primary="$(clusterset_primary_name)"
  case "$clusterset_primary" in
    releem_cs_primary)
      ensure_cluster releem_cs_primary releem-ic-cs-primary-1 single writable \
        releem-ic-cs-primary-1 releem-ic-cs-primary-2 releem-ic-cs-primary-3
      ensure_cluster releem_cs_replica releem-ic-cs-replica-1 single fenced \
        releem-ic-cs-replica-1 releem-ic-cs-replica-2 releem-ic-cs-replica-3
      ;;
    releem_cs_replica)
      ensure_cluster releem_cs_primary releem-ic-cs-primary-1 single fenced \
        releem-ic-cs-primary-1 releem-ic-cs-primary-2 releem-ic-cs-primary-3
      ensure_cluster releem_cs_replica releem-ic-cs-replica-1 single writable \
        releem-ic-cs-replica-1 releem-ic-cs-replica-2 releem-ic-cs-replica-3
      ;;
    *) die "unknown ClusterSet primary cluster" ;;
  esac
}

configure() {
  validate_run_label
  require_api_key
  require_cluster_password
  require_command gcloud
  require_command sha256sum
  preflight
  load_state
  validate_live_network_stack "$STATE_REGION" required
  require_running_inventory

  local binary=/tmp/releem-agent-db-topology-x86_64 checksum hosts='' node ip index=0 clusterset_primary
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /usr/local/go/bin/go build -buildvcs=false -o "$binary" .
  chmod 755 "$binary"
  checksum="$(sha256sum "$binary" | awk '{print $1}')"
  for node in "${NODES[@]}"; do
    ip="$(private_ip "$node")"
    [[ "$ip" =~ ^10\.212\.0\.[0-9]+$ ]] || die "unexpected dedicated-subnet address for $node"
    hosts+="$ip $node"$'\n'
  done

  for node in "${NODES[@]}"; do
    wait_for_ssh "$node"
    scp_node "$binary" "$node:/tmp/releem-agent-db-topology-x86_64"
    scp_node install.sh "$node:/tmp/releem-install.sh"
    index=$((index + 1))
    remote_checksum="$(remote_bootstrap "$node" "$((1200 + index))" "$hosts" | tail -n1)"
    [[ "$remote_checksum" == "$checksum" ]] || die "binary checksum mismatch on $node"
  done
  configure_clusters
  clusterset_primary="$(clusterset_primary_name)"
  verify_cluster_state healthy-configured "$clusterset_primary"
  inventory
}

marker() {
  local transition="$1" value
  value="$(date -u +%Y%m%dT%H%M%S.%NZ)-${transition}"
  mkdir -p "$EVIDENCE_DIR/$RUN_LABEL/transitions"
  printf '%s\t%s\n' "$(date -u +%s%3N)" "$value" >>"$EVIDENCE_DIR/$RUN_LABEL/transitions/markers.tsv"
  printf '%s\n' "$value"
}

marker_epoch_ms() {
  local mark="$1" markers="$EVIDENCE_DIR/$RUN_LABEL/transitions/markers.tsv"
  awk -F'\t' -v m="$mark" '$2 == m {print $1; found=1} END {exit !found}' "$markers" | tail -n1
}

write_expectation() {
  local mark="$1" json="$2" file="$EVIDENCE_DIR/$RUN_LABEL/transitions/${mark}.expectation.json"
  jq -e . <<<"$json" >"$file" || die "invalid transition expectation"
  chmod 600 "$file"
}

trigger_collection() {
  local node
  for node in "${NODES[@]}"; do
    ssh_node "$node" sudo systemctl restart releem-agent.service >/dev/null
    ssh_node "$node" sudo systemctl is-active --quiet releem-agent.service
  done
}

verify_cluster_state() {
  local label="$1" expected_clusterset_primary="$2" out captured_at single multi cs_primary cs_replica clusterset
  out="$EVIDENCE_DIR/$RUN_LABEL/${label}.json"
  mkdir -p "$EVIDENCE_DIR/$RUN_LABEL"
  single="$(mysqlsh_on_first_available "var c=dba.getCluster('releem_single'); print(JSON.stringify(c.status({extended:1})))" releem-ic-single-1 releem-ic-single-2 releem-ic-single-3 | extract_mysqlsh_json_object)"
  multi="$(mysqlsh_on_first_available "var c=dba.getCluster('releem_multi'); print(JSON.stringify(c.status({extended:1})))" releem-ic-multi-1 releem-ic-multi-2 releem-ic-multi-3 | extract_mysqlsh_json_object)"
  cs_primary="$(mysqlsh_on_first_available "var c=dba.getCluster('releem_cs_primary'); print(JSON.stringify(c.status({extended:1})))" releem-ic-cs-primary-1 releem-ic-cs-primary-2 releem-ic-cs-primary-3 | extract_mysqlsh_json_object)"
  cs_replica="$(mysqlsh_on_first_available "var c=dba.getCluster('releem_cs_replica'); print(JSON.stringify(c.status({extended:1})))" releem-ic-cs-replica-1 releem-ic-cs-replica-2 releem-ic-cs-replica-3 | extract_mysqlsh_json_object)"
  clusterset="$(mysqlsh_on_first_available "var c=dba.getCluster(); print(JSON.stringify(c.getClusterSet().status({extended:1})))" releem-ic-cs-primary-1 releem-ic-cs-replica-1 | extract_mysqlsh_json_object)"

  assert_cluster_state_for_label "$single" releem_single single "$label" \
    releem-ic-single-1 releem-ic-single-2 releem-ic-single-3
  assert_cluster_state_for_label "$multi" releem_multi multi "$label" \
    releem-ic-multi-1 releem-ic-multi-2 releem-ic-multi-3
  assert_clusterset_state "$clusterset" "$label" "$expected_clusterset_primary"
  assert_clusterset_clusters "$clusterset" "$cs_primary" "$cs_replica"

  captured_at="$(date -u +%FT%TZ)"
  jq -n --arg label "$label" --arg captured_at "$captured_at" \
    --argjson single "$single" --argjson multi "$multi" \
    --argjson cs_primary "$cs_primary" --argjson cs_replica "$cs_replica" \
    --argjson clusterset "$clusterset" \
    '{label:$label,captured_at:$captured_at,clusters:{releem_single:$single,releem_multi:$multi,releem_cs_primary:$cs_primary,releem_cs_replica:$cs_replica},clusterset:$clusterset}' \
    >"$out"
  chmod 600 "$out"
}

require_persistence_env() {
  local name
  for name in TOPOLOGY_MYSQL_HOST TOPOLOGY_MYSQL_USER TOPOLOGY_MYSQL_PASSWORD \
    TOPOLOGY_CLICKHOUSE_HOST TOPOLOGY_CLICKHOUSE_USER TOPOLOGY_CLICKHOUSE_PASSWORD; do
    [[ -n "${!name:-}" ]] || die "$name must be set for persistence validation"
  done
  [[ "${TOPOLOGY_UID:-}" =~ ^[1-9][0-9]{0,9}$ ]] || die "TOPOLOGY_UID must be a positive numeric tenant UID"
  (( TOPOLOGY_UID <= 4294967295 )) || die "TOPOLOGY_UID is outside UInt32 range"
  [[ "$TOPOLOGY_CLICKHOUSE_HOST" =~ ^[A-Za-z0-9.-]+$ ]] || die "TOPOLOGY_CLICKHOUSE_HOST must be a bare hostname or address"
  [[ "${TOPOLOGY_CLICKHOUSE_SCHEME:-http}" == http || "${TOPOLOGY_CLICKHOUSE_SCHEME:-http}" == https ]] ||
    die "TOPOLOGY_CLICKHOUSE_SCHEME must be http or https"
  [[ "${TOPOLOGY_CLICKHOUSE_PORT:-8123}" =~ ^[1-9][0-9]{0,4}$ ]] || die "invalid TOPOLOGY_CLICKHOUSE_PORT"
  (( ${TOPOLOGY_CLICKHOUSE_PORT:-8123} <= 65535 )) || die "invalid TOPOLOGY_CLICKHOUSE_PORT"
}

tenant_predicate() {
  local alias="$1"
  [[ "$alias" =~ ^[a-z][a-z0-9_]*$ ]] || die "invalid SQL alias"
  [[ "${TOPOLOGY_UID:-}" =~ ^[1-9][0-9]{0,9}$ ]] || die "invalid TOPOLOGY_UID"
  printf '%s.uid = %s\n' "$alias" "$TOPOLOGY_UID"
}

platform_mysql() {
  local query="$1" MYSQL_PWD="$TOPOLOGY_MYSQL_PASSWORD"
  export MYSQL_PWD
  timeout "${MYSQL_QUERY_TIMEOUT_SECONDS}s" mysql --batch --raw --skip-column-names \
    --connect-timeout="$MYSQL_CONNECT_TIMEOUT_SECONDS" \
    --host="$TOPOLOGY_MYSQL_HOST" --port="${TOPOLOGY_MYSQL_PORT:-3306}" \
    --user="$TOPOLOGY_MYSQL_USER" "${TOPOLOGY_MYSQL_DATABASE:-releemdb}" --execute "$query"
}

platform_clickhouse() {
  local query="$1"
  printf 'user = "%s:%s"\n' "$TOPOLOGY_CLICKHOUSE_USER" "$TOPOLOGY_CLICKHOUSE_PASSWORD" |
    timeout "$((CLICKHOUSE_QUERY_TIMEOUT_SECONDS + 5))s" curl --config - --fail --silent --show-error \
      --connect-timeout "$CLICKHOUSE_CONNECT_TIMEOUT_SECONDS" --max-time "$CLICKHOUSE_QUERY_TIMEOUT_SECONDS" --data-binary "$query" \
      "${TOPOLOGY_CLICKHOUSE_SCHEME:-http}://${TOPOLOGY_CLICKHOUSE_HOST}:${TOPOLOGY_CLICKHOUSE_PORT:-8123}/?database=${TOPOLOGY_CLICKHOUSE_DATABASE:-releemdb_dev}"
}

current_state_query() {
  local hostnames="$1" marker_ms="$2"
  printf '%s\n' "SELECT JSON_OBJECT('sid',s.sid,'last_rid',r.last_rid,'hostname',s.hostname,'relation_type',r.relation_type,'group_key',r.group_key,'parent_group_key',r.parent_group_key,'member_key',r.member_key,'primary_member_key',r.primary_member_key,'role',r.role,'is_writer',r.is_writer,'is_reader',r.is_reader,'replication_state',r.replication_state,'last_seen_epoch_ms',r.last_seen_epoch_ms) FROM servers s JOIN db_topology_relation_members r ON r.sid=s.sid AND r.uid=${TOPOLOGY_UID} WHERE $(tenant_predicate s) AND s.hostname IN (${hostnames}) AND r.last_seen_epoch_ms >= ${marker_ms} ORDER BY s.sid,r.relation_type,r.group_key;"
}

upstream_state_query() {
  local hostnames="$1"
  printf '%s\n' "SELECT JSON_OBJECT('sid',u.sid,'channel_key',u.channel_key,'upstream_member_key',u.upstream_member_key,'replication_state',u.replication_state,'last_seen_epoch_ms',u.last_seen_epoch_ms) FROM db_topology_upstreams u JOIN db_topology_members m ON m.group_id=u.group_id AND m.sid=u.sid AND m.last_seen_epoch_ms=u.last_seen_epoch_ms AND m.last_rid <=> u.last_rid JOIN db_topology_groups g ON g.id=m.group_id AND g.uid=${TOPOLOGY_UID} JOIN servers s ON s.sid=u.sid WHERE $(tenant_predicate s) AND s.hostname IN (${hostnames}) ORDER BY u.sid,u.channel_key;"
}

sid_inventory_query() {
  local hostnames="$1"
  printf '%s\n' "SELECT GROUP_CONCAT(s.sid ORDER BY s.sid) FROM servers s WHERE $(tenant_predicate s) AND s.hostname IN (${hostnames});"
}

node_sql_list() {
  local node first=1
  for node in "${NODES[@]}"; do
    (( first )) || printf ','
    printf "'%s'" "$node"
    first=0
  done
}

assert_current_state() {
  local mark="$1" marker_ms="$2" file="$3" transition="${1#*-}"
  jq -s -e --arg mark "$transition" --argjson marker_ms "$marker_ms" '
    def semantic($prefix; $type): map(select(.hostname | startswith($prefix)) | select(.relation_type == $type));
    . as $all |
    ($all | semantic("releem-ic-cs-"; "innodb_clusterset")) as $clusterset |
    ($clusterset | map(select(.role == "primary_cluster"))) as $primary_cluster |
    ($clusterset | map(select(.role == "replica_cluster"))) as $replica_cluster |
    ($primary_cluster | map(.hostname)) as $primary_hosts |
    ($all | semantic("releem-ic-cs-"; "innodb_cluster") |
      map(select(.hostname as $hostname | ($primary_hosts | index($hostname)) != null)) |
      map(.group_key) | unique) as $primary_group_keys |
    ($all | length > 0) and
    ($all | all((.last_seen_epoch_ms | type) == "number" and .last_seen_epoch_ms >= $marker_ms)) and
    ($all | group_by([.relation_type,.group_key]) | all((map(.member_key)|unique|length) == length)) and
    ($all | group_by([.sid,.relation_type,.group_key]) | all(length == 1)) and
    (($all | semantic("releem-ic-single-"; "innodb_cluster") | length) == 3) and
    (($all | semantic("releem-ic-single-"; "innodb_cluster") | map(.group_key) | unique | length) == 1) and
    (($all | semantic("releem-ic-multi-"; "innodb_cluster") | length) == 3) and
    (($all | semantic("releem-ic-multi-"; "innodb_cluster") | map(.group_key) | unique | length) == 1) and
    (($all | semantic("releem-ic-cs-"; "innodb_cluster") | length) == 6) and
    (($all | semantic("releem-ic-cs-"; "innodb_cluster") | map(.group_key) | unique | length) == 2) and
    (($clusterset | length) == 6) and
    (($clusterset | map(.group_key) | unique | length) == 1) and
    (($primary_cluster | length) == 3) and
    (($primary_cluster | all(.parent_group_key == null)) and (($primary_cluster | map(select(.is_writer == 1)) | length) == 1)) and
    (($replica_cluster | length) == 3) and
    (($replica_cluster | all(.parent_group_key != null and .is_writer == 0))) and
    (($replica_cluster | map(.parent_group_key) | unique) == $primary_group_keys) and
    (if ($mark | startswith("single-")) then
       (($all | semantic("releem-ic-single-"; "innodb_cluster") | map(select(.is_writer == 1)) | length) == 1)
     else true end) and
    (if $mark == "multi-writer-offline" then
       (($all | semantic("releem-ic-multi-"; "innodb_cluster") | map(select(.is_writer == 1)) | length) == 2)
     elif $mark == "multi-healthy" or $mark == "multi-writer-rejoined" then
       (($all | semantic("releem-ic-multi-"; "innodb_cluster") | map(select(.is_writer == 1)) | length) == 3)
     else true end) and
    (if $mark == "clusterset-replication-stopped" then
       ($all | map(select((.relation_type == "async_replication" or .relation_type == "innodb_clusterset") and .replication_state == "stopped")) | length) > 0
     else true end)
  ' "$file" >/dev/null || die "current-state topology assertions failed for marker $mark"
}

assert_transition_state() {
  local current_file="$1" upstream_file="$2" expectation_file="$3"
  jq -s -e --slurpfile e "$expectation_file" '
    ($e[0]) as $x |
    (group_by([.relation_type,.group_key]) | all((map(.member_key)|unique|length) == length)) and
    if $x.transition == "single-primary-stopped" then
      (map(select(.hostname|startswith("releem-ic-single-")) | select(.relation_type=="innodb_cluster")) as $r |
       ($r|map(select(.hostname==$x.target_hostname and .member_key==$x.old_primary_member_key and .is_writer==0))|length)==1 and
       ($r|map(.primary_member_key)|unique)==[$x.new_primary_member_key] and
       ($r|map(select(.member_key==$x.new_primary_member_key and .is_writer==1))|length)==1)
    elif $x.transition == "single-secondary-promoted" then
      (map(select(.hostname|startswith("releem-ic-single-")) | select(.relation_type=="innodb_cluster")) as $r |
       ($r|map(select(.hostname==$x.target_hostname and .member_key==$x.new_primary_member_key and .is_writer==1))|length)==1 and
       ($r|map(.primary_member_key)|unique)==[$x.new_primary_member_key] and
       ($r|map(select(.member_key==$x.old_primary_member_key and .is_writer==0))|length)==1)
    elif $x.transition == "single-old-primary-rejoined" then
      (map(select(.hostname==$x.target_hostname and .member_key==$x.target_member_key and .replication_state=="healthy" and .is_writer==0))|length)==1 and
      (map(select(.hostname|startswith("releem-ic-single-")) | select(.relation_type=="innodb_cluster") | .primary_member_key)|unique)==[$x.new_primary_member_key]
    elif $x.transition == "multi-writer-offline" then
      (map(select(.hostname==$x.target_hostname and .member_key==$x.target_member_key and .is_writer==0 and (.replication_state=="error" or .replication_state=="stopped")))|length)==1
    elif $x.transition == "multi-writer-rejoined" then
      (map(select(.hostname==$x.target_hostname and .member_key==$x.target_member_key and .is_writer==1 and .replication_state=="healthy"))|length)==1
    elif $x.transition == "clusterset-primary-switchover" then
      (map(select((.hostname|startswith($x.old_primary_cluster)) and .relation_type=="innodb_clusterset"))|length)==3 and
      (map(select((.hostname|startswith($x.old_primary_cluster)) and .relation_type=="innodb_clusterset"))|all(.role=="replica_cluster" and .is_writer==0 and .parent_group_key != null)) and
      (map(select((.hostname|startswith($x.new_primary_cluster)) and .relation_type=="innodb_clusterset"))|length)==3 and
      (map(select((.hostname|startswith($x.new_primary_cluster)) and .relation_type=="innodb_clusterset"))|all(.role=="primary_cluster" and .parent_group_key == null)) and
      (map(select((.hostname|startswith($x.new_primary_cluster)) and .relation_type=="innodb_clusterset" and .is_writer==1))|length)==1
    elif $x.transition == "clusterset-replication-stopped" or $x.transition == "clusterset-replication-resumed" then true
    else false end
  ' "$current_file" >/dev/null || die "exact transition identity assertion failed"

  jq -s -e --slurpfile e "$expectation_file" '
    ($e[0]) as $x |
    if $x.transition == "clusterset-replication-stopped" then
      (map(select(.sid==$x.target_sid and .channel_key=="clusterset_replication" and .replication_state=="stopped"))|length)==1
    elif $x.transition == "clusterset-replication-resumed" then
      (map(select(.sid==$x.target_sid and .channel_key=="clusterset_replication" and .replication_state=="healthy"))|length)==1
    elif $x.transition == "clusterset-primary-switchover" then
      (map(select(.sid==$x.new_replica_sid and .channel_key=="clusterset_replication" and .replication_state=="healthy"))|length)==1
    elif $x.transition == "single-primary-stopped" or $x.transition == "single-secondary-promoted" or
         $x.transition == "single-old-primary-rejoined" or $x.transition == "multi-writer-offline" or
         $x.transition == "multi-writer-rejoined" then true
    else false end
  ' "$upstream_file" >/dev/null || die "exact upstream transition assertion failed"
}

build_clickhouse_observation_query() {
  local marker_ms="$1" pairs_csv="$2" pair sid rid first=1 conditions=''
  IFS=',' read -ra pairs <<<"$pairs_csv"
  for pair in "${pairs[@]}"; do
    sid="${pair%%:*}"; rid="${pair#*:}"
    [[ "$sid" =~ ^[0-9]+$ && "$rid" =~ ^[A-Za-z0-9._-]{1,255}$ ]] || die "invalid SID/RID observation correlation"
    (( first )) || conditions+=' OR '
    conditions+="(sid = ${sid} AND rid = '${rid}')"
    first=0
  done
  (( first == 0 )) || die "no SID/RID observation correlations supplied"
  printf '%s\n' "SELECT sid,rid,toUnixTimestamp(timestamp) * 1000 AS observed_epoch_ms,relations FROM db_topology_observations WHERE uid = ${TOPOLOGY_UID} AND timestamp >= toDateTime(intDiv(${marker_ms}, 1000)) AND timestamp < toDateTime(intDiv(${marker_ms}, 1000) + ${CLICKHOUSE_MARKER_WINDOW_SECONDS}) AND (${conditions}) ORDER BY sid,timestamp ASC FORMAT JSONEachRow"
}

assert_selected_observations() {
  local file="$1" expected="$2" pairs_csv="$3" marker_ms="$4" current_file="$5"
  jq -s -e --slurpfile current "$current_file" --argjson expected "$expected" --arg pairs "$pairs_csv" \
    --argjson marker_ms "$marker_ms" --argjson window "$CLICKHOUSE_MARKER_WINDOW_SECONDS" '
    ($pairs | split(",") | map(split(":") | {sid:(.[0]|tonumber),rid:.[1]}) | sort_by(.sid,.rid)) as $expected_pairs |
    (($marker_ms / 1000 | floor) * 1000) as $marker_floor_ms |
    length == $expected and
    (map(.sid)|unique|length)==$expected and
    (map([.sid,.rid])|unique|length)==$expected and
    (map({sid,rid})|sort_by(.sid,.rid)) == $expected_pairs and
    all(.observed_epoch_ms >= $marker_floor_ms and
        .observed_epoch_ms < ($marker_floor_ms + ($window * 1000))) and
    all(. as $observation |
      ($observation.relations|fromjson) as $relations |
      ($current | map(select(.sid == $observation.sid))) as $expected_relations |
      ($relations|type)=="array" and ($relations|length)>0 and
      (($relations|map([.Type,.GroupKey])|unique|length)==($relations|length)) and
      ($expected_relations|length)>0 and
      (($relations | map({relation_type:.Type,group_key:.GroupKey,member_key:.MemberKey,parent_group_key:.ParentGroupKey,role:.Role,is_writer:(if .IsWriter then 1 else 0 end),replication_state:.ReplicationState}) | sort_by(.relation_type,.group_key)) ==
       ($expected_relations | map({relation_type,group_key,member_key,parent_group_key,role,is_writer,replication_state}) | sort_by(.relation_type,.group_key)))
    )
  ' "$file" >/dev/null || die "selected observations are missing, duplicated, or relation-invalid"
}

poll_clickhouse_observations() {
  local marker_ms="$1" pairs_csv="$2" expected="$3" current_file="$4" output_file="$5"
  local query deadline now
  query="$(build_clickhouse_observation_query "$marker_ms" "$pairs_csv")"
  deadline=$(( $(date +%s) + CLICKHOUSE_PERSISTENCE_TIMEOUT_SECONDS ))
  while :; do
    platform_clickhouse "$query" >"$output_file"
    if [[ -s "$output_file" ]] &&
      (assert_selected_observations "$output_file" "$expected" "$pairs_csv" "$marker_ms" "$current_file" 2>/dev/null); then
      return 0
    fi
    now="$(date +%s)"; (( now < deadline )) || die "ClickHouse persistence timeout"
    sleep 10
  done
}

current_sid_rid_pairs() {
  local file="$1" expected="$2"
  jq -r -s --argjson expected "$expected" '
    group_by(.sid) |
    if length != $expected then error("unexpected SID count") else . end |
    map(if (map(.last_rid)|unique|length)==1 and .[0].last_rid != null
        then ((.[0].sid|tostring)+":"+.[0].last_rid) else error("ambiguous SID/RID") end) |
    join(",")' "$file"
}

collect() {
  validate_run_label
  require_api_key
  require_cluster_password
  require_persistence_env
  require_command mysql
  require_command curl
  require_command jq
  load_state
  require_running_inventory
  local mark="${1:-$(marker manual-collect)}" marker_ms evidence_dir mysql_deadline now hostnames sids
  local pairs_csv expectation
  marker_ms="$(marker_epoch_ms "$mark")"
  hostnames="$(node_sql_list)"
  evidence_dir="$EVIDENCE_DIR/$RUN_LABEL/persistence/$mark"
  mkdir -p "$evidence_dir"
  trigger_collection

  expectation="$EVIDENCE_DIR/$RUN_LABEL/transitions/${mark}.expectation.json"
  mysql_deadline=$(( $(date +%s) + MYSQL_PERSISTENCE_TIMEOUT_SECONDS ))
  while :; do
    platform_mysql "SELECT COUNT(DISTINCT s.sid) FROM servers s JOIN db_topology_relation_members r ON r.sid=s.sid AND r.uid=${TOPOLOGY_UID} WHERE $(tenant_predicate s) AND s.hostname IN (${hostnames}) AND r.last_seen_epoch_ms >= ${marker_ms};" >"$evidence_dir/mysql-count.txt"
    platform_mysql "$(current_state_query "$hostnames" "$marker_ms")" >"$evidence_dir/mysql-current.jsonl"
    platform_mysql "$(upstream_state_query "$hostnames")" >"$evidence_dir/mysql-upstreams.jsonl"
    if [[ "$(cat "$evidence_dir/mysql-count.txt")" == "$REQUIRED_NODES" ]] &&
      (assert_current_state "$mark" "$marker_ms" "$evidence_dir/mysql-current.jsonl" 2>/dev/null) &&
      { [[ ! -f "$expectation" ]] || (assert_transition_state "$evidence_dir/mysql-current.jsonl" "$evidence_dir/mysql-upstreams.jsonl" "$expectation" 2>/dev/null); }; then
      break
    fi
    now="$(date +%s)"; (( now < mysql_deadline )) || die "Platform MySQL persistence timeout for marker $mark"
    sleep 10
  done

  assert_current_state "$mark" "$marker_ms" "$evidence_dir/mysql-current.jsonl"
  [[ ! -f "$expectation" ]] || assert_transition_state "$evidence_dir/mysql-current.jsonl" "$evidence_dir/mysql-upstreams.jsonl" "$expectation"

  sids="$(platform_mysql "$(sid_inventory_query "$hostnames")")"
  [[ "$sids" =~ ^[0-9]+(,[0-9]+){11}$ ]] || die "expected exactly 12 SIDs"
  pairs_csv="$(current_sid_rid_pairs "$evidence_dir/mysql-current.jsonl" "$REQUIRED_NODES")"
  [[ "$(tr ',' '\n' <<<"$pairs_csv" | wc -l)" -eq "$REQUIRED_NODES" ]] || die "expected one current RID for each SID"
  poll_clickhouse_observations "$marker_ms" "$pairs_csv" "$REQUIRED_NODES" \
    "$evidence_dir/mysql-current.jsonl" "$evidence_dir/clickhouse-observations.jsonl"
  awk -F'\t' 'NR>1 && $1<p {exit 1} {p=$1}' "$EVIDENCE_DIR/$RUN_LABEL/transitions/markers.tsv" 2>/dev/null ||
    die "transition markers are not monotonic"
  jq -n --arg marker "$mark" --argjson marker_epoch_ms "$marker_ms" --arg sids "$sids" \
    '{marker:$marker,marker_epoch_ms:$marker_epoch_ms,sids:($sids|split(",")|map(tonumber)),mysql_current:"mysql-current.jsonl",mysql_upstreams:"mysql-upstreams.jsonl",clickhouse:"clickhouse-observations.jsonl",result:"PASS"}' \
    >"$evidence_dir/summary.json"
  log "persistence validated for marker $mark"
}

exercise_step() {
  local name="$1" expected_clusterset_primary="$2"; shift 2
  local mark
  mark="$(marker "$name")"
  "$@"
  sleep "${TOPOLOGY_SETTLE_SECONDS:-20}"
  verify_cluster_state "$name" "$expected_clusterset_primary"
  collect "$mark"
}

mysql_scalar() {
  local node="$1" query="$2"
  ssh_node "$node" sudo mysql --batch --skip-column-names -e "$query" | tail -n1
}

server_uuid() {
  mysql_scalar "$1" 'SELECT @@server_uuid;'
}

cluster_primary_host() {
  local cluster="$1" seed="$2"
  mysqlsh_on "$seed" "var r=session.runSql('SELECT MEMBER_HOST FROM performance_schema.replication_group_members WHERE MEMBER_ROLE=\\\"PRIMARY\\\" AND MEMBER_STATE=\\\"ONLINE\\\" LIMIT 1'); var x=r.fetchOne(); if (x) print(x[0]);" |
    extract_expected_node_name
}

wait_for_new_primary() {
  local seed="$1" old_primary="$2" deadline now candidate
  deadline=$(( $(date +%s) + ADMINAPI_TIMEOUT_SECONDS ))
  while :; do
    candidate="$(cluster_primary_host releem_single "$seed" 2>/dev/null || true)"
    if [[ "$candidate" =~ ^releem-ic-single-[1-3]$ && "$candidate" != "$old_primary" ]]; then
      printf '%s\n' "$candidate"
      return
    fi
    now="$(date +%s)"; (( now < deadline )) || die "single-primary promotion timeout"
    sleep 5
  done
}

wait_for_replica_channel_state() {
  local node="$1" expected="$2" deadline now state
  deadline=$(( $(date +%s) + ADMINAPI_TIMEOUT_SECONDS ))
  while :; do
    state="$(mysql_scalar "$node" "SELECT CONCAT(SERVICE_STATE,':',(SELECT SERVICE_STATE FROM performance_schema.replication_applier_status_by_coordinator WHERE CHANNEL_NAME='clusterset_replication')) FROM performance_schema.replication_connection_status WHERE CHANNEL_NAME='clusterset_replication';" 2>/dev/null || true)"
    case "$expected:$state" in
      stopped:OFF:OFF|healthy:ON:ON) return ;;
    esac
    now="$(date +%s)"; (( now < deadline )) || die "ClusterSet channel did not reach $expected"
    sleep 5
  done
}

sid_for_hostname() {
  local hostname="$1" sid
  is_expected_node "$hostname" || die "invalid topology hostname"
  sid="$(platform_mysql "SELECT s.sid FROM servers s WHERE $(tenant_predicate s) AND s.hostname='${hostname}' AND s.is_deleted=0 ORDER BY s.sid DESC LIMIT 1;")"
  [[ "$sid" =~ ^[0-9]+$ ]] || die "missing tenant-scoped SID for $hostname"
  printf '%s\n' "$sid"
}

clusterset_primary_name() {
  mysqlsh_on_first_available \
    "var s=dba.getCluster().getClusterSet().status({extended:1}); print(s.primaryCluster);" \
    releem-ic-cs-primary-1 releem-ic-cs-replica-1 | extract_clusterset_primary_name
}

clusterset_switchover() {
  local target="$1" node
  for node in releem-ic-cs-primary-1 releem-ic-cs-replica-1; do
    if mysqlsh_on_with_timeout "$((SWITCHOVER_TIMEOUT_SECONDS + SSH_TIMEOUT_SECONDS))" "$node" \
      "var cs=dba.getCluster().getClusterSet(); cs.setPrimaryCluster('${target}',{timeout:${SWITCHOVER_TIMEOUT_SECONDS}}); print(JSON.stringify(cs.status({extended:1})));"; then
      return 0
    fi
  done
  return 1
}

exercise() {
  validate_run_label
  require_api_key
  require_cluster_password
  require_persistence_env
  load_state
  require_running_inventory

  local single_primary single_primary_uuid single_new_primary single_new_uuid single_rejoin_source mark expectation
  local multi_target= replica_cluster= releem_cs_current= releem_cs_target= replica_seed= replica_primary= replica_sid=
  local old_cs_primary_host= old_cs_primary_sid=
  releem_cs_current="$(clusterset_primary_name)"
  [[ "$releem_cs_current" == releem_cs_primary || "$releem_cs_current" == releem_cs_replica ]] || die "unknown ClusterSet primary cluster"
  single_primary="$(cluster_primary_host releem_single releem-ic-single-1)"
  [[ "$single_primary" =~ ^releem-ic-single-[1-3]$ ]] || die "could not identify the single-primary writer"
  single_primary_uuid="$(server_uuid "$single_primary")"
  single_rejoin_source=releem-ic-single-1
  [[ "$single_rejoin_source" != "$single_primary" ]] || single_rejoin_source=releem-ic-single-2

  exercise_step single-healthy "$releem_cs_current" true
  mark="$(marker single-primary-stopped)"
  ssh_node "$single_primary" sudo mysql -e 'STOP GROUP_REPLICATION'
  single_new_primary="$(wait_for_new_primary "$single_rejoin_source" "$single_primary")"
  single_new_uuid="$(server_uuid "$single_new_primary")"
  expectation="$(jq -n --arg transition single-primary-stopped --arg target "$single_primary" --arg old "$single_primary_uuid" --arg new "$single_new_uuid" '{transition:$transition,target_hostname:$target,target_member_key:$old,old_primary_member_key:$old,new_primary_member_key:$new}')"
  write_expectation "$mark" "$expectation"
  sleep "${TOPOLOGY_SETTLE_SECONDS:-20}"; verify_cluster_state single-primary-stopped "$releem_cs_current"; collect "$mark"

  mark="$(marker single-secondary-promoted)"
  expectation="$(jq -n --arg transition single-secondary-promoted --arg target "$single_new_primary" --arg old "$single_primary_uuid" --arg new "$single_new_uuid" '{transition:$transition,target_hostname:$target,old_primary_member_key:$old,new_primary_member_key:$new}')"
  write_expectation "$mark" "$expectation"
  verify_cluster_state single-secondary-promoted "$releem_cs_current"; collect "$mark"

  mark="$(marker single-old-primary-rejoined)"
  mysqlsh_on "$single_rejoin_source" \
    "var c=dba.getCluster('releem_single'); c.rejoinInstance('${MYSQL_ADMIN_USER}@${single_primary}:3306',{recoveryMethod:'incremental'}); print(JSON.stringify(c.status({extended:1})))"
  expectation="$(jq -n --arg transition single-old-primary-rejoined --arg hostname "$single_primary" --arg target "$single_primary_uuid" --arg new "$single_new_uuid" '{transition:$transition,target_hostname:$hostname,target_member_key:$target,new_primary_member_key:$new}')"
  write_expectation "$mark" "$expectation"
  sleep "${TOPOLOGY_SETTLE_SECONDS:-20}"; verify_cluster_state single-old-primary-rejoined "$releem_cs_current"; collect "$mark"

  exercise_step multi-healthy "$releem_cs_current" true
  multi_target="$(server_uuid releem-ic-multi-2)"
  mark="$(marker multi-writer-offline)"
  ssh_node releem-ic-multi-2 sudo mysql -e 'STOP GROUP_REPLICATION'
  expectation="$(jq -n --arg transition multi-writer-offline --arg hostname releem-ic-multi-2 --arg target "$multi_target" '{transition:$transition,target_hostname:$hostname,target_member_key:$target}')"
  write_expectation "$mark" "$expectation"
  sleep "${TOPOLOGY_SETTLE_SECONDS:-20}"; verify_cluster_state multi-writer-offline "$releem_cs_current"; collect "$mark"
  mark="$(marker multi-writer-rejoined)"
  mysqlsh_on releem-ic-multi-1 \
    "var c=dba.getCluster('releem_multi'); c.rejoinInstance('${MYSQL_ADMIN_USER}@releem-ic-multi-2:3306',{recoveryMethod:'incremental'}); print(JSON.stringify(c.status({extended:1})))"
  expectation="$(jq -n --arg transition multi-writer-rejoined --arg hostname releem-ic-multi-2 --arg target "$multi_target" '{transition:$transition,target_hostname:$hostname,target_member_key:$target}')"
  write_expectation "$mark" "$expectation"
  sleep "${TOPOLOGY_SETTLE_SECONDS:-20}"; verify_cluster_state multi-writer-rejoined "$releem_cs_current"; collect "$mark"

  exercise_step clusterset-healthy "$releem_cs_current" true
  if [[ "$releem_cs_current" == releem_cs_primary ]]; then replica_cluster=releem_cs_replica; replica_seed=releem-ic-cs-replica-1; else replica_cluster=releem_cs_primary; replica_seed=releem-ic-cs-primary-1; fi
  replica_primary="$(cluster_primary_host "$replica_cluster" "$replica_seed")"
  replica_sid="$(sid_for_hostname "$replica_primary")"

  mark="$(marker clusterset-replication-stopped)"
  ssh_node "$replica_primary" sudo mysql -e "STOP REPLICA FOR CHANNEL 'clusterset_replication'"
  wait_for_replica_channel_state "$replica_primary" stopped
  expectation="$(jq -n --arg transition clusterset-replication-stopped --argjson sid "$replica_sid" '{transition:$transition,target_sid:$sid}')"
  write_expectation "$mark" "$expectation"
  verify_cluster_state clusterset-replication-stopped "$releem_cs_current"; collect "$mark"

  mark="$(marker clusterset-replication-resumed)"
  ssh_node "$replica_primary" sudo mysql -e "START REPLICA FOR CHANNEL 'clusterset_replication'"
  wait_for_replica_channel_state "$replica_primary" healthy
  expectation="$(jq -n --arg transition clusterset-replication-resumed --argjson sid "$replica_sid" '{transition:$transition,target_sid:$sid}')"
  write_expectation "$mark" "$expectation"
  verify_cluster_state clusterset-replication-resumed "$releem_cs_current"; collect "$mark"

  mark="$(marker clusterset-primary-switchover)"
  [[ "$releem_cs_current" == releem_cs_primary ]] && releem_cs_target=releem_cs_replica || releem_cs_target=releem_cs_primary
  old_cs_primary_host="$(cluster_primary_host "$releem_cs_current" "$(cluster_seed "$releem_cs_current")")"
  old_cs_primary_sid="$(sid_for_hostname "$old_cs_primary_host")"
  clusterset_switchover "$releem_cs_target"
  expectation="$(jq -n --arg transition clusterset-primary-switchover \
    --arg old "$( [[ "$releem_cs_current" == releem_cs_primary ]] && printf releem-ic-cs-primary- || printf releem-ic-cs-replica- )" \
    --arg new "$( [[ "$releem_cs_target" == releem_cs_primary ]] && printf releem-ic-cs-primary- || printf releem-ic-cs-replica- )" \
    --argjson new_replica_sid "$old_cs_primary_sid" \
    '{transition:$transition,old_primary_cluster:$old,new_primary_cluster:$new,new_replica_sid:$new_replica_sid}')"
  write_expectation "$mark" "$expectation"
  sleep "${TOPOLOGY_SETTLE_SECONDS:-20}"; verify_cluster_state clusterset-primary-switchover "$releem_cs_target"; collect "$mark"
}

inventory() {
  if [[ "${1:-}" == "--names-only" ]]; then printf '%s\n' "${NODES[@]}"; return; fi
  validate_run_label
  require_command gcloud
  load_state
  gcloud_project compute instances list \
    --filter="labels.${LABEL_KEY}=${RUN_LABEL} AND labels.releem-topology-managed=true" \
    --format="table(name,zone.basename(),status,machineType.basename(),labels.${LABEL_KEY})"
  log "expected machine count: $REQUIRED_NODES; public and private addresses intentionally omitted"
}

destroy() {
  validate_run_label
  [[ "${GCP_TOPOLOGY_CONFIRM_DESTROY:-}" == "$RUN_LABEL" ]] ||
    die "GCP_TOPOLOGY_CONFIRM_DESTROY must equal the exact run label"
  local records disks name zone status owner managed users stack_dir region firewall_name
  records="$(gcloud_project compute instances list \
    --filter="labels.${LABEL_KEY}=${RUN_LABEL} AND labels.releem-topology-managed=true" \
    --format="csv[no-heading,separator='|'](name,zone.basename(),labels.${LABEL_KEY},labels.releem-topology-managed)")"
  while IFS='|' read -r name zone owner managed; do
    [[ -n "$name" ]] || continue
    is_expected_node "$name" || die "refusing unexpected instance delete: $name"
    [[ "$owner" == "$RUN_LABEL" && "$managed" == "true" ]] || die "refusing unowned instance delete: $name"
    gcloud_project compute instances delete "$name" --zone="$zone" --quiet
  done <<<"$records"

  disks="$(gcloud_project compute disks list \
    --filter="labels.${LABEL_KEY}=${RUN_LABEL} AND labels.releem-topology-managed=true" \
    --format="csv[no-heading,separator='|'](name,zone.basename(),status,labels.${LABEL_KEY},labels.releem-topology-managed,users)")"
  while IFS='|' read -r name zone status owner managed users; do
    [[ -n "$name" ]] || continue
    is_expected_node "$name" || die "refusing unexpected disk delete: $name"
    [[ "$owner" == "$RUN_LABEL" && "$managed" == "true" && -z "$users" ]] ||
      die "refusing attached or unowned disk delete: $name"
    gcloud_project compute disks delete "$name" --zone="$zone" --quiet
  done <<<"$disks"

  stack_dir="$EVIDENCE_DIR/$RUN_LABEL/destroy-network"
  inventory_network_stack '' "$stack_dir"
  region="$(jq -r '[.[0].region // empty][0] // "" | split("/")[-1]' "$stack_dir/subnet.json")"
  [[ -n "$region" ]] || region="$(jq -r '[.[0].region // empty][0] // "" | split("/")[-1]' "$stack_dir/router.json")"
  [[ -n "$region" ]] || region=unused
  validate_network_stack "$region" "$stack_dir/network.json" "$stack_dir/subnet.json" "$stack_dir/router.json" "$stack_dir/nat.json" "$stack_dir/firewall.json" optional

  if jq -e 'length == 1' "$stack_dir/nat.json" >/dev/null; then
    gcloud_project compute routers nats delete "$NAT_NAME" --router="$ROUTER_NAME" --region="$region" --quiet
  fi
  if jq -e 'length == 1' "$stack_dir/router.json" >/dev/null; then
    gcloud_project compute routers delete "$ROUTER_NAME" --region="$region" --quiet
  fi
  while IFS= read -r firewall_name; do
    [[ -n "$firewall_name" ]] && gcloud_project compute firewall-rules delete "$firewall_name" --quiet
  done < <(jq -r '.[].name' "$stack_dir/firewall.json")
  if jq -e 'length == 1' "$stack_dir/subnet.json" >/dev/null; then
    gcloud_project compute networks subnets delete "$SUBNET_NAME" --region="$region" --quiet
  fi
  if jq -e 'length == 1' "$stack_dir/network.json" >/dev/null; then
    gcloud_project compute networks delete "$NETWORK_NAME" --quiet
  fi
}

main() {
  case "${1:-help}" in
    help|-h|--help) usage ;;
    preflight) shift; [[ $# -eq 0 ]] || die "preflight takes no arguments"; preflight ;;
    create) shift; [[ $# -eq 0 ]] || die "create takes no arguments"; create ;;
    configure) shift; [[ $# -eq 0 ]] || die "configure takes no arguments"; configure ;;
    exercise) shift; [[ $# -eq 0 ]] || die "exercise takes no arguments"; exercise ;;
    collect) shift; [[ $# -le 1 ]] || die "collect accepts at most one marker"; collect "${1:-}" ;;
    inventory) shift; [[ $# -le 1 ]] || die "inventory accepts only --names-only"; inventory "${1:-}" ;;
    destroy) shift; [[ $# -eq 0 ]] || die "destroy takes no arguments"; destroy ;;
    *) usage >&2; die "unknown command: $1" ;;
  esac
}

main "$@"
