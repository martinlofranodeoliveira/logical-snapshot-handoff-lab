# Changelog

## 0.1.0 — Unreleased

Development version; no published release or deployment is implied.

- Private nonroot PostgreSQL 17 source, separate replicator process and independent SQL controller.
- Exported-snapshot bootstrap with import-before-query and atomic SQLite image/checkpoint publication.
- Bounded pgoutput v1 transaction replay through pgx and pglogrepl.
- Unsigned commit endpoints, lifetime exclusive writer fencing and WAL/FULL checkpoint-before-feedback ordering.
- Explicit incomplete catch-up, transaction rejection and slot-loss rebootstrap errors.
- Eight sequential offline/local scenarios, real raw transaction capture replay and adversarial regression tests.
- External SQLite version/source-ID qualification, bilingual READMEs and bilingual state/protocol documentation.

### Handoff and runtime hardening

- Resolve actual Go module checksums and pin a public PG 17 / SQLite 3.54.1 image preparation recipe.
- Create the fixed private database before fixture admission and make inherited barrier pipes pollable for real deadlines.
- Signal only direct-child Go process handles, retain the namespace on forced postmaster shutdown, and drain response stdout before Wait.
- Expand real PostgreSQL boundary recovery and independent durable-state/unsigned-checkpoint regressions.
