# Design

## Why a real source and a small projection

An application-written outbox does not exercise exported snapshots, pgoutput transaction boundaries or replication feedback. This lab therefore uses PostgreSQL's actual WAL-derived logical stream. SQLite makes the projection independently inspectable with SQL. The supported schema is intentionally fixed so that decoding, restart and failure contracts fit in a reviewable implementation.

The repository contains 42 purposeful files: implementation and regression tests, two fixed SQL fixtures, a local demo, one test-only CI workflow, repository policy files and bilingual protocol/state documentation. There is no remote-deployment or publication workflow.

## Processes and pipes

The demo/controller owns the private root and foreground postmaster. Its own executable is started again as a distinct worker process; tests similarly start their test executable in a narrowly selected worker test. This avoids compiling another program during an offline scenario. It is a separate OS process with its own SQLite connection, exclusive writer lock and replication connection, not a controller goroutine masquerading as a crash target.

Inherited descriptors: stdin/stdout carry synchronous bounded JSON-lines requests/responses; fd 3 receives one-byte barrier releases; fd 4 emits barrier names; fd 5 contains one bounded private-root configuration. There are no supplied connection strings. The only barriers are snapshot (after import, before copying), commit (before SQLite commit) and feedback (after commit, before acknowledgement).

The controller's source SQL connection and SQLite mode=ro connections are independent of the worker. During the atomicity scenario, a controller read transaction pins the old SQLite snapshot. A second read sees old state at the precommit barrier; after release, a fresh read sees exactly the final state, while the pinned transaction still sees the old one.

## States

| State | Durable image | Permitted next action |
|---|---|---|
| Empty | no metadata, no rows | explicit bootstrap |
| Bootstrapping | previous committed image remains visible | finish atomic replacement or fail |
| Ready | rows + identity + checkpoint + scalar count | status, catchUp, explicit bootstrap, stop |
| Buffering | unchanged Ready image | supported Commit or failure |
| Committed, feedback outstanding | new complete image | acknowledge or restart/replay |
| Failed catch-up | last complete image | inspect; retry only if history/contract permits |
| Rebootstrap required | preserved image, unsafe/missing history | explicit bootstrap or stop |

There is no persisted in-progress transaction. PostgreSQL retains the source history, SQLite retains the complete image and checkpoint, and advisory flock state is only process-lifetime state. The most recent raw capture is bounded ephemeral diagnostic data.

## Atomicity and unsigned checkpoints

Both bootstrap and apply use one SQLite transaction. All validation failures before commit roll back. The applied counter increments once per new supported Commit, not per row, and never for bootstrap, rollback or duplicate endpoints. Checkpoints use canonical fixed-width hex TEXT so every uint64 LSN, including values above MaxInt64, is representable. The store never casts LSNs to signed integers.

Replay validates even transactions whose endpoints are old. A malformed/oversized transaction does not become admissible merely because it appears duplicated. Duplicate suppression occurs inside the durable applier transaction after validation and reading the checkpoint.

## Restart conservatism

Restart checks source identity, publication, slot plugin/database/invalidation and slot/projection chronology. It starts at confirmed_flush_lsn, not an optimistic server WAL end, which exposes lost-feedback replay to the duplicate endpoint guard. Relation mappings are reconstructed from pgoutput Relation messages, not silently loaded from stale application metadata.

Slot checks intentionally do not attempt to prove all possible failover histories or recover arbitrary edits to SQLite. The lab owns its source, has no failover API and excludes malicious local metadata tampering. Missing history is a loud error with the old image retained.

## Operational boundaries

WAL/FULL plus feedback ordering is a storage-level intent, not proof against hardware faults. The crash scenario uses SIGKILL on the worker process after commit, not an emulated power loss. The private postmaster is shut down/reaped before its files are removed. Any incomplete catch-up is reported explicitly. A successful experiment can include deliberately unsuccessful catch-up operations.

The CI downloads build-time dependencies; runtime scenarios are local/offline. Qualification is observable through actual linked SQLite version/source ID and the resolved Go module graph, rather than fabricated dependency-execution claims.

## Direct child handle lifetime

Linux/Go 1.27 process shutdown uses only the direct child's `os.Process` handle, including SIGINT and forced Kill; no numeric negative-PGID signal is used. The Go runtime's pidfd path refers to one process identity, and its Linux fallback seals PID signalling before Wait4 under the signal lock. See [Go os/exec_unix.go](https://go.dev/src/os/exec_unix.go) and [pidfd_linux.go](https://go.dev/src/os/pidfd_linux.go). A caller cannot reconstruct a process handle from a stale PID. Tests perform actual Wait then assert Signal/Kill return ErrProcessDone, and close an already reaped owned child; they do not force PID reuse. Normal PostgreSQL SIGINT shutdown coordinates its own backends. Forced shutdown retains the namespace and returns an error; arbitrary descendant cleanup is not promised.
