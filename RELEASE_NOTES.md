# Release Notes

## 1.20.0
- Initial tag in selected range
- Optimized database list collection

## 1.21.0
- Added collect processlist
- Enabled logging for Windows
- Added Security section to Readme
- New version release 1.19.9

## 1.21.1
- Users security checks (#405)

## 1.21.2
- Updated algorithm for blank password detection

## 1.21.3
- Fixed error when plugin `validate_password` is activated
- Added 1.20 release documentation updates

## 1.21.3.1
- Added the ability to select the region for storing server data

## 1.21.3.2
- Hotfix: converted variable types
- Fixed error converting `driver.Value` `[]uint8` to int (value out of range)

## 1.21.3.3
- Fixed algorithm for generating `Password` column for security checks
- Fixed calculation of query example count

## 1.21.3.4
- Fixed bug when there are no privileges for the table

## 1.21.4
- V1.21.4 (#416)

## 1.21.4.1
- Corrected installation process text

## 1.21.4.2
- Updated project Go version

## 1.21.5
- Improved security: user information is not sent to platform
- User checks are implemented on the agent side

## 1.21.6
- Optimized latency collection (#434)
- Releem 1.21.0 changelog updates

## 1.21.6.1
- Added query length limitation to processlist

## 1.22.0
- Optimized query examples collection query
- Fixed `need_grant_permission` check
- Added release notes to changelog
- Added all Releem Agent parameters

## 1.22.0.1
- Added SSL support for Docker and RDS
- Added tracking updates

## 1.22.0.2
- Improved agent installation (#455), including manual RDS-on-EC2 option
- Optimized process list collection (#456)

## 1.22.0.3
- Added GCP Cloud SQL feature (#458)

## 1.22.0.4
- Added initial config apply flow (discussion #465)

## 1.22.0.5
- Fixed TLS usage
- Added release 1.22.0 to changelog

## 1.22.1
- Fixed collection for all text queries
- Updated `CHANGELOG.md` for 1.22.0.4
- Added blank line before stages declaration

## 1.22.2
- Added `information_schema.key_column_usage` collection for foreign key checks
- Added `performance_schema_digests_size` variable setup in installer

## 1.22.2.1
- Fixed initial configuration apply flow

## 1.22.2.2
- Fixed Windows crash: `invalid memory address or nil pointer dereference`

## 1.22.3
- Added collection of additional tables for automatic schema-change apply
- Fixed error handler

## 1.22.3.1
- Fixed permission-check condition

## 1.22.3.2
- Enabled query interpolation
- Added release 1.22.1 to changelog

## 1.22.4
- Fixed `releem.conf` template
- Updated Go and dependency versions
- Removed `envsubst` dependency

## 1.22.5
- Changed query filter field to avoid full table scan

## 1.22.6
- Optimized `events_statements_*` consumer enablement
- Optimized statement samples collection

## 1.22.7
- Fixed table count collection
- Optimized `performance_schema.table_io_waits_summary_by_index_usage` collection

## 1.23.0
- Refactored code
- Added PostgreSQL support
- Optimized custom query flow (#498)

## 1.23.1
- Fixed debug message
- Fixed enabling query optimization

## 1.23.2
- Optimized index usage statistics collection
- Updated error texts

## 1.23.3
- Fixed filtering field
- Added `Last_Seen` check for EXPLAIN collection
- Added skipping empty partition (issue #495)

## 1.23.3.1
- Refactored bash scripts and added bash unit tests (#505)
- Updated changelog for 1.22.7 and related fixes/features (#501)

## 1.23.4
- Updated package versions
- Updated Go and dependencies
- PostgreSQL query optimization (#507)
- Fixed error when `metrics.DB.Conf.Variables` is nil
- Fixed timer for starting data collection

## 1.23.4.1
- Added serverless database capacity metric
- Bumped agent version to 1.23.4.1
- Added support for Aurora Serverless v2 Enhanced Monitoring metrics (#508)

## 1.23.5
- Bumped Go toolchain and module dependencies
- Enabled `mysql_ssl_mode=true` by default for Azure MySQL installs
- Fixed debug message
- Switched config requests to queries API domain
- Split apply flow by platform and added Azure MySQL support
- Added fail-fast behavior on startup errors
- Included related changelog and test updates

## 1.23.5.1
- Switched agent error, installer, and configurer event logs to the queries API domain
- Simplified queries API base URL construction

## 1.23.5.2
- Fixed PostgreSQL processlist collection for nullable connection and wait event fields
- Added lowercase `eu` support for selecting the EU Releem region

## 1.23.5.3
- Released Windows installer and MySQL configurer scripts
- Added Windows task execution support for task types 0, 1, 3, 4, and 5
- Fixed Windows one-shot agent commands so `-f`, `-c`, `--initial`, `--event`, and `--task` run outside the service wrapper
- Refreshed recommended configuration before Windows local apply
- Added GCP-based Linux and Windows end-to-end installation test infrastructure

## 1.23.6
- Added `pg_stat_statements` validation for PostgreSQL credential setup
- Updated Go toolchain and module dependencies
- Bumped agent, installer, configurer, and Windows script versions to 1.23.6

## 1.23.6.1
- Updated Go toolchain and module dependencies, including `golang.org/x/crypto` to v0.52.0
- Bumped agent, installer, configurer, and Windows script versions to 1.23.6.1

## 1.23.7
- Updated Go toolchain to 1.26.4
- Updated Go module dependencies, including Azure, AWS, Google API, OpenTelemetry, and `golang.org/x/*` packages
- Bumped agent, installer, configurer, and Windows script versions to 1.23.7

## 1.24.0
- Added the first iteration of the automatic task executor
- Added installer support for custom MySQL and PostgreSQL root login overrides
- Added explicit installer flags for selecting the database type
- Fixed root password prompt handling in the installer
- Fixed Windows installer exit handling and added custom root login tests
- Added startup delay to ensure the server is registered before follow-up operations
- Bumped agent, installer, configurer, and Windows script versions to 1.24.0

## 1.25.0
- Added PostgreSQL query optimization collection with `pg_stat_statements` capability detection and support for PostgreSQL 12 and newer
- Added PostgreSQL EXPLAIN collection with query deduplication, collection quotas, prepared-statement parameter handling, and `search_path` support
- Added PostgreSQL schema collection for tables, partitions, columns, indexes, constraints, foreign keys, sequences, relation sizes, and planner statistics
- Added PostgreSQL metadata for expression, partial, covering, and partitioned indexes
- Improved query and schema collection resilience when individual databases, schema sections, or EXPLAIN operations fail
- Aligned MySQL and PostgreSQL schema publishing, failure reporting, and EXPLAIN collection behavior
- Added PostgreSQL query optimization grants and hardened installer quoting and credential escaping
- Fixed PostgreSQL process and query collection compatibility across server versions
- Fixed Windows installer system utility resolution
- Expanded automated coverage for PostgreSQL query optimization, schema collection, installer behavior, and cross-database compatibility

## 1.25.1
- Cache MySQL/MariaDB table size metrics on large servers to reduce `information_schema` load during collection (closes [#496](https://github.com/Releem/releem-agent/issues/496), #528)
- Configurable thresholds/TTL: `table_size_cache_table_threshold`, `table_size_cache_ram_multiplier`, `table_size_cache_ttl_seconds`
- Stale snapshot fallback on refresh failure; AWS RDS physical RAM KiB→bytes for eligibility
- Bumped agent, installer, configurer, and Windows script versions to 1.25.1

## 1.25.2
- Added Aurora MySQL and Aurora PostgreSQL topology discovery, including writer/reader roles and attached instance and cluster parameter groups
- Added scope-aware application of Aurora recommendations to instance and cluster parameter groups, with readiness validation, writer-only cluster changes, result verification, and structured audit details
- Added Aurora configuration support to the Linux installer, Docker template, and public and private CloudFormation templates; Aurora onboarding requires matching custom instance and cluster parameter groups
- Added conversion of PostgreSQL recommendations to AWS parameter units and preserved pending-reboot apply behavior
- Improved managed PostgreSQL collection by skipping only provider-owned maintenance databases and suppressing only expected managed-provider `pg_hba_file_rules` limitations, while preserving actionable errors for local installations
- Expanded Go, installer, Docker, CloudFormation, and AWS payload contract coverage
- Bumped agent, installer, configurer, and Windows script versions to 1.25.2

## 1.25.3
- Updated the build toolchain to Go 1.27.1 in `go.mod` and the GitHub Actions workflow
- Upgraded all Go dependencies to their latest releases, including the AWS SDK for Go v2, Google API client, OpenTelemetry, gRPC, protobuf, and the `golang.org/x` modules
- Bumped agent, installer, configurer, and Windows script versions to 1.25.3


## 1.25.4
- Fixed dynamic(without restart) and full(with restart) configuration apply modes
- Bumped agent, installer, configurer, and Windows script versions to 1.25.4

## 1.25.4.1
- Fixed agent termination on CloudWatch Logs errors while collecting AWS RDS Enhanced Monitoring metrics (#534). Errors are now logged without exiting; the affected collection cycle is skipped and collection resumes on the next scheduled cycle.
- Added regression coverage for CloudWatch HTTP 500/503 errors and recovery on the next collection cycle.
- Bumped agent, installer, configurer, and Windows script versions to 1.25.4.1.

## 1.26.0

- Updated the Go toolchain from 1.27.1 to 1.27.2 in `go.mod` and the GitHub Actions build workflow.
- Updated Go dependencies, including the AWS SDK for Go v2, Azure SDK core and authentication libraries, Google Cloud Monitoring and API clients, OpenTelemetry, gRPC, `gopsutil`, and the `golang.org/x` modules.
- Renamed the management scripts to `releem.sh` on Linux and `releem.ps1` on Windows. Installers and updates download both the new scripts and the legacy `mysqlconfigurer` wrappers, preserving existing commands and cron schedules.
- Unified table-size caching for MySQL, MariaDB, and PostgreSQL. Minute reports reuse the latest complete snapshot; full reports refresh it after 12 hours on ordinary servers, while large servers retain their configurable TTL and thresholds. Failed refreshes preserve the last complete snapshot.
- Reduced PostgreSQL table-statistics collection overhead by reading catalog statistics directly while preserving the fields required by query optimization and schema analysis.
- Added a shared PostgreSQL database connection pool for metrics, EXPLAIN, and schema collection, with an LRU eviction policy and configurable `pg_max_db_connections` limit (default: 10).
- Reused one MySQL connection during each EXPLAIN collection pass, avoiding repeated schema selection and recovering the session after connection failures.
- Included dynamic-apply parameter changes, failures, and summaries in task output. GCP operation failures now return task errors without terminating the agent.
- Fixed Windows apply validation for persisted configuration paths and aligned missing-directory and missing-include exit codes with Linux.
- Expanded regression coverage for connection lifecycle, table-size cache expiry, PostgreSQL statistics, and Linux and Windows script compatibility.
- Bumped agent, installer, management script, and related test versions to 1.26.0.

## 1.26.1

- Added support for applying a selected configuration by `configuration_status_id` and rolling back to a selected configuration through managed rollback tasks.
- Moved configuration downloads for local apply tasks into the Go agent. Linux and Windows management scripts apply the prepared file; initial setup downloads fresh initial configuration before applying it.
- Improved task validation and ensured tasks report their final status even when metric collection fails after execution.
- Preserved configuration request errors and timeout causes across local and cloud apply paths. Repeater panics now return errors instead of being treated as successful empty responses. Empty configuration responses no longer overwrite the existing staging file.
- Made one-shot configuration collection wait for completion and report failures. Docker startup and completed Linux updates tolerate a failed initial or post-update configuration request.
- Restored query sample collection before one-shot query optimization reports.
- Validated complete installer artifact sets before replacement and added Windows recovery for failed file replacement, including restoration of the previous service state.
- Aligned Linux and Windows apply backups: each attempt replaces the backup with the current live file; successful apply, including copying without restart, removes it, while failures retain it. Successful rollback also removes the backup. Manual retries with a retained backup restart even when staged and live files match.
- Preserved PowerShell 3.0 compatibility for configuration file comparisons and forced restarts for full apply and managed rollback tasks on Windows.
- Exposed the PostgreSQL database connection pool limit through Docker configuration and expanded Go, Bats, and offline PowerShell regression coverage.
- Aligned PostgreSQL query payloads with lightweight and full collection profiles: lightweight reports contain query identifiers, calls, and execution timings; full reports additionally include query text, row counts, and available EXPLAIN results.
- Added Windows installer detection for MySQL90, MySQL97, and MySQL267 service names.
- Preserved CLI usage-error exit code 2 and propagated one-shot collection failures to the process exit status.
- Updated GitHub Actions dependencies and replaced the S3 upload action with AWS CLI publication that fails the workflow on upload errors. CI now runs release-publication, Docker, and offline PowerShell regression checks.
- Added PostgreSQL EXPLAIN permission-hint regression tests, including SELECT row-locking and substring expressions.
- Bumped agent, installer, management script, and related test versions to 1.26.1.

## 1.26.2

- Fixed PostgreSQL query statistics mixing standalone queries with executions inside functions or EXPLAIN ANALYZE when execution-level information is available.
- Reduced duplicate PostgreSQL EXPLAIN operations by sharing plans and errors for matching statements across execution levels.
- Added the original PostgreSQL query ID (`pg_queryid`) and execution level (`toplevel`) to query metrics. Older `pg_stat_statements` versions without execution-level information report `toplevel: null` and retain combined statistics.
- Updated the agent, installers, and management scripts to 1.26.2.

**Upgrade note:** PostgreSQL queries use new Releem IDs in this release. After upgrading, query history is recorded under the new IDs; existing history remains under the previous IDs. Statistics from different database users remain combined.
