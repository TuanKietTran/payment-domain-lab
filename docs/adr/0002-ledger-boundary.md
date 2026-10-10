# ADR-0002: Ledger service boundary and idempotent HTTP posting

- Status: Accepted
- Date: 2026-10-09

## Context

Payment transitions have ledger effects, but payment rules must not depend on ledger store implementation or transport-domain structs. A ledger request can succeed even if its response is lost, so retries must not duplicate value movement.

## Decision

The payment domain defines a narrow `Ledger` port and payment-owned posting values. `LedgerClient` is an outbound HTTP adapter with private wire DTOs. It creates deterministic currency accounts idempotently and posts each transition using a key derived from the payment action idempotency key. The payment changes state only after a successful adapter response.

Ledger and payment remain separate HTTP services. Integration tests compose real handlers while domain tests use a fake port.

## Consequences

The dependency direction is payment domain to an interface, not to ledger domain code. A timed-out posting can be retried safely with the same key. The simulation does not provide durable recovery if a process crashes between remote success and local mutation; production use needs a durable outbox/workflow and reconciliation.
