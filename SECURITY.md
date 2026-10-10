# Security policy

## Supported versions

This project is pre-1.0. Security fixes are applied only to the latest commit on the default branch. No released version is currently supported for production use.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use the repository host's private security-advisory facility when a remote is established, or contact the maintainer privately through the organization-maintained channel. Include affected revision, reproduction steps using fake data, impact, and any proposed mitigation. Do not include credentials or real payment information.

Maintainers should acknowledge a report within five business days, coordinate validation and remediation privately, and publish a sanitized advisory after a fix is available.

## Scope and deployment warning

This is an in-memory simulation. It has no authentication, authorization, durable audit log, TLS termination, tenant isolation, or secret-management system. It must not process real funds or data and must not be exposed to an untrusted network. Compose binds published ports to loopback for this reason. See `docs/production-readiness.md` for the complete gap analysis.

Dependencies are intentionally minimal. CI pins action major versions and the Staticcheck version; maintainers should review updates before merging them.
