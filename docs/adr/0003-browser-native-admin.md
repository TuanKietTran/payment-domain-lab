# ADR-0003: Browser-native admin client

- Status: Accepted
- Date: 2026-10-09

## Context

The admin is one responsive screen with a create form, payment cards, state-dependent actions, and inspectable JSON results. Adding a framework would introduce a package manager, build pipeline, runtime dependencies, update burden, and supply-chain surface.

## Decision

Use embedded semantic HTML, CSS, and browser-native JavaScript. Use native form validation and controls, explicit labels, live result announcements, visible focus, reduced-motion support, escaped untrusted content, and server-authoritative transitions. Refresh payment state after successful and failed actions while retaining the structured result.

No external fonts, scripts, styles, or network origins are used. The server sends cache and browser security headers. Inline assets are accepted for this small self-contained artifact.

## Consequences

The service builds with Go alone and the admin works without a separate asset deployment. The code must maintain its own small rendering and request helpers. If client complexity grows, first separate static assets and remove CSP `unsafe-inline`; reconsider a framework only with a superseding ADR and a demonstrated maintenance benefit.
