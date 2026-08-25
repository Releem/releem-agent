# Agent Instructions

## Project Scope

Releem_Agent is the open-source agent installed on customer database servers. It
collects MySQL, MariaDB, and PostgreSQL metrics, sends them to Releem Platform,
and applies recommended configurations on Linux, Windows, cloud, and on-premise
targets.

Primary areas include:
- `main.go`, `metrics/`, `tasks/`, `models/`, `utils/`, and `repeater/` for the Go agent.
- `install.sh` and `mysqlconfigurer.sh` for Linux install/apply flows.
- `windows/install.ps1` and `windows/mysqlconfigurer.ps1` for Windows flows.
- `releem-agent-on-premise/` and `docker/` for distribution-specific packaging.
- `tests/` for Bats and Terraform-backed end-to-end installation tests.

## Working Rules

- Preserve backward-compatible install and apply behavior. Existing Linux and
  Windows command-line flows are production-sensitive.
- For Aurora onboarding, require both a custom DB instance parameter group and
  a custom DB cluster parameter group. Both configured names must match the
  groups attached in AWS, and both groups must be ready before apply, including
  on readers. Only a writer may mutate the cluster group. A missing, default,
  mismatched, or not-ready group is an apply error; do not weaken these checks
  under the non-Aurora RDS compatibility contract.
- Do not edit built binaries or generated packages unless the task explicitly
  targets release artifacts: `releem-agent-*` files and on-premise binary copies.
- Do not edit secrets or local config files unless asked: `.keys/`, `releem.conf`,
  and `.config/*`.
- Keep OS and DB matrix assumptions explicit. Supported paths include MySQL,
  MariaDB, PostgreSQL, Linux, Windows, cloud, Docker, and on-premise variants.
- For shell scripts, prefer POSIX-compatible changes unless the script already
  requires Bash-specific behavior.
- For PowerShell, preserve Windows Server compatibility and avoid Linux-only
  assumptions.
- When installer behavior changes, update or add Bats/e2e coverage near
  `tests/install.bats`, `tests/mysqlconfigurer.bats`, or `tests/README.md`.
- Avoid unrelated formatting churn in generated configs and installer scripts.

## Setup

Go dependencies are managed with modules:

```bash
go mod download
```

End-to-end install tests require Terraform and authenticated `gcloud`; see
`tests/README.md` before running cloud-backed workflows.

## Verification

For Go changes:

```bash
go test ./...
```

For shell syntax checks:

```bash
bash -n install.sh
bash -n mysqlconfigurer.sh
```

For focused Bats tests when available:

```bash
bats tests/install.bats
bats tests/mysqlconfigurer.bats
```

For cloud-backed installation tests:

```bash
cd tests
./run_tests_local.sh --os ubuntu-22.04 --db mysql-8.0 --test 1
```

## Review Checklist

- Does the change preserve existing install/apply flags and output contracts?
- For Aurora, are both custom parameter groups configured, attached, matched,
  and ready before apply, while cluster mutation remains writer-only?
- Are Linux and Windows paths still equivalent where intended?
- Are built binaries, secrets, and local configs untouched?
- Is the relevant OS/DB behavior covered by Go, Bats, or e2e tests?
- Are failure messages actionable for users installing the agent manually?

## Creating a Release

1. Bump the version everywhere: `config/config.go` (`ReleemAgentVersion`),
   `install.sh` (header + `install_script_version`), `mysqlconfigurer.sh`
   (header + `VERSION`), `windows/mysqlconfigurer.ps1` (`$ScriptVersion`),
   related tests (e.g. `tests/mysqlconfigurer.bats`,
   `tests/windows/test_05_update_delegation.ps1`), and
   `current_version_agent`. Do not edit built binaries.
2. Add a new `## X.Y.Z` section to `RELEASE_NOTES.md` with the changes in that
   release.
3. Commit the version bump and release notes.
4. Tag that commit `X.Y.Z` and push the branch and tag to `origin`.
