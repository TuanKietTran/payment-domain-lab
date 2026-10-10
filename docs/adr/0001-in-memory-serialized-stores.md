# ADR-0001: In-memory serialized domain stores

- Status: Accepted
- Date: 2026-10-09

## Context

The specification defines a simulation and explicitly requires serialized concurrent writes and atomic in-memory mutation. It also prioritizes money, balancing, state-transition, and idempotency behavior over persistence infrastructure.

## Decision

Each service owns a mutex-protected in-memory store. A write validates completely before mutation. Ledger postings use exact arithmetic for the whole-posting zero-sum check, then aggregate and overflow-check each `int64` account delta and resulting balance before applying any balance. This accepts balanced boundary combinations whose intermediate sum would overflow `int64` without weakening account balance bounds. Payment actions hold the payment store lock through the ledger call and mutate payment state only after success. Successful idempotency records are retained for the process lifetime; failed operations are not recorded.

Returned postings are deep copies so callers cannot mutate accepted ledger history.

## Consequences

The model is deterministic and straightforward to race-test, but throughput is intentionally serialized and a slow ledger call blocks payment reads/writes. State, idempotency history, and audit history disappear on restart; memory usage grows without retention. Production adoption requires transactional durable storage, retention, pagination, and likely per-aggregate concurrency control while preserving these invariants.
