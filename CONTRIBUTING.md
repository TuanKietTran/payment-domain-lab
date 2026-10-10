# Contributing

## Development setup

Use Go 1.23 or newer. No third-party runtime dependency or JavaScript toolchain is required.

1. Read `SPECS.md` and the architecture decisions in `docs/adr/`.
2. Run `make check` before submitting a change.
3. Run `make staticcheck` for Go changes and `make smoke` for API, UI, command, or deployment changes.
4. If Docker is available, run `docker compose config --quiet` and `make docker-build` for container changes.

## Change expectations

- Preserve integer minor-unit money handling; do not introduce floating point.
- Add tests for transitions, error paths, idempotency, atomicity, or concurrency affected by a change.
- Keep domain code independent of HTTP wire types and service implementations.
- Preserve structured JSON errors and backward-compatible v1 field/error-code behavior.
- Keep the admin accessible without a client build or external runtime dependency unless an accepted ADR changes that decision.
- Update documentation, samples, deployment files, and ADRs when behavior or architecture changes.

## Pull requests

Keep changes focused and explain the domain or operational impact. Complete the pull-request checklist. A change is ready when formatting, vet, unit/integration tests, race tests, Staticcheck, builds, and relevant smoke/container checks pass.

Do not include secrets, real payment data, generated binaries, logs, or local environment files. Use fake examples only.
