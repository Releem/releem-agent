# Shared ExplainCollectionState Design

## Goal

Share EXPLAIN connection/attempt/failure state between MySQL and PostgreSQL collectors.

## Approach

`utils.ExplainCollectionState` owns:

- current `*sql.DB` + database name (reuse / switch / close)
- `TryBeginAttempt` dedupe
- `failedDatabases` map with reason text
- injectable `Connect` defaulting to `ConnectionDatabaseErr`
- `ConnectionFailedExplainError(reason)` → `connection_failed: …`

PostgreSQL wraps it as `pgExplainCollectionState` only to inject `executeExplain`.
MySQL `CollectExplain` takes `*u.ExplainCollectionState` and shares one instance across sum/avg rankings (same as PG total/mean).

## MySQL behavior aligned with PostgreSQL

- Failed schema → `explain_error = connection_failed: <err>`; no reconnect spam
- Skip when `explain` or `explain_error` already set
- Success quota `>= 100` per ranking
