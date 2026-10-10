# ADR-0005: Pagination, optional bearer auth, and request IDs

## Status

Accepted. Extends [ADR-0001](0001-in-memory-serialized-stores.md) and [ADR-0003](0003-browser-native-admin.md); neither is superseded.

## Context

List endpoints returned every retained record, there was no access control, and requests could not be correlated across the payment and ledger services.

## Decision

- List endpoints (`/v1/payments`, `/v1/accounts`, `/v1/postings`) accept `limit` (default 50, max 200) and an opaque `cursor` that encodes the last returned item ID. `next_cursor` appears only when more items remain. The first page keeps the original response keys, so existing clients keep working.
- Static bearer-token auth is enabled by `PAYMENT_AUTH_TOKEN` and `LEDGER_AUTH_TOKEN` and is off when they are empty. The parser requires exactly one Authorization field containing a case-insensitive `Bearer` scheme, one or more spaces, and one token68 credential; ambiguous whitespace, trailing content, comma-joined credentials, and duplicate fields are rejected. Tokens are SHA-256 hashed before a constant-time comparison so length does not leak, and the comparison operation is also performed for syntactically invalid credentials. `/healthz` is always exempt; the payment admin page is exempt so it can load and prompt for a token, which it keeps in `sessionStorage`. Payment sends `LEDGER_AUTH_TOKEN` to the ledger.
- Every response carries `X-Request-ID`. Safe caller-supplied values (at most 64 characters of letters, digits, `-`, `_`, `.`) are kept; others are replaced. Payment forwards the ID to the ledger. Middleware applies its request-ID and MIME-sniffing headers after handler execution so handlers cannot override them and panic replacement cannot leak partial headers or bodies. Request logs record `request_id`, `method`, `path`, `status`, `bytes`, `remote_addr`, and `duration_ms`.
- Each command exposes `loadConfig` and `run` so configuration and server wiring are testable without process exit or fixed ports.

## Consequences

- ID cursors are invalid once the referenced item is gone; acceptable while records are never deleted.
- A single shared token provides no per-principal identity, rotation, or authorization and is not a production authentication scheme.
- Pagination copies the in-memory list per request, which is acceptable at simulation scale.
- Integration tests exercise the full payment handler/client/ledger path and assert that both the caller request ID and configured ledger token reach every ledger request. Pagination tests cover exact-size and terminal pages, malformed/unknown/altered cursors, and duplicate-free traversal.
