# Production-readiness and specification review

This repository is a payment-domain simulation, not a production payment processor. The review below records what is enforced, where boundaries sit, and which production capabilities are deliberately absent.

## Specification traceability

| Requirement | Implementation | Verification |
|---|---|---|
| Positive integer minor-unit payment amounts and three-letter currency | `internal/payment/store.go` | payment store and integration tests |
| Immutable, balanced, multi-entry postings | `internal/ledger/store.go` deep-copies entries, computes totals and per-account deltas with entry-order-independent exact arithmetic, and atomically checks currencies/final `int64` deltas/balance overflow | ledger immutability, full-`int64` boundary, validation, and overflow tests |
| Global per-service idempotency keys | payment and ledger store idempotency records include canonical operation/request hashes | concurrent duplicate-key, conflicting-payload, retry, and cross-operation tests |
| Serialized concurrent writes and no partial local mutation | mutex-protected stores; payment mutation follows successful ledger response; returned posting snapshots deep-copy entries | race suite, concurrent retry/snapshot tests, ambiguous commit test, and real-HTTP overflow/recovery test |
| Structured JSON errors | `internal/httpx` plus JSON not-found fallbacks | HTTP contract and panic recovery tests |
| Payment lifecycle and ledger effects | payment transition switch and ledger port | lifecycle unit and real-handler integration tests |
| Operational HTTP behavior | body cap, unknown-field rejection, strict bearer parsing, timeouts, buffered panic recovery, middleware-owned security/request-ID headers, request logs, signal shutdown | HTTP middleware tests and command run/shutdown tests |
| Dependency-free responsive admin | embedded semantic HTML/CSS/JavaScript | static accessibility assertions and browser walkthrough |

## Service and domain boundaries

- `internal/ledger` owns account, posting, balance, and ledger idempotency rules.
- `internal/payment` owns payment state, transition, refund, and payment idempotency rules.
- The payment domain depends only on its small `Ledger` port and payment-owned `LedgerPosting` value. It does not import ledger domain types.
- `LedgerClient` is the outbound HTTP adapter. Its private wire DTOs and JSON tags are the only payment-side knowledge of the ledger API.
- HTTP handlers translate transport concerns and never mutate store internals directly.
- Cross-service tests may import both domains because they are composition tests, not production coupling.

## API contract review

- All write bodies are exactly one top-level JSON object, reject unknown fields and non-object/`null` values, and are capped at 1 MiB. The exact limit is accepted; larger bodies return structured HTTP 413.
- Every write requires an idempotency key capped at 200 bytes.
- Domain strings and caller-supplied account IDs are bounded; currency is canonicalized to uppercase, and posting account IDs are trimmed on a copied request before fingerprinting.
- Invalid transitions are HTTP 409; malformed input is HTTP 400; absent resources are HTTP 404; unavailable or invalid ledger responses are HTTP 502. Outbound success responses are capped at 1 MiB and must contain exactly one JSON value with a non-empty resource ID.
- Lists return deterministic payment/account ordering. Posting order is immutable acceptance order. Pagination tests cover exact limits, terminal omission of `next_cursor`, malformed/unknown/altered cursors, and duplicate-free traversal.
- Bearer parsing permits exactly one RFC-style token68 credential and rejects ambiguous or duplicate Authorization forms. Secret comparison remains constant-time.
- Admin responses are non-cacheable and include framing, referrer, MIME-sniffing, and content-security headers; CSP explicitly blocks object content, framing, external defaults, and base changes. Static UI tests cover labels/live status, per-tab token persistence and reload, escaped dynamic fields, pagination append/reset, failure preservation, and control recovery. Buffered middleware owns `X-Request-ID` and `X-Content-Type-Options`, discards partial panic output, and preserves ordinary handler headers/status/body.
- Unsupported methods on documented resources return JSON `405 method_not_allowed` with an accurate `Allow` header. Unknown routes/resources return structured JSON `404`; contract tests assert content type, error envelope, request ID, and MIME-sniffing headers.
- Cross-service tests prove a caller request ID and the payment service's configured ledger token reach each ledger HTTP request.

The API is intentionally not declared stable before version `v1.0.0`. Error `code` values and JSON field names are the compatibility surface; human-readable messages are not.

## Browser client choice

The admin uses browser-native HTML, CSS, and JavaScript rather than a framework. The UI has one screen, three inputs, a list, and four state-dependent actions. A framework would add a package manager, client build, dependency updates, and a larger supply-chain surface without providing needed routing or complex state management. Native controls and semantic landmarks improve keyboard and screen-reader behavior, while all untrusted values are escaped before HTML insertion. The server remains authoritative for every transition and the UI refreshes after both success and failure.

Inline CSS and JavaScript keep the artifact self-contained. The content-security policy permits only same-origin resources and the two inline blocks; it blocks external defaults, framing, base changes, and cross-origin API calls. If the UI expands, move assets to separately served files and remove `unsafe-inline` rather than adopting a framework by default.

## Deliberate non-production characteristics

- State and idempotency records are in memory and disappear on restart.
- Optional static bearer-token auth exists (off by default); there is no per-principal authentication, authorization, tenant isolation, TLS termination, audit export, database, migration, backup, or disaster recovery.
- Health endpoints are liveness checks; orchestration-level dependency readiness is not modeled.
- List endpoints are paginated (limit/cursor, default 50, max 200), but retained records are unbounded and have no expiry. Cursors are the last item ID, so a cursor is invalid if that item no longer exists.
- Docker and Compose were validated with OrbStack Docker Engine 29.4.0: both service images built, an isolated Compose project reached healthy state, and both container health endpoints responded successfully. Compose passes empty auth tokens by default.
- Request IDs and structured request logs exist, but there are no metrics, tracing, or log shipping.
- The two-service transaction relies on retry with ledger idempotency, not a durable outbox or distributed transaction.
- Account semantics permit negative balances because this simulation models ledger positions, not spend controls.

Do not expose these services to an untrusted network or use them for real funds. Production adoption requires durable storage, authenticated principals, authorization policy, encrypted transport, secret management, observability/SLOs, reconciliation, durable idempotency/outbox processing, pagination/retention, and an external security review.
