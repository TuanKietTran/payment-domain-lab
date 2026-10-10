# ADR-0004: Two-target container and Compose deployment

- Status: Accepted
- Date: 2026-10-09

## Context

The two commands need a reproducible local deployment without combining their runtime or weakening the service boundary.

## Decision

Use one multi-stage Dockerfile with separate `ledger-service` and `payment-service` runtime targets. Both run as an unprivileged user in minimal Alpine images. Compose builds each target, uses service-name networking, waits for ledger health, binds published ports to loopback, drops capabilities, prevents privilege escalation, and uses read-only filesystems.

Keep the direct binary workflow and a self-cleaning local smoke script so Docker is optional for development.

## Consequences

Build layers are shared while runtime images and entry points remain distinct. The images still inherit Alpine and must be rebuilt for base-image fixes. In-memory state is ephemeral by design. Registry publication, image signing, SBOM/provenance generation, and orchestration manifests remain release/deployment concerns outside this lab.
