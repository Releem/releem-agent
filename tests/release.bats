#!/usr/bin/env bats

setup() {
    local repo_root
    repo_root="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
    export LC_ALL=C
    TEST_TMPDIR="$(mktemp -d)"
    mkdir -p "$TEST_TMPDIR/source" "$TEST_TMPDIR/v2" "$TEST_TMPDIR/bin"
    awk '
        /    - name: Upload S3 current_version_agent/ { found=1; next }
        found && /^      run: \|/ { script=1; next }
        script && /^    - name:/ { exit }
        script { sub(/^        /, ""); print }
    ' "$repo_root/.github/workflows/go.yml" > "$TEST_TMPDIR/publish.sh"
    printf '1.26.2\n' > "$TEST_TMPDIR/source/current_version_agent"
    cp "$TEST_TMPDIR/source/current_version_agent" "$TEST_TMPDIR/v2/current_version_agent"
    cp "$repo_root/install.sh" "$TEST_TMPDIR/v2/install.sh"
    local name
    for name in releem.sh mysqlconfigurer.sh install.ps1 releem.ps1 mysqlconfigurer.ps1 \
        releem-agent-x86_64 releem-agent-amd64 releem-agent-aarch64 releem-agent-freebsd-amd64 releem-agent-i686 releem-agent.exe; do
        echo new-payload > "$TEST_TMPDIR/v2/$name"
    done
    cat > "$TEST_TMPDIR/bin/aws" <<'EOF'
#!/bin/bash
[ "$1 $2" = 's3 cp' ] || exit 91
[ "$5 $6 $7" = '--acl public-read --content-type' ] || exit 92
case "$3" in
    *.sh) expected_type=application/x-sh ;;
    *.exe) expected_type=application/x-msdownload ;;
    *) expected_type=text/plain ;;
esac
[ "$8" = "$expected_type" ] || exit 93
printf '%s\n' "$4" >> "$UPLOAD_LOG"
if [ -n "$FAIL_UPLOAD" ] && [ "$4" = "$FAIL_UPLOAD" ]; then exit 23; fi
key="${4#s3://test-release/}"
mkdir -p "$PUBLIC_STORE/$(dirname "$key")"
cp "$3" "$PUBLIC_STORE/$key"
EOF
    chmod +x "$TEST_TMPDIR/bin/aws"
    export UPLOAD_LOG="$TEST_TMPDIR/uploads" PUBLIC_STORE="$TEST_TMPDIR/public"
    reset_publication
}

reset_publication() {
    rm -rf "$PUBLIC_STORE"
    mkdir -p "$PUBLIC_STORE/v2"
    printf '1.26.2\n' > "$PUBLIC_STORE/v2/current_version_agent"
    local name
    for name in "$TEST_TMPDIR/v2/"*; do
        [ "${name##*/}" != current_version_agent ] || continue
        echo legacy-installer > "$PUBLIC_STORE/v2/${name##*/}"
    done
    : > "$UPLOAD_LOG"
}

teardown() { rm -rf "$TEST_TMPDIR"; }

@test "publication uploads root v2 artifacts in the existing glob order" {
    cd "$TEST_TMPDIR/source"
    run env PATH="$TEST_TMPDIR/bin:$PATH" AWS_BUCKET=test-release bash -e "$TEST_TMPDIR/publish.sh"
    [ "$status" -eq 0 ]
    [ "$(cat "$PUBLIC_STORE/v2/current_version_agent")" = 1.26.2 ]
    [ ! -d "$PUBLIC_STORE/v2/releases" ]
    local name
    for name in "$TEST_TMPDIR/v2/"*; do
        printf 's3://test-release/v2/%s\n' "${name##*/}"
    done > "$TEST_TMPDIR/expected-uploads"
    cmp "$TEST_TMPDIR/expected-uploads" "$UPLOAD_LOG"
    [ "$(head -n 1 "$UPLOAD_LOG")" = s3://test-release/v2/current_version_agent ]
    local count=0
    for name in "$TEST_TMPDIR/v2/"*; do
        cmp "$name" "$PUBLIC_STORE/v2/${name##*/}"
        [ "$(grep -Fxc "s3://test-release/v2/${name##*/}" "$UPLOAD_LOG")" -eq 1 ]
        count=$((count + 1))
    done
    [ "$(wc -l < "$UPLOAD_LOG")" -eq "$count" ]
}

@test "publication stops at each failed upload and preserves failed and remaining artifacts" {
    cd "$TEST_TMPDIR/source"
    # Obtain the full upload sequence, then fail each individual boundary.
    run env PATH="$TEST_TMPDIR/bin:$PATH" AWS_BUCKET=test-release bash -e "$TEST_TMPDIR/publish.sh"
    [ "$status" -eq 0 ]
    cp "$UPLOAD_LOG" "$TEST_TMPDIR/sequence"
    local destination attempted name expected_count=0 failure_seen
    while IFS= read -r destination; do
        reset_publication
        expected_count=$((expected_count + 1))
        run env PATH="$TEST_TMPDIR/bin:$PATH" AWS_BUCKET=test-release FAIL_UPLOAD="$destination" \
            bash -e "$TEST_TMPDIR/publish.sh"
        [ "$status" -eq 23 ]
        [ "$(wc -l < "$UPLOAD_LOG")" -eq "$expected_count" ]
        [ "$(tail -n 1 "$UPLOAD_LOG")" = "$destination" ]
        head -n "$expected_count" "$TEST_TMPDIR/sequence" > "$TEST_TMPDIR/expected-attempts"
        cmp "$TEST_TMPDIR/expected-attempts" "$UPLOAD_LOG"
        [ ! -d "$PUBLIC_STORE/v2/releases" ]
        failure_seen=0
        while IFS= read -r attempted; do
            name="${attempted##*/}"
            [ "$attempted" != "$destination" ] || failure_seen=1
            if [ "$failure_seen" -eq 0 ]; then
                cmp "$TEST_TMPDIR/v2/$name" "$PUBLIC_STORE/v2/$name"
            elif [ "$name" = current_version_agent ]; then
                [ "$(cat "$PUBLIC_STORE/v2/$name")" = 1.26.2 ]
            else
                [ "$(cat "$PUBLIC_STORE/v2/$name")" = legacy-installer ]
            fi
        done < "$TEST_TMPDIR/sequence"
    done < "$TEST_TMPDIR/sequence"
}
