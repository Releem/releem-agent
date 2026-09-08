#!/usr/bin/env bats

setup() {
    TEST_TMPDIR="$(mktemp -d)"
    SCRIPT="${BATS_TEST_DIRNAME}/gcp_innodb_cluster.sh"
    source "$SCRIPT" help >/dev/null
}

teardown() {
    rm -rf "$TEST_TMPDIR"
}

@test "ClickHouse assertion accepts normalized persisted relations" {
    current="$TEST_TMPDIR/current.jsonl"
    observations="$TEST_TMPDIR/observations.jsonl"
    cat >"$current" <<'EOF'
{"sid":42,"last_rid":"rid-42","relation_type":"innodb_cluster","group_key":"cluster-1","parent_group_key":null,"member_key":"member-1","role":"primary","is_writer":1,"replication_state":"healthy","last_seen_epoch_ms":1710000003123}
EOF
    cat >"$observations" <<'EOF'
{"sid":42,"rid":"rid-42","observed_epoch_ms":1710000003000,"relations":"[{\"relation_type\":\"innodb_cluster\",\"group_key\":\"cluster-1\",\"parent_group_key\":null,\"member_key\":\"member-1\",\"role\":\"primary\",\"is_writer\":1,\"replication_state\":\"healthy\"}]"}
EOF

    run assert_selected_observations \
        "$observations" 1 "42:rid-42" 1710000003123 "$current"

    [ "$status" -eq 0 ]
}

@test "stopped single-primary may not know the newly elected primary" {
    current="$TEST_TMPDIR/current.jsonl"
    upstreams="$TEST_TMPDIR/upstreams.jsonl"
    expectation="$TEST_TMPDIR/expectation.json"
    cat >"$current" <<'EOF'
{"hostname":"releem-ic-single-1","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"old-primary","primary_member_key":null,"role":"secondary","is_writer":0,"replication_state":"error"}
{"hostname":"releem-ic-single-2","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"new-primary","primary_member_key":"new-primary","role":"primary","is_writer":1,"replication_state":"healthy"}
{"hostname":"releem-ic-single-3","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"secondary","primary_member_key":"new-primary","role":"secondary","is_writer":0,"replication_state":"healthy"}
EOF
    : >"$upstreams"
    cat >"$expectation" <<'EOF'
{"transition":"single-primary-stopped","target_hostname":"releem-ic-single-1","old_primary_member_key":"old-primary","new_primary_member_key":"new-primary"}
EOF

    run assert_transition_state "$current" "$upstreams" "$expectation"

    [ "$status" -eq 0 ]
}

@test "promoted secondary does not require the stopped primary to see it" {
    current="$TEST_TMPDIR/current.jsonl"
    upstreams="$TEST_TMPDIR/upstreams.jsonl"
    expectation="$TEST_TMPDIR/expectation.json"
    cat >"$current" <<'EOF'
{"hostname":"releem-ic-single-1","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"old-primary","primary_member_key":null,"role":"secondary","is_writer":0,"replication_state":"error"}
{"hostname":"releem-ic-single-2","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"new-primary","primary_member_key":"new-primary","role":"primary","is_writer":1,"replication_state":"healthy"}
{"hostname":"releem-ic-single-3","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"secondary","primary_member_key":"new-primary","role":"secondary","is_writer":0,"replication_state":"healthy"}
EOF
    : >"$upstreams"
    cat >"$expectation" <<'EOF'
{"transition":"single-secondary-promoted","target_hostname":"releem-ic-single-2","old_primary_member_key":"old-primary","new_primary_member_key":"new-primary"}
EOF

    run assert_transition_state "$current" "$upstreams" "$expectation"

    [ "$status" -eq 0 ]
}

@test "rejoined primary assertion ignores parallel group replication relation" {
    current="$TEST_TMPDIR/current.jsonl"
    upstreams="$TEST_TMPDIR/upstreams.jsonl"
    expectation="$TEST_TMPDIR/expectation.json"
    cat >"$current" <<'EOF'
{"hostname":"releem-ic-single-1","relation_type":"group_replication","group_key":"cluster-1","member_key":"new-primary","primary_member_key":"new-primary","role":"primary","is_writer":1,"replication_state":"healthy"}
{"hostname":"releem-ic-single-1","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"new-primary","primary_member_key":"new-primary","role":"primary","is_writer":1,"replication_state":"healthy"}
{"hostname":"releem-ic-single-2","relation_type":"group_replication","group_key":"cluster-1","member_key":"old-primary","primary_member_key":"new-primary","role":"secondary","is_writer":0,"replication_state":"healthy"}
{"hostname":"releem-ic-single-2","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"old-primary","primary_member_key":"new-primary","role":"secondary","is_writer":0,"replication_state":"healthy"}
{"hostname":"releem-ic-single-3","relation_type":"group_replication","group_key":"cluster-1","member_key":"secondary","primary_member_key":"new-primary","role":"secondary","is_writer":0,"replication_state":"healthy"}
{"hostname":"releem-ic-single-3","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"secondary","primary_member_key":"new-primary","role":"secondary","is_writer":0,"replication_state":"healthy"}
EOF
    : >"$upstreams"
    cat >"$expectation" <<'EOF'
{"transition":"single-old-primary-rejoined","target_hostname":"releem-ic-single-2","target_member_key":"old-primary","new_primary_member_key":"new-primary"}
EOF

    run assert_transition_state "$current" "$upstreams" "$expectation"

    [ "$status" -eq 0 ]
}

@test "multi-primary degraded assertion accepts missing member as secondary" {
    status='{"clusterName":"releem_multi","defaultReplicaSet":{"topologyMode":"Multi-Primary","topology":{"releem-ic-multi-1:3306":{"status":"ONLINE","mode":"R/W","memberRole":"PRIMARY"},"releem-ic-multi-2:3306":{"status":"(MISSING)","mode":"n/a","memberRole":"SECONDARY"},"releem-ic-multi-3:3306":{"status":"ONLINE","mode":"R/W","memberRole":"PRIMARY"}}}}'

    run assert_cluster_state_for_label "$status" releem_multi multi \
        multi-writer-offline \
        releem-ic-multi-1 releem-ic-multi-2 releem-ic-multi-3

    [ "$status" -eq 0 ]
}

@test "multi-primary degraded assertion rejects writable missing member" {
    status='{"clusterName":"releem_multi","defaultReplicaSet":{"topologyMode":"Multi-Primary","topology":{"releem-ic-multi-1:3306":{"status":"ONLINE","mode":"R/W","memberRole":"PRIMARY"},"releem-ic-multi-2:3306":{"status":"(MISSING)","mode":"R/W","memberRole":"SECONDARY"},"releem-ic-multi-3:3306":{"status":"ONLINE","mode":"R/W","memberRole":"PRIMARY"}}}}'

    run assert_cluster_state_for_label "$status" releem_multi multi \
        multi-writer-offline \
        releem-ic-multi-1 releem-ic-multi-2 releem-ic-multi-3

    [ "$status" -ne 0 ]
}

@test "multi offline assertion ignores parallel group replication relation" {
    current="$TEST_TMPDIR/current.jsonl"
    upstreams="$TEST_TMPDIR/upstreams.jsonl"
    expectation="$TEST_TMPDIR/expectation.json"
    cat >"$current" <<'EOF'
{"hostname":"releem-ic-multi-2","relation_type":"group_replication","group_key":"cluster-1","member_key":"member-2","is_writer":0,"replication_state":"error"}
{"hostname":"releem-ic-multi-2","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"member-2","is_writer":0,"replication_state":"error"}
EOF
    : >"$upstreams"
    cat >"$expectation" <<'EOF'
{"transition":"multi-writer-offline","target_hostname":"releem-ic-multi-2","target_member_key":"member-2"}
EOF

    run assert_transition_state "$current" "$upstreams" "$expectation"

    [ "$status" -eq 0 ]
}

@test "multi rejoin assertion ignores parallel group replication relation" {
    current="$TEST_TMPDIR/current.jsonl"
    upstreams="$TEST_TMPDIR/upstreams.jsonl"
    expectation="$TEST_TMPDIR/expectation.json"
    cat >"$current" <<'EOF'
{"hostname":"releem-ic-multi-2","relation_type":"group_replication","group_key":"cluster-1","member_key":"member-2","is_writer":1,"replication_state":"healthy"}
{"hostname":"releem-ic-multi-2","relation_type":"innodb_cluster","group_key":"cluster-1","member_key":"member-2","is_writer":1,"replication_state":"healthy"}
EOF
    : >"$upstreams"
    cat >"$expectation" <<'EOF'
{"transition":"multi-writer-rejoined","target_hostname":"releem-ic-multi-2","target_member_key":"member-2"}
EOF

    run assert_transition_state "$current" "$upstreams" "$expectation"

    [ "$status" -eq 0 ]
}
