# PostgreSQL Query Payload and EXPLAIN Quotas Design

## Goal

Keep PostgreSQL monitoring payloads backward compatible while limiting the new
query-optimization contract to servers where query optimization is enabled.
Collect up to 100 unique successful EXPLAIN plans for each of the total-time and
mean-time rankings.

## Payload Modes

When `query_optimization=false`, the Agent preserves the PostgreSQL query payload
used by `origin/master`. Each query contains:

- `datname`
- `queryid`
- `query`
- `query_text`
- `calls`
- `total_exec_time_us`
- `mean_exec_time_us`

The legacy payload does not contain `rows`, `SUM_ROWS_SENT`, `QueriesFormat`, or
`DatabaseSchemaFormat`. PostgreSQL schema collection remains disabled. All other
DB, system, and Agent metrics keep their existing structure and units.

When `query_optimization=true`, the Agent publishes the versioned PostgreSQL
query-optimization payload. Each query contains:

- `datname`
- `queryid`
- `query`
- `calls`
- `total_exec_time_us`
- `mean_exec_time_us`
- `rows`
- optional `explain` or `explain_error`

The query payload uses `QueriesFormat=postgresql_pgss_v1`. Schema data uses
`DatabaseSchemaFormat=postgresql_schema_v1`. Timing values remain in
microseconds end to end. The Platform parser consumes `_us` fields directly and
does not apply an additional milliseconds-to-microseconds conversion.

For the versioned payload, Platform derives per-call rows as `rows / calls`. A
zero call count produces zero rows per call.

## EXPLAIN Selection

The collector performs two ordered ranking passes:

1. descending `total_exec_time_us`;
2. descending `mean_exec_time_us`.

Each pass has an independent quota of 100 successful EXPLAIN results. Failed,
skipped, or empty EXPLAIN results do not consume the success quota. A pass stops
when it reaches 100 successes or exhausts its candidate list.

The two passes share a deduplication set. Queries attempted during the total-time
pass are not retried during the mean-time pass. Therefore, the second pass
collects up to 100 additional unique successful plans, for a maximum of 200
unique successful plans per collection.

Connection reuse and failed-database suppression remain shared across both
passes. Existing statement eligibility, excluded-database, permission-error,
and schema-resolution behavior remains unchanged.

## Compatibility

An Agent with query optimization disabled remains compatible with the current
Platform monitoring path because its query payload is identical to
`origin/master` and contains no new format markers.

The new query and schema formats are emitted only when query optimization is
enabled. They are consumed by the matching PostgreSQL query-optimization path in
Platform. No unrelated payload section is versioned or renamed.

## Tests

Agent tests verify that:

- disabled query optimization emits the exact legacy query fields and omits
  `rows`, `SUM_ROWS_SENT`, and both format markers;
- enabled query optimization emits `_us` timing fields, `rows`, and the query
  and schema format markers;
- unrelated payload sections remain unchanged between modes;
- failures before successful EXPLAIN results do not consume a success quota;
- the total-time pass can collect 100 successes;
- the mean-time pass can collect 100 additional unique successes;
- attempted queries are not retried across rankings.

Platform tests verify that the versioned PostgreSQL parser requires and consumes
`total_exec_time_us` and `mean_exec_time_us` directly, derives rows per call, and
rejects the old `_ms` keys for the versioned format.
