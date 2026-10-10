# Payment simulation

A dependency-free Go payment and double-entry ledger lab. All money is fake and represented as signed `int64` minor units in the ledger and positive `int64` minor units on payments. State is in memory and is lost when a service stops.

## Payment lifecycle

- `created -> authorized -> captured`
- `created|authorized -> canceled`
- `captured -> partially_refunded -> refunded`

Authorization moves value from customer payable to merchant pending. Capture moves it to merchant settled. Cancel reverses an authorization, and refunds move captured value from merchant settled back to customer payable. Every posting balances to zero in one three-letter currency. Writes require an `Idempotency-Key`; retrying the same canonical request returns the original result while changing the request returns HTTP 409.

## Run

Requires Go 1.23 or newer.

```sh
make run-ledger
# In another terminal:
make run-payment
```

Open <http://127.0.0.1:8080> for the admin UI. Stop each process with Ctrl-C.

For a containerized local deployment:

```sh
make compose-up
# Browse http://127.0.0.1:8080
make compose-down
```

Compose builds separate non-root images, waits for health checks, binds only to loopback, drops Linux capabilities, and uses read-only filesystems. Service state remains in memory and is removed with the containers.

### Configuration

| Variable | Service | Default | Meaning |
|---|---|---:|---|
| `LEDGER_ADDR` | ledger | `:8081` | HTTP listen address |
| `PAYMENT_ADDR` | payment | `:8080` | HTTP listen address |
| `LEDGER_URL` | payment | `http://127.0.0.1:8081` | Ledger base URL |
| `PAYMENT_LEDGER_TIMEOUT` | payment | `5s` | Per-request ledger timeout |
| `PAYMENT_AUTH_TOKEN` | payment | empty (auth off) | Static bearer token required on `/v1` routes; `/healthz` and the admin page stay open |
| `LEDGER_AUTH_TOKEN` | ledger, payment | empty (auth off) | Ledger: bearer token required on all routes except `/healthz`. Payment: token sent to the ledger |
| `HTTP_READ_HEADER_TIMEOUT` | both | `5s` | HTTP header timeout |
| `HTTP_READ_TIMEOUT` | both | `10s` | HTTP request read timeout |
| `HTTP_WRITE_TIMEOUT` | both | `15s` | HTTP response write timeout |
| `HTTP_IDLE_TIMEOUT` | both | `60s` | Keep-alive idle timeout |
| `SHUTDOWN_TIMEOUT` | both | `5s` | Graceful shutdown deadline |

Durations use Go syntax such as `750ms`, `5s`, or `1m` and must be positive; invalid values make the service exit with status 2.

### Authentication

Auth is off by default. When a token variable is set, requests need one unambiguous `Authorization` field using the RFC Bearer form (`Bearer` case-insensitively, one or more spaces, then one token68 credential). Leading or trailing whitespace, tab separators, trailing content, comma-joined credentials, and duplicate fields are rejected. Credentials are SHA-256 hashed and compared in constant time, including the comparison path for malformed input; failures return `401` with `WWW-Authenticate`. The admin page asks for the token in a password field; selecting **Apply token** stores the trimmed value in `sessionStorage` for the tab, resets pagination, and reloads the list. A single static shared token is not a substitute for real authentication.

### Pagination

`GET /v1/payments`, `/v1/accounts`, and `/v1/postings` accept `limit` (default 50, max 200) and an opaque `cursor`. Responses include `next_cursor` only when more items remain. Invalid limits or cursors return `400` (`invalid_limit`, `invalid_cursor`). The admin's **Load more** appends pages; refreshes and token changes reset pagination, and failed refreshes preserve already rendered payment cards.

### Request IDs

Every response carries `X-Request-ID`. A caller-supplied value (up to 64 characters of letters, digits, `-`, `_`, `.`) is kept; otherwise one is generated. Payment forwards it to the ledger. Middleware owns the response request-ID and MIME-sniffing headers, including on errors and recovered panics, so handlers cannot replace them. Request logs include `request_id`, `method`, `path`, `status`, `bytes`, `remote_addr`, and `duration_ms`.

## API walkthrough

Create a payment and copy its `id` into `PAYMENT_ID`:

```sh
curl -sS http://127.0.0.1:8080/v1/payments \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: create-demo-1' \
  -d '{"amount":1250,"currency":"USD","description":"Demo order"}'

PAYMENT_ID=pay_replace_me

curl -sS -X POST "http://127.0.0.1:8080/v1/payments/$PAYMENT_ID/actions/authorize" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: authorize-demo-1' -d '{}'

curl -sS -X POST "http://127.0.0.1:8080/v1/payments/$PAYMENT_ID/actions/capture" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: capture-demo-1' -d '{}'

curl -sS -X POST "http://127.0.0.1:8080/v1/payments/$PAYMENT_ID/actions/refund" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: refund-demo-1' -d '{"amount":250}'

curl -sS http://127.0.0.1:8080/v1/payments
curl -sS http://127.0.0.1:8081/v1/accounts
curl -sS http://127.0.0.1:8081/v1/postings
```

The HTTP API is:

| Service | Method and path | Purpose |
|---|---|---|
| payment | `GET /healthz` | Liveness |
| payment | `GET`, `POST /v1/payments` | Paginated list; create with `Idempotency-Key` |
| payment | `GET /v1/payments/{id}` | Retrieve one payment |
| payment | `POST /v1/payments/{id}/actions/{authorize|capture|cancel|refund}` | Apply an idempotent lifecycle action |
| ledger | `GET /healthz` | Liveness |
| ledger | `GET`, `POST /v1/accounts` | Paginated list; create with `Idempotency-Key` |
| ledger | `GET /v1/accounts/{id}` | Retrieve one account |
| ledger | `GET`, `POST /v1/postings` | Paginated list; create a balanced posting with `Idempotency-Key` |

Write requests must contain exactly one top-level JSON object, reject unknown fields, and are limited to 1 MiB. Oversized bodies return `413 request_too_large`; malformed, empty, scalar, array, `null`, or trailing JSON returns `400 invalid_json`. Unsupported methods return `405` with `Allow`; unknown routes and resources return `404`. API failures always use JSON:

```json
{"error":{"code":"invalid_transition","message":"cannot capture a payment in captured state"}}
```

## Verification

```sh
make check       # format check, vet, tests, race tests, and builds
make staticcheck # Staticcheck (downloads the pinned tool on first use)
make smoke       # build, launch both locally, exercise the lifecycle, stop
make test
make test-race
make build
make docker-build
```

The test suite includes store-level invariants, concurrency and idempotency checks, HTTP error behavior, immutable posting checks, overflow atomicity, duplicate-free pagination and cursor failures, buffered-response and shutdown behavior, and real cross-service HTTP coverage proving payment forwards both ledger auth and caller request IDs. GitHub Actions runs the complete checks, Staticcheck, the local deployment smoke test, Compose validation, and both container builds; see `.github/workflows/ci.yml`.

The explicit specification and deployment-readiness review is in [`docs/production-readiness.md`](docs/production-readiness.md). Consequential choices are recorded in [`docs/adr/`](docs/adr/). See [`CONTRIBUTING.md`](CONTRIBUTING.md), [`SECURITY.md`](SECURITY.md), [`RELEASING.md`](RELEASING.md), and [`CHANGELOG.md`](CHANGELOG.md) for repository policy.
