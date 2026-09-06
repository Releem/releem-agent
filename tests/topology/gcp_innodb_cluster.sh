#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly PROJECT="static-mediator-400907"
readonly LABEL_KEY="releem-topology-run"
readonly MANAGED_LABEL="releem-topology-managed=true"
readonly NETWORK_TAG="releem-ic-internal"
readonly REQUIRED_NODES=12
readonly EVIDENCE_DIR="${GCP_TOPOLOGY_EVIDENCE_DIR:-/tmp/releem-db-topology-evidence/gcp}"
readonly STATE_DIR="${GCP_TOPOLOGY_STATE_DIR:-/tmp/releem-db-topology-state/gcp}"
readonly RUN_LABEL="${GCP_TOPOLOGY_RUN_LABEL:-}"
readonly MYSQL_ADMIN_USER="releem_cluster_admin"
readonly MYSQL_REPO_KEY_URL="https://repo.mysql.com/RPM-GPG-KEY-mysql-2025"

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
  MYSQL_CLUSTER_PASSWORD      Ephemeral AdminAPI password; never written by this script

Persistence validation variables for collect/exercise:
  TOPOLOGY_MYSQL_HOST, TOPOLOGY_MYSQL_USER, TOPOLOGY_MYSQL_PASSWORD
  TOPOLOGY_MYSQL_DATABASE (default: releemdb_dev), TOPOLOGY_MYSQL_PORT (default: 3306)
  TOPOLOGY_CLICKHOUSE_URL, TOPOLOGY_CLICKHOUSE_USER, TOPOLOGY_CLICKHOUSE_PASSWORD
  TOPOLOGY_CLICKHOUSE_DATABASE (default: releemdb_dev)

Set GCP_TOPOLOGY_CONFIRM_DESTROY to the exact run label before destroy.
EOF
}

validate_run_label() {
  [[ "$RUN_LABEL" =~ ^[a-z][a-z0-9-]{0,39}$ ]] ||
    die "GCP_TOPOLOGY_RUN_LABEL must match ^[a-z][a-z0-9-]{0,39}$"
}

require_api_key() {
  [[ -n "${RELEEM_API_KEY:-}" ]] || die "RELEEM_API_KEY must be set at runtime"
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
  local kind="$1" records="$2" name owner managed
  while IFS='|' read -r name _ _ owner managed; do
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
    {name:.[0],zone:.[1],status:.[2],run_label:.[3],managed:(.[4] == "true")})'
}

firewall_records_json() {
  jq -Rsc 'split("\n") | map(select(length > 0) | split("|") |
    {name:.[0],ownership_marker:.[1]})'
}

preflight() {
  validate_run_label
  require_command gcloud
  require_command jq
  local lifecycle zones zone region quotas machine_type machine_info existing_count missing_count candidate_region
  local instances disks firewalls evidence existing_zones

  lifecycle="$(gcloud_project projects describe "$PROJECT" --format='value(lifecycleState)')"
  [[ "$lifecycle" == "ACTIVE" ]] || die "project $PROJECT is not ACTIVE"

  instances="$(gcloud_project compute instances list \
    --filter='name~^releem-ic-(single|multi|cs-primary|cs-replica)-[1-3]$' \
    --format="csv[no-heading,separator='|'](name,zone.basename(),status,labels.${LABEL_KEY},labels.releem-topology-managed)")"
  disks="$(gcloud_project compute disks list \
    --filter='name~^releem-ic-(single|multi|cs-primary|cs-replica)-[1-3]$' \
    --format="csv[no-heading,separator='|'](name,zone.basename(),status,labels.${LABEL_KEY},labels.releem-topology-managed)")"
  firewalls="$(gcloud_project compute firewall-rules list \
    --filter="name=releem-ic-internal-${RUN_LABEL}" \
    --format="csv[no-heading,separator='|'](name,description)")"

  assert_owned_or_absent instance "$instances"
  assert_owned_or_absent disk "$disks"
  if [[ -n "$firewalls" ]] && ! grep -Fq "owner=${LABEL_KEY}:${RUN_LABEL}" <<<"$firewalls"; then
    die "firewall ownership collision for another run"
  fi

  zones="$(gcloud_project compute zones list --filter='status=UP' --format='value(name,status)')"
  existing_zones="$(cut -d'|' -f2 <<<"$instances" | sed '/^$/d' | sort -u)"
  if [[ "$(printf '%s\n' "$existing_zones" | sed '/^$/d' | wc -l)" -gt 1 ]]; then
    die "owned instances span multiple zones"
  fi
  existing_count="$(printf '%s\n' "$instances" | sed '/^$/d' | wc -l)"
  (( existing_count <= REQUIRED_NODES )) || die "matching instance inventory exceeds $REQUIRED_NODES"
  missing_count=$((REQUIRED_NODES - existing_count))

  if [[ -n "$existing_zones" ]]; then
    zone="$existing_zones"
    grep -Eq "^${zone}[[:space:]]+UP$" <<<"$zones" || die "selected zone $zone is not UP"
    region="${zone%-*}"
    quotas="$(gcloud_project compute regions describe "$region" --format=json)"
    quota_available "$quotas" CPUS "$((missing_count * 2))" ||
      die "region $region lacks conservative CPU quota for $missing_count missing nodes"
    quota_available "$quotas" IN_USE_ADDRESSES "$missing_count" ||
      die "region $region lacks address quota for $missing_count missing nodes"
  else
    zone=''
    while read -r candidate_zone _; do
      [[ -n "$candidate_zone" ]] || continue
      candidate_region="${candidate_zone%-*}"
      quotas="$(gcloud_project compute regions describe "$candidate_region" --format=json)"
      if quota_available "$quotas" CPUS "$((missing_count * 2))" &&
        quota_available "$quotas" IN_USE_ADDRESSES "$missing_count"; then
        zone="$candidate_zone"
        region="$candidate_region"
        break
      fi
    done <<<"$zones"
    [[ -n "$zone" ]] || die "no UP zone has sufficient CPU and address quota for $missing_count nodes"
  fi

  machine_type="e2-small"
  machine_info="$(gcloud_project compute machine-types describe "$machine_type" --zone="$zone" --format='value(guestCpus,memoryMb)')"
  [[ -n "$machine_info" ]] || die "machine type $machine_type is unavailable in $zone"

  write_state "$zone" "$region" "$machine_type"
  evidence="$EVIDENCE_DIR/$RUN_LABEL/preflight.json"
  jq -n \
    --arg project "$PROJECT" --arg run_label "$RUN_LABEL" --arg zone "$zone" \
    --arg region "$region" --arg machine_type "$machine_type" \
    --argjson expected_nodes "$REQUIRED_NODES" \
    --argjson existing_instances "$(printf '%s\n' "$instances" | sed '/^$/d' | wc -l)" \
    --argjson existing_disks "$(printf '%s\n' "$disks" | sed '/^$/d' | wc -l)" \
    --argjson existing_firewalls "$(printf '%s\n' "$firewalls" | sed '/^$/d' | wc -l)" \
    --argjson cpu_quota "$(quota_json "$quotas" CPUS)" \
    --argjson address_quota "$(quota_json "$quotas" IN_USE_ADDRESSES)" \
    --argjson instance_inventory "$(resource_records_json <<<"$instances")" \
    --argjson disk_inventory "$(resource_records_json <<<"$disks")" \
    --argjson firewall_inventory "$(firewall_records_json <<<"$firewalls")" \
    '{project:$project,run_label:$run_label,zone:$zone,region:$region,machine_type:$machine_type,expected_nodes:$expected_nodes,selected_quota:{CPUS:$cpu_quota,IN_USE_ADDRESSES:$address_quota},existing:{instances:$existing_instances,disks:$existing_disks,firewalls:$existing_firewalls},inventory:{instances:$instance_inventory,disks:$disk_inventory,firewalls:$firewall_inventory},result:"PASS"}' \
    >"$evidence"
  chmod 600 "$evidence"
  log "preflight passed: project=$PROJECT zone=$zone machine=$machine_type nodes=$REQUIRED_NODES"
}

resource_exists() {
  local kind="$1" name="$2" zone="${3:-}"
  if [[ -n "$zone" ]]; then
    gcloud_project compute "$kind" describe "$name" --zone="$zone" >/dev/null 2>&1
  else
    gcloud_project compute "$kind" describe "$name" >/dev/null 2>&1
  fi
}

create() {
  validate_run_label
  require_api_key
  preflight
  load_state
  local firewall="releem-ic-internal-${RUN_LABEL}" node
  if ! resource_exists firewall-rules "$firewall"; then
    gcloud_project compute firewall-rules create "$firewall" \
      --network=default --direction=INGRESS --action=ALLOW \
      --rules=tcp:3306,tcp:33060,tcp:33061 \
      --source-tags="$NETWORK_TAG" --target-tags="$NETWORK_TAG" \
      --description="owner=${LABEL_KEY}:${RUN_LABEL}; managed-by=tests/topology/gcp_innodb_cluster.sh"
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
      --labels="${LABEL_KEY}=${RUN_LABEL},${MANAGED_LABEL}" --tags="$NETWORK_TAG" \
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
  gcloud_project compute ssh "$node" --zone="$STATE_ZONE" --quiet -- "$@"
}

wait_for_ssh() {
  local node="$1" attempt
  for attempt in $(seq 1 60); do
    if ssh_node "$node" true >/dev/null 2>&1; then return 0; fi
    sleep 5
  done
  die "SSH did not become ready for $node"
}

require_running_inventory() {
  local records name status owner managed count=0 expected
  records="$(gcloud_project compute instances list \
    --filter='name~^releem-ic-(single|multi|cs-primary|cs-replica)-[1-3]$' \
    --format="csv[no-heading,separator='|'](name,status,labels.${LABEL_KEY},labels.releem-topology-managed)")"
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
  {
    printf '%s\n%s\n%s\n%s\n' "$MYSQL_CLUSTER_PASSWORD" "$RELEEM_API_KEY" "$node" "$server_id"
    printf '%s\n' '__HOSTS__'
    printf '%s\n' "$hosts_file"
    printf '%s\n' '__SCRIPT__'
    cat <<'REMOTE'
set -Eeuo pipefail
umask 077
IFS= read -r cluster_password
IFS= read -r releem_api_key
IFS= read -r node_name
IFS= read -r server_id
IFS= read -r marker
[[ "$marker" == '__HOSTS__' ]]
mysql_admin_user=releem_cluster_admin
hosts=''
while IFS= read -r line; do
  [[ "$line" == '__SCRIPT__' ]] && break
  hosts+="$line"$'\n'
done

export DEBIAN_FRONTEND=noninteractive
sudo apt-get update -qq
sudo apt-get install -y -qq ca-certificates curl gnupg lsb-release ufw
curl -fsSL https://repo.mysql.com/RPM-GPG-KEY-mysql-2025 | gpg --dearmor | sudo tee /usr/share/keyrings/mysql.gpg >/dev/null
printf '%s\n' \
  'deb [arch=amd64 signed-by=/usr/share/keyrings/mysql.gpg] https://repo.mysql.com/apt/ubuntu jammy mysql-8.4-lts' \
  'deb [arch=amd64 signed-by=/usr/share/keyrings/mysql.gpg] https://repo.mysql.com/apt/ubuntu jammy mysql-tools' |
  sudo tee /etc/apt/sources.list.d/mysql.list >/dev/null
sudo apt-get update -qq
sudo apt-get install -y -qq mysql-community-server mysql-shell

printf '%s' "$hosts" | sudo tee /etc/hosts.releem-topology >/dev/null
sudo sed -i '/# releem-topology begin/,/# releem-topology end/d' /etc/hosts
{
  printf '%s\n' '# releem-topology begin'
  printf '%s' "$hosts"
  printf '%s\n' '# releem-topology end'
} | sudo tee -a /etc/hosts >/dev/null

sudo tee /etc/mysql/mysql.conf.d/releem-innodb-cluster.cnf >/dev/null <<CFG
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
loose-group_replication_group_seeds=releem-ic-single-1:33061,releem-ic-single-2:33061,releem-ic-single-3:33061,releem-ic-multi-1:33061,releem-ic-multi-2:33061,releem-ic-multi-3:33061,releem-ic-cs-primary-1:33061,releem-ic-cs-primary-2:33061,releem-ic-cs-primary-3:33061,releem-ic-cs-replica-1:33061,releem-ic-cs-replica-2:33061,releem-ic-cs-replica-3:33061
CFG
sudo systemctl restart mysql

escaped_password=${cluster_password//\\/\\\\}
escaped_password=${escaped_password//\'/\'\'}
sudo mysql --protocol=socket <<SQL
CREATE USER IF NOT EXISTS '${mysql_admin_user}'@'%' IDENTIFIED BY '${escaped_password}';
ALTER USER '${mysql_admin_user}'@'%' IDENTIFIED BY '${escaped_password}';
GRANT ALL PRIVILEGES ON *.* TO '${mysql_admin_user}'@'%' WITH GRANT OPTION;
FLUSH PRIVILEGES;
SQL

sudo ufw allow OpenSSH >/dev/null
sudo ufw allow from 10.0.0.0/8 to any port 3306 proto tcp >/dev/null
sudo ufw allow from 10.0.0.0/8 to any port 33060 proto tcp >/dev/null
sudo ufw allow from 10.0.0.0/8 to any port 33061 proto tcp >/dev/null
sudo ufw --force enable >/dev/null

mysql_version=$(mysql --version)
[[ "$mysql_version" == *'Ver 8.4.'* ]]
mysqlsh --version | grep -Eq 'MySQL Shell 8\.4\.'
sudo mysql -NBe "SELECT @@server_id, @@server_uuid, @@report_host, @@gtid_mode, @@log_bin" |
  awk -v id="$server_id" -v host="$node_name" '$1 == id && $2 != "" && $3 == host && $4 == "ON" && $5 == 1 {ok=1} END {exit !ok}'

if [[ ! -x /opt/releem/releem-agent ]]; then
  export RELEEM_API_KEY="$releem_api_key" RELEEM_ENV=dev RELEEM_HOSTNAME="$node_name" RELEEM_MYSQL_ROOT_PASSWORD=''
  sudo --preserve-env=RELEEM_API_KEY,RELEEM_ENV,RELEEM_HOSTNAME,RELEEM_MYSQL_ROOT_PASSWORD \
    bash /tmp/releem-install.sh >/tmp/releem-install.log 2>&1
  unset RELEEM_API_KEY RELEEM_ENV RELEEM_HOSTNAME RELEEM_MYSQL_ROOT_PASSWORD
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
  } | ssh_node "$node" sudo bash -s
}

mysqlsh_on() {
  local node="$1" js="$2"
  mysql_password_lines 24 | ssh_node "$node" \
    mysqlsh --js --quiet-start=2 --passwords-from-stdin --uri "${MYSQL_ADMIN_USER}@${node}:3306" --execute "$js"
}

mysqlsh_on_first_available() {
  local js="$1" node
  shift
  for node in "$@"; do
    if mysqlsh_on "$node" "$js"; then return 0; fi
  done
  return 1
}

configure_clusters() {
  mysqlsh_on releem-ic-single-1 \
    "var c=dba.getCluster('releem_single'); print(JSON.stringify(c.status({extended:1})))" >/dev/null 2>&1 ||
  mysqlsh_on releem-ic-single-1 \
    "var c=dba.createCluster('releem_single',{gtidSetIsComplete:true}); c.addInstance('${MYSQL_ADMIN_USER}@releem-ic-single-2:3306',{recoveryMethod:'clone'}); c.addInstance('${MYSQL_ADMIN_USER}@releem-ic-single-3:3306',{recoveryMethod:'clone'}); c.status({extended:1})"

  mysqlsh_on releem-ic-multi-1 \
    "var c=dba.getCluster('releem_multi'); print(JSON.stringify(c.status({extended:1})))" >/dev/null 2>&1 ||
  mysqlsh_on releem-ic-multi-1 \
    "var c=dba.createCluster('releem_multi',{multiPrimary:true,gtidSetIsComplete:true}); c.addInstance('${MYSQL_ADMIN_USER}@releem-ic-multi-2:3306',{recoveryMethod:'clone'}); c.addInstance('${MYSQL_ADMIN_USER}@releem-ic-multi-3:3306',{recoveryMethod:'clone'}); c.status({extended:1})"

  mysqlsh_on releem-ic-cs-primary-1 \
    "var c=dba.getCluster('releem_cs_primary'); var cs=c.getClusterSet(); print(JSON.stringify(cs.status({extended:1})))" >/dev/null 2>&1 ||
  mysqlsh_on releem-ic-cs-primary-1 \
    "var c=dba.createCluster('releem_cs_primary',{gtidSetIsComplete:true}); c.addInstance('${MYSQL_ADMIN_USER}@releem-ic-cs-primary-2:3306',{recoveryMethod:'clone'}); c.addInstance('${MYSQL_ADMIN_USER}@releem-ic-cs-primary-3:3306',{recoveryMethod:'clone'}); var cs=c.createClusterSet('releem_clusterset'); var r=cs.createReplicaCluster('${MYSQL_ADMIN_USER}@releem-ic-cs-replica-1:3306','releem_cs_replica',{recoveryMethod:'clone'}); r.addInstance('${MYSQL_ADMIN_USER}@releem-ic-cs-replica-2:3306',{recoveryMethod:'clone'}); r.addInstance('${MYSQL_ADMIN_USER}@releem-ic-cs-replica-3:3306',{recoveryMethod:'clone'}); cs.status({extended:1})"
}

configure() {
  validate_run_label
  require_api_key
  require_cluster_password
  require_command gcloud
  require_command sha256sum
  load_state
  preflight
  require_running_inventory

  local binary=/tmp/releem-agent-db-topology-x86_64 checksum hosts='' node ip index=0
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /usr/local/go/bin/go build -buildvcs=false -o "$binary" .
  chmod 755 "$binary"
  checksum="$(sha256sum "$binary" | awk '{print $1}')"
  for node in "${NODES[@]}"; do
    ip="$(private_ip "$node")"
    [[ "$ip" =~ ^10\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "unexpected private address for $node"
    hosts+="$ip $node"$'\n'
  done

  for node in "${NODES[@]}"; do
    wait_for_ssh "$node"
    gcloud_project compute scp "$binary" "$node:/tmp/releem-agent-db-topology-x86_64" --zone="$STATE_ZONE" --quiet
    gcloud_project compute scp install.sh "$node:/tmp/releem-install.sh" --zone="$STATE_ZONE" --quiet
    index=$((index + 1))
    remote_checksum="$(remote_bootstrap "$node" "$((1200 + index))" "$hosts" | tail -n1)"
    [[ "$remote_checksum" == "$checksum" ]] || die "binary checksum mismatch on $node"
  done
  configure_clusters
  verify_cluster_state healthy-configured
  inventory
}

marker() {
  local transition="$1" value
  value="$(date -u +%Y%m%dT%H%M%S.%NZ)-${transition}"
  mkdir -p "$EVIDENCE_DIR/$RUN_LABEL/transitions"
  printf '%s\t%s\n' "$(date -u +%s%3N)" "$value" >>"$EVIDENCE_DIR/$RUN_LABEL/transitions/markers.tsv"
  printf '%s\n' "$value"
}

trigger_collection() {
  local node
  for node in "${NODES[@]}"; do
    ssh_node "$node" sudo systemctl restart releem-agent.service >/dev/null
    ssh_node "$node" sudo systemctl is-active --quiet releem-agent.service
  done
}

verify_cluster_state() {
  local label="$1" out="$EVIDENCE_DIR/$RUN_LABEL/${label}.json"
  mkdir -p "$EVIDENCE_DIR/$RUN_LABEL"
  {
    printf '{"label":%s,"captured_at":%s,"clusters":[' "$(jq -Rn --arg x "$label" '$x')" "$(jq -Rn --arg x "$(date -u +%FT%TZ)" '$x')"
    mysqlsh_on_first_available "var c=dba.getCluster('releem_single'); print(JSON.stringify(c.status({extended:1})))" releem-ic-single-1 releem-ic-single-2 releem-ic-single-3
    printf ','
    mysqlsh_on_first_available "var c=dba.getCluster('releem_multi'); print(JSON.stringify(c.status({extended:1})))" releem-ic-multi-1 releem-ic-multi-2 releem-ic-multi-3
    printf ','
    mysqlsh_on releem-ic-cs-primary-1 "var c=dba.getCluster('releem_cs_primary'); print(JSON.stringify(c.getClusterSet().status({extended:1})))"
    printf ']}\n'
  } | sed -n '/^{/,$p' >"$out"
  jq -e . "$out" >/dev/null || die "invalid MySQL Shell state evidence for $label"
}

require_persistence_env() {
  local name
  for name in TOPOLOGY_MYSQL_HOST TOPOLOGY_MYSQL_USER TOPOLOGY_MYSQL_PASSWORD \
    TOPOLOGY_CLICKHOUSE_URL TOPOLOGY_CLICKHOUSE_USER TOPOLOGY_CLICKHOUSE_PASSWORD; do
    [[ -n "${!name:-}" ]] || die "$name must be set for persistence validation"
  done
}

platform_mysql() {
  local query="$1"
  MYSQL_PWD="$TOPOLOGY_MYSQL_PASSWORD" mysql --batch --raw --skip-column-names \
    --host="$TOPOLOGY_MYSQL_HOST" --port="${TOPOLOGY_MYSQL_PORT:-3306}" \
    --user="$TOPOLOGY_MYSQL_USER" "${TOPOLOGY_MYSQL_DATABASE:-releemdb_dev}" --execute "$query"
}

platform_clickhouse() {
  local query="$1"
  printf 'user = "%s:%s"\n' "$TOPOLOGY_CLICKHOUSE_USER" "$TOPOLOGY_CLICKHOUSE_PASSWORD" |
    curl --config - --fail --silent --show-error --data-binary "$query" \
      "${TOPOLOGY_CLICKHOUSE_URL}?database=${TOPOLOGY_CLICKHOUSE_DATABASE:-releemdb_dev}"
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
    ($all | length > 0) and
    ($all | all((.last_seen_epoch_ms | type) == "number" and .last_seen_epoch_ms >= $marker_ms)) and
    (($all | semantic("releem-ic-single-"; "innodb_cluster") | length) == 3) and
    (($all | semantic("releem-ic-single-"; "innodb_cluster") | map(.group_key) | unique | length) == 1) and
    (($all | semantic("releem-ic-multi-"; "innodb_cluster") | length) == 3) and
    (($all | semantic("releem-ic-multi-"; "innodb_cluster") | map(.group_key) | unique | length) == 1) and
    (($all | semantic("releem-ic-cs-"; "innodb_cluster") | length) == 6) and
    (($all | semantic("releem-ic-cs-"; "innodb_cluster") | map(.group_key) | unique | length) == 2) and
    (($all | semantic("releem-ic-cs-"; "innodb_clusterset") | length) == 6) and
    (($all | semantic("releem-ic-cs-"; "innodb_clusterset") | map(.group_key) | unique | length) == 1) and
    (($all | semantic("releem-ic-cs-replica-"; "innodb_clusterset") | all(.parent_group_key != null and .role == "replica_cluster")) or ($mark | contains("primary-switchover"))) and
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
  local mark="${1:-$(marker manual-collect)}" marker_ms evidence_dir deadline now hostnames sids
  marker_ms="$(date -u +%s)000"
  hostnames="$(node_sql_list)"
  evidence_dir="$EVIDENCE_DIR/$RUN_LABEL/persistence/$mark"
  mkdir -p "$evidence_dir"
  trigger_collection

  deadline=$(( $(date +%s) + ${TOPOLOGY_PERSISTENCE_TIMEOUT_SECONDS:-300} ))
  while :; do
    platform_mysql "SELECT COUNT(DISTINCT s.sid) FROM servers s JOIN db_topology_relation_members r ON r.sid=s.sid WHERE s.hostname IN (${hostnames}) AND r.last_seen_epoch_ms >= ${marker_ms};" >"$evidence_dir/mysql-count.txt"
    [[ "$(cat "$evidence_dir/mysql-count.txt")" == "$REQUIRED_NODES" ]] && break
    now="$(date +%s)"; (( now < deadline )) || die "Platform MySQL persistence timeout for marker $mark"
    sleep 10
  done

  platform_mysql "SELECT JSON_OBJECT('sid',s.sid,'hostname',s.hostname,'relation_type',r.relation_type,'group_key',r.group_key,'parent_group_key',r.parent_group_key,'member_key',r.member_key,'primary_member_key',r.primary_member_key,'role',r.role,'is_writer',r.is_writer,'is_reader',r.is_reader,'replication_state',r.replication_state,'last_seen_epoch_ms',r.last_seen_epoch_ms) FROM servers s JOIN db_topology_relation_members r ON r.sid=s.sid WHERE s.hostname IN (${hostnames}) ORDER BY s.sid,r.relation_type,r.group_key;" >"$evidence_dir/mysql-current.jsonl"
  platform_mysql "SELECT JSON_OBJECT('sid',u.sid,'channel_key',u.channel_key,'upstream_member_key',u.upstream_member_key,'replication_state',u.replication_state,'last_seen_epoch_ms',u.last_seen_epoch_ms) FROM db_topology_upstreams u JOIN servers s ON s.sid=u.sid WHERE s.hostname IN (${hostnames}) ORDER BY u.sid,u.channel_key;" >"$evidence_dir/mysql-upstreams.jsonl"
  assert_current_state "$mark" "$marker_ms" "$evidence_dir/mysql-current.jsonl"

  sids="$(platform_mysql "SELECT GROUP_CONCAT(sid ORDER BY sid) FROM servers WHERE hostname IN (${hostnames});")"
  [[ "$sids" =~ ^[0-9]+(,[0-9]+){11}$ ]] || die "expected exactly 12 SIDs"
  while :; do
    platform_clickhouse "SELECT sid,rid,toUnixTimestamp64Milli(timestamp) AS observed_epoch_ms,topology_type,role,group_key,member_key,is_writer,is_reader,replication_state,relations FROM db_topology_observations WHERE sid IN (${sids}) AND toUnixTimestamp64Milli(timestamp) >= ${marker_ms} ORDER BY sid,timestamp FORMAT JSONEachRow" >"$evidence_dir/clickhouse-observations.jsonl"
    if [[ -s "$evidence_dir/clickhouse-observations.jsonl" ]] &&
      jq -e '(.sid|type)=="number" and (.relations|fromjson|type)=="array"' "$evidence_dir/clickhouse-observations.jsonl" >/dev/null &&
      [[ "$(jq -s 'map(.sid)|unique|length' "$evidence_dir/clickhouse-observations.jsonl")" == "$REQUIRED_NODES" ]]; then
      break
    fi
    now="$(date +%s)"; (( now < deadline )) || die "ClickHouse persistence timeout for marker $mark"
    sleep 10
  done
  awk -F'\t' 'NR>1 && $1<p {exit 1} {p=$1}' "$EVIDENCE_DIR/$RUN_LABEL/transitions/markers.tsv" 2>/dev/null ||
    die "transition markers are not monotonic"
  jq -n --arg marker "$mark" --argjson marker_epoch_ms "$marker_ms" --arg sids "$sids" \
    '{marker:$marker,marker_epoch_ms:$marker_epoch_ms,sids:($sids|split(",")|map(tonumber)),mysql_current:"mysql-current.jsonl",mysql_upstreams:"mysql-upstreams.jsonl",clickhouse:"clickhouse-observations.jsonl",result:"PASS"}' \
    >"$evidence_dir/summary.json"
  log "persistence validated for marker $mark"
}

exercise_step() {
  local name="$1"; shift
  local mark
  mark="$(marker "$name")"
  "$@"
  trigger_collection
  sleep "${TOPOLOGY_SETTLE_SECONDS:-20}"
  verify_cluster_state "$name"
  collect "$mark"
}

exercise() {
  validate_run_label
  require_api_key
  require_cluster_password
  require_persistence_env
  load_state
  require_running_inventory

  local single_primary single_rejoin_source
  single_primary="$(mysqlsh_on releem-ic-single-1 'var r=session.runSql("SELECT MEMBER_HOST FROM performance_schema.replication_group_members WHERE MEMBER_ROLE=\"PRIMARY\" AND MEMBER_STATE=\"ONLINE\" LIMIT 1"); var x=r.fetchOne(); if (x) print(x[0]);' | tail -n1)"
  [[ "$single_primary" =~ ^releem-ic-single-[1-3]$ ]] || die "could not identify the single-primary writer"
  single_rejoin_source=releem-ic-single-1
  [[ "$single_rejoin_source" != "$single_primary" ]] || single_rejoin_source=releem-ic-single-2

  exercise_step single-healthy true
  exercise_step single-primary-stopped ssh_node "$single_primary" sudo mysql -e 'STOP GROUP_REPLICATION'
  exercise_step single-secondary-promoted sleep 20
  exercise_step single-old-primary-rejoined mysqlsh_on "$single_rejoin_source" \
    "var c=dba.getCluster('releem_single'); c.rejoinInstance('${MYSQL_ADMIN_USER}@${single_primary}:3306',{recoveryMethod:'incremental'}); print(JSON.stringify(c.status({extended:1})))"

  exercise_step multi-healthy true
  exercise_step multi-writer-offline ssh_node releem-ic-multi-2 sudo mysql -e 'STOP GROUP_REPLICATION'
  exercise_step multi-writer-rejoined mysqlsh_on releem-ic-multi-1 \
    "var c=dba.getCluster('releem_multi'); c.rejoinInstance('${MYSQL_ADMIN_USER}@releem-ic-multi-2:3306',{recoveryMethod:'incremental'}); print(JSON.stringify(c.status({extended:1})))"

  exercise_step clusterset-healthy true
  exercise_step clusterset-replication-stopped ssh_node releem-ic-cs-replica-1 \
    sudo mysql -e "STOP REPLICA FOR CHANNEL 'clusterset_replication'"
  exercise_step clusterset-replication-resumed ssh_node releem-ic-cs-replica-1 \
    sudo mysql -e "START REPLICA FOR CHANNEL 'clusterset_replication'"
  exercise_step clusterset-primary-switchover mysqlsh_on releem-ic-cs-primary-1 \
    "var cs=dba.getCluster('releem_cs_primary').getClusterSet(); cs.setPrimaryCluster('releem_cs_replica'); print(JSON.stringify(cs.status({extended:1})))"
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
  load_state
  preflight
  local records name zone labels firewall="releem-ic-internal-${RUN_LABEL}"
  records="$(gcloud_project compute instances list \
    --filter="labels.${LABEL_KEY}=${RUN_LABEL} AND labels.releem-topology-managed=true" \
    --format="csv[no-heading,separator='|'](name,zone.basename(),labels.${LABEL_KEY},labels.releem-topology-managed)")"
  while IFS='|' read -r name zone owner managed; do
    [[ -n "$name" ]] || continue
    [[ "$owner" == "$RUN_LABEL" && "$managed" == "true" ]] || die "refusing unowned delete: $name"
    gcloud_project compute instances delete "$name" --zone="$zone" --quiet
  done <<<"$records"
  if resource_exists firewall-rules "$firewall"; then
    description="$(gcloud_project compute firewall-rules describe "$firewall" --format='value(description)')"
    [[ "$description" == *"owner=${LABEL_KEY}:${RUN_LABEL}"* ]] || die "refusing unowned firewall delete"
    gcloud_project compute firewall-rules delete "$firewall" --quiet
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
