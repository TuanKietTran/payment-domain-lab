# Release and versioning guidance

The project follows Semantic Versioning. Until `v1.0.0`, minor versions may contain documented API changes and patch versions remain backward compatible within that minor line.

The compatibility surface includes `/v1` routes, JSON field names, state values, error codes, environment variable names, and command/container entry points. Human-readable error messages and admin presentation are not stable APIs.

## Release process

1. Confirm the tree contains no generated binaries, logs, credentials, or real data.
2. Update `CHANGELOG.md`, documentation, examples, and ADR status where needed.
3. Run `make check`, `make staticcheck`, and `make smoke`.
4. With Docker available, run `docker compose config --quiet`, `make docker-build`, and a Compose health check.
5. Review changes since the previous tag for API and migration impact.
6. Create an annotated `vMAJOR.MINOR.PATCH` tag from the reviewed default-branch commit.
7. Publish release notes derived from the changelog and attach checksums for any produced artifacts.

This repository does not automate publication and must not create a remote or publish images/releases as part of local verification.
