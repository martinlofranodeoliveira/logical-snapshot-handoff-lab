# Testing and evidence boundaries

## Commands

After dependency preparation and with `GOFLAGS=-tags=libsqlite3`, `CGO_ENABLED=1` and matching external SQLite paths:

```sh
go mod tidy
go mod verify
go test -p 1 ./...
LAB_INTEGRATION=1 go test -p 1 ./...
go run ./cmd/demo
```

The second test invocation forbids silently skipping unavailable PG17/nonroot integration. Tests are not parallelized across private clusters. CI uses Go 1.27, a nonroot runner, two Go scheduler threads and a 256 MiB Go heap target. The stated 1 GiB / 2 CPU lab budget is a scenario design budget, not a measured benchmark or a hard aggregate cgroup guarantee.

## Independent oracles

- Exported-snapshot race: the worker pauses after import and before its first row query; controller commits row 2. Bootstrap must contain only row 1, then direct source and independent SQLite SQL must each equal the fixed two-row result.
- Atomicity: a separate SQLite transaction pins the old two-row image. The worker buffers a committed source update/delete, pauses before SQLite commit, and independent SQL still sees the old image. After release, a fresh inspector sees exactly one final row while the pinned inspector still sees the old two rows.
- Rollback: no marker transaction is committed. An unreachable checkpoint increment times out; row image, checkpoint and count remain unchanged. This distinguishes rollback from an extra real transaction used only to observe progress.
- Lost feedback: controller queries committed SQLite checkpoint/count at the feedback barrier, kills and reaps the worker, commits another known fixture transaction, then restarts. At least one old endpoint must be observed and the durable counter grows only for the new transaction.
- Captured replay: actual supported raw frames from the handoff transaction are decoded into a separate applier image. First apply must create exactly the expected two rows with count one; second apply changes nothing.
- Oversized transaction: 65 updates are committed at the source but rejected by the worker; incomplete error and unchanged rows/checkpoint/count are required.
- Missing slot: stopped slot is dropped only in the owned cluster; restart returns REBOOTSTRAP_REQUIRED, preserving the image.

## Offline regression and adversarial coverage

Tests cover unsigned LSNs crossing 0x8000000000000000 and reaching 0xffffffffffffffff, JSON round trips, canonical checkpoint storage, duplicate endpoints, reopen durability, writer fencing, conflicting inserts, missing updates, invalid revisions, invalid UTF-8/NUL labels, whole-transaction rollback, final row count overflow, bootstrap row overflow, synthetic commit-barrier errors, missing/replaced relation metadata, NULL/binary/TOAST tuple values, primary-key update old tuples, 65-change rejection and malformed protocol frames.

These are deterministic regression oracles, not broad fuzzing, throughput benchmarks or standards conformance certification. The real server integration exercises pgx/pglogrepl interoperability and source chronology; offline tests intentionally do not fake success when the qualified SQLite library is unavailable.

## Failure interpretation

An expected TRANSACTION_LIMIT or missing-slot response is a successful test assertion, not a successful catch-up. The demo's final JSON is an experiment report; inspect each operation response to determine replication health. A runtime failure returns nonzero with completed scenario names and incomplete=true. Raw source errors are not allowed to grow diagnostics without bound.

These files have no attached execution transcript. Run the listed commands on the prepared host to obtain actual qualification and regression evidence. Review source-download integrity and the generated Go checksum graph as described in dependencies.md.

## Additional boundary and process regressions

The real PostgreSQL boundary suite leaves a snapshot barrier unreleased until its request deadline, rejects 33 rows during both bootstrap and replay, rejects primary-key mutation without checkpoint advancement, repairs the source only through explicit rebootstrap, and refuses an altered relation layout. Direct SQL checks retained source/projection state. Store tests cross the unsigned LSN high bit and maximum value, ignore duplicate endpoints, reject a 33-row final image, permit an intermediate 33-row image whose same transaction finishes at 32, and fail closed at the applied-counter limit.

Twelve actual short-lived helper processes return an 80 KB JSON payload then exit immediately; the controller must drain it before Cmd.Wait. Direct-child process handles are also tested after actual Wait, when Signal/Kill must report ErrProcessDone. These qualify the concrete Go/Linux handle policy, not forced PID reuse or arbitrary descendant cleanup.
