# Security policy

Logical Snapshot Handoff Lab is development version 0.1.0, a local experiment, not a general-purpose replication service. No production deployment or security certification is claimed.

## Reporting

Report vulnerabilities privately using GitHub private vulnerability reporting:

https://github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/security/advisories/new

Do not include credentials, real personal data or private client code. Provide a minimal synthetic reproducer, the linked SQLite version/source ID, PostgreSQL version and the affected commit/checkpoint behavior when possible. No response-time SLA is offered. General contact information is available only through the official homepage: https://lofranotecnologia.com.br.

## Boundaries

- Only an owned, private PG17 Unix-socket source is supported; there is no remote endpoint API.
- Run under an unprivileged UID. Private roots require current-UID ownership and mode 0700.
- The inherited-pipe protocol is not a network authentication protocol.
- WAL retention and SQLite thresholds are advisory, not disk quotas.
- The lab does not protect against malicious same-UID processes, arbitrary local file replacement or deliberate metadata tampering.
- A missing or invalid slot requires explicit rebootstrap; history is never skipped silently.
- Storage-level durability depends on the actual filesystem, OS and device. Process-kill tests do not establish power-loss safety.
- Build dependencies require independent supply-chain review. See docs/dependencies.md for exact pins, SQLite linkage checks and source-download limitations.

Do not expose the worker as a server or reuse its private-source assumptions for untrusted databases without a separate threat model and design.
