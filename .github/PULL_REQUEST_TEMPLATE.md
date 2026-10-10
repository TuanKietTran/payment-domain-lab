## Summary

<!-- Describe the domain, API, UI, or operational change. -->

## Verification

- [ ] `make check`
- [ ] `make staticcheck`
- [ ] `make smoke` when behavior/deployment is affected
- [ ] Container checks when container files are affected, or noted unavailable
- [ ] Browser/accessibility verification when the admin UI is affected

## Risk and compatibility

- [ ] Money remains integer minor units and ledger postings remain balanced
- [ ] Idempotency, concurrency, failure atomicity, and error contracts were considered
- [ ] `/v1` compatibility and configuration impact are documented
- [ ] Tests and documentation were updated
- [ ] No secrets, real payment data, binaries, or logs are included
