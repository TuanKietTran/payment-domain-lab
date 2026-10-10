# Changelog

Notable changes are documented here following Keep a Changelog and Semantic Versioning.

## [Unreleased]

### Added

- Ledger service with balanced immutable postings, accounts, balances, and idempotency.
- Payment lifecycle service with authorization, capture, cancel, and partial/full refunds.
- Responsive dependency-free admin UI and structured error inspection.
- Unit, race, HTTP, concurrency, and cross-service integration tests.
- Make, local smoke, Docker Compose, hardened container, and GitHub CI workflows.
- Production-readiness, security, contribution, release, and architecture documentation.
- Bounded `limit`/`cursor` pagination on payment, account, and posting lists.
- Optional constant-time bearer-token auth (`PAYMENT_AUTH_TOKEN`, `LEDGER_AUTH_TOKEN`) with `/healthz` exempt and admin UI token field.
- `X-Request-ID` middleware with request ID propagation to the ledger and structured request log fields.
- Testable `loadConfig`/`run` in both commands, with config and server wiring tests.
- End-to-end coverage proving payment forwards caller request IDs and the configured ledger bearer token.
- Pagination coverage for exact and terminal pages, unknown/altered cursors, and duplicate-free traversal.

### Changed

- Bearer parsing now accepts only one unambiguous RFC-style token68 credential and rejects malformed whitespace, trailing content, comma-joined values, and duplicate Authorization fields while retaining constant-time secret comparison.
- Buffered middleware now enforces its own `X-Request-ID` and `X-Content-Type-Options` values after handler execution, including error and panic responses.
- Ledger zero-sum validation now uses exact arithmetic, allowing mathematically balanced postings across the full `int64` entry range while retaining atomic per-account balance overflow checks.

### Verification

- Added race-tested concurrent idempotency coverage, conflicting-payload checks, payment transition/refund boundary matrices, and ledger balance/currency/account invariant tests.
- Added static admin contract coverage and payment/ledger method, 404, JSON error, security-header, and request-ID tests.
- Admin token application is explicit and persisted per tab; successful reloads clear stale load errors, list reloads reset cursors, load-more requests cannot be double-submitted, failed reloads preserve rendered cards, and disabled action controls recover.
- Resource, action, health, and admin routes now consistently return JSON `405 method_not_allowed` with `Allow` instead of falling through to `404` for unsupported methods.
- Admin CSP now explicitly blocks object content, and all displayed payment fields use the HTML escaping boundary.
- JSON decoding now requires one top-level object, rejects `null` and non-object values, accepts an object exactly at the 1 MiB boundary, and reports larger bodies as `413 request_too_large`.
- Added repeated resource-boundary, timeout, shutdown, and concurrent-idempotency stress coverage; timeout tests now explicitly release or drain their HTTP resources so cleanup cannot deadlock.
- Ledger posting aggregation now uses exact arithmetic per account, removing entry-order-dependent false overflow failures while still rejecting final deltas outside `int64` atomically.
- Ledger posting account IDs are trimmed on a copied request before validation and idempotency fingerprinting, so canonical retries do not mutate caller input or conflict solely on surrounding whitespace.
- The payment-to-ledger client now bounds and fully consumes one JSON response value, rejects trailing JSON and missing resource IDs, and reports oversized or malformed success responses as `ledger_invalid_response`.
- Added real-HTTP overflow/retry coverage proving failed ledger balance updates leave payment state retryable, plus concurrent snapshot tests proving returned posting entries cannot alias stored state.
