# Payment Simulation — Prioritized Specification

## P0 — Money and consistency invariants

1. Money is fake, represented only as positive `int64` minor units plus a three-letter currency code. Floating point is forbidden.
2. A ledger posting is immutable and has at least two entries. Every entry is a non-zero signed amount; the sum by currency is exactly zero.
3. Account currency must match every posted entry. Account balances are derived atomically from accepted postings.
4. A non-empty idempotency key identifies one logical write. Reusing it with the same canonical request returns the original result; reusing it with different input returns `idempotency_conflict`.
5. Concurrent writes are serialized in each in-memory service. Failed validation or downstream calls do not partially mutate state.
6. All failures use JSON `{ "error": { "code", "message" } }` and an appropriate HTTP status.

## P1 — Ledger service

1. Create and list accounts; account IDs are unique and opening balances are zero.
2. Create and list balanced immutable postings and retrieve balances.
3. Expose `GET /healthz`, reject unknown fields, bound request bodies, recover panics, log requests, and shut down gracefully.

## P1 — Payment service

1. Create a payment in `created`; transitions are: `created -> authorized -> captured`, `created|authorized -> canceled`, and captured payments may be partially or fully refunded until refunded total equals amount.
2. Authorization posts customer payable -> merchant pending. Capture posts merchant pending -> merchant settled. Cancel of an authorization reverses authorization. Refund posts merchant settled -> customer payable.
3. Each action validates state, currency, amount, and idempotency before calling the ledger. Ledger failure leaves payment unchanged.
4. Expose list/get/create/action JSON APIs, `GET /healthz`, operational middleware, and graceful shutdown.

## P1 — Browser admin

1. Payment service serves a dependency-free responsive admin page at `/`.
2. The page lists current payments, creates payments, and offers only applicable authorize/capture/cancel/refund actions.
3. Success and structured failure results remain visibly inspectable.

## P2 — Verification and operations

1. Unit-test ledger invariants, idempotency, and payment transitions.
2. Cross-service tests run real HTTP handlers and verify posting/balance effects and invalid transitions.
3. Environment-driven addresses/timeouts, Makefile, README, and curl samples are included.
4. `gofmt`, `go test ./...`, and builds for both commands must pass; a browser walkthrough must cover create, authorize, capture, refund, and an invalid transition.

## Work order

1. Define shared HTTP/error primitives and ledger domain/API.
2. Define payment domain, ledger client/API, and admin UI.
3. Add tests from unit scope outward to cross-service HTTP integration.
4. Add operational files, format, test, build, then perform the browser walkthrough and fix defects.
