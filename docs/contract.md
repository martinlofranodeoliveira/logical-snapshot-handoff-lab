# Development 0.1.0 contract

This contract describes a Linux-only, local, single-source lab, not PostgreSQL or SQLite standards compliance. Public control names are `bootstrap`, `catchUp`, `status`, `stop`; Go counterparts are methods of `internal/replica.Replica`. There is one synchronous request at a time and no listening control socket.

## Owned resources

The controller creates exactly one private PG17 cluster under a mode-0700 `/tmp/lsh-*` directory. `initdb` and the foreground postmaster run under the current nonroot UID. PostgreSQL listens only on that directory's mode-0700 Unix socket directory at fixed port 55432. The port is merely part of the private Unix socket filename. No TCP listener or supplied endpoint is accepted. All PG-prefixed environment variables are removed before pgx config parsing; child PostgreSQL processes receive a minimal environment. No host configuration files are consulted through libpq service/password defaults.

All workers, pipes, SQLite files, the writer lock and the source cluster belong to this controller. Workers are killed/reaped with a five-second bound; the postmaster receives fast shutdown (`SIGINT`) via its direct child `os.Process` handle, then a direct child `Kill` after five seconds (never a numeric negative-PGID signal), with another five-second reap bound. On abnormal exit, forced shutdown, or unreaped timeout, the private namespace directory is retained and cleanup reports an error. Temporary directories are removed only after their owned processes exit cleanly and are reaped. The standalone worker entry point requires inherited descriptors and a private-root configuration, not command-line endpoints.

## Data and bounds

The fixed publication is `lab_pub`, slot `lab_slot`, plugin `pgoutput`, relation `public.items`, replica identity DEFAULT. Column order, OIDs, type modifiers, non-null SQL layout, default replica identity and protocol key flags are validated. The three columns are `id int8 PRIMARY KEY`, `label text NOT NULL`, `revision int8 NOT NULL`. IDs cover signed int64; revisions must be positive. Labels must be valid UTF-8, NUL-free and at most 256 encoded bytes.

Bootstrap rejects the 33rd row. A committed projection has at most 32 rows. A replay transaction admits at most 64 insert/update/delete/truncate operations, and at most 65,536 decoded payload bytes: insert/update contribute 8 ID bytes + 8 revision bytes + UTF-8 label bytes; delete contributes 8 ID bytes; truncate contributes zero payload bytes but one operation. Row count is checked after all operations, not after each operation. A temporary intermediate image may exceed 32 rows inside the invisible SQLite transaction.

Raw logical messages are limited to 64 KiB, received CopyData to 96 KiB, and a buffered raw transaction capture to 128 KiB, including relation metadata, Begin and Commit. There is exactly one relation dictionary entry. Only the latest applied capture is held in memory. Output scanners are bounded to 512 KiB per response; requests to 2 KiB, worker configuration to 4 KiB. PostgreSQL and worker diagnostic capture each stop at 16 KiB; returned error text is sanitized and truncated to 600 bytes. The mature pgx wire reader can allocate a frame before this lab's CopyData length check; the source is the owned local server, not an untrusted network peer.

Run scenarios sequentially with `GOMAXPROCS=2`, `GOMEMLIMIT=256MiB`; PostgreSQL shared buffers are 16 MiB, work memory 1 MiB, maintenance memory 16 MiB, connections 8 and replication slots 1. These are conservative laboratory budgets, not OS-enforced aggregate memory/CPU quotas. WAL limits, SQLite autocheckpoint 64 pages and journal size limit 1 MiB are advisory, not physical disk caps. Long-lived inspectors can retain WAL. No disk-quota, power-loss or exactly-once claim is made.

## Bootstrap

`bootstrap` is explicit and destructive only to this lab's slot, never to an already committed projection on failure. It checks the fixed source layout/publication and source identity, explicitly drops an existing lab slot once inactive, and creates a new logical slot with `EXPORT_SNAPSHOT`. The replication exporter receives no further command and stays open while a separate `REPEATABLE READ READ ONLY` transaction executes `SET TRANSACTION SNAPSHOT` before any data query. It copies a bounded ordered image. SQLite atomically replaces rows and stores the consistent point, source system identifier, database OID, table OID, slot/publication names and applied count zero. The initial snapshot is not a replayed transaction.

A failed explicit rebootstrap may have replaced the source slot while preserving the old SQLite projection. Further replay can consequently require another explicit bootstrap. There is no implicit history repair.

## Replay, checkpoints and feedback

Protocol v1, text tuples, no streaming, no logical messages, no two-phase transactions. Buffer the entire supported transaction. Only Commit produces an apply request. Non-key updates, inserts, default-identity deletes and one-relation truncate without options are supported. Old tuples in UPDATE are rejected as primary-key mutation/unsupported identity. Missing relation metadata after restart is not guessed. Unknown messages/layouts/types, TOAST placeholders, binary values and malformed chronology fail closed.

`Commit.CommitLSN` must match Begin's final LSN (nonzero) and `Commit.TransactionEndLSN` must be larger. The latter is the endpoint, called EndLSN here. LSNs are unsigned 64-bit values stored as fixed 16-lowercase-hex TEXT, never signed SQLite INTEGER. There is no wall-clock replay ordering.

SQLite WAL mode with synchronous FULL commits changed rows, endpoint and scalar applied-transaction count in one transaction. A missing update/delete row or conflicting insert fails the whole apply. Endpoints <= the stored checkpoint have no logical effect and do not change the count. There is no unbounded per-transaction audit table. Overflow of the signed scalar count fails rather than wrapping.

Feedback write/flush/apply positions are the durable checkpoint only. They never use XLogData.WALStart, server WAL end or keepalive timestamps. A limit/layout/type/apply failure advances neither checkpoint nor feedback for that transaction. A keepalive may receive feedback for an earlier already durable checkpoint. The `feedback` barrier is after a new SQLite commit and before the transaction's feedback. Process death there permits replay; duplicate endpoints suppress duplicate projection effects.

The writer holds an exclusive advisory flock for its complete lifetime, including bootstrap, replay and status. Inspectors independently open SQLite mode=ro and use read transactions. They see committed images, and a pinned read transaction stays on its old image while a WAL-mode writer commits.

## Restart and catch-up

Before replay, stored identity is compared with the actual system/database/table/publication. Altered columns, types, modifiers, nullability, primary key or replica identity on `public.items` fail with `RELATION_LAYOUT`. The actual slot must exist with plugin pgoutput, database lab, no invalidation reason and `reserved` or `extended` WAL status. Its confirmed_flush_lsn and restart_lsn must be present and <= the projection checkpoint; the projection cannot be ahead of current source WAL. Violations return `REBOOTSTRAP_REQUIRED`, preserving the complete projection. This is a consistency check, not cryptographic authentication of arbitrary metadata edits or comprehensive failover detection.

Replay starts from the slot's confirmed point, intentionally allowing a commit whose feedback was lost to be delivered again. The relation dictionary starts empty and is reconstructed from actual protocol messages. No implicit slot creation occurs during restart.

`catchUp(targetLSN)` requires a nonzero target LSN and succeeds only after checking slot consistency and observing durable checkpoint >= target. An arbitrary `pg_current_wal_lsn()` can include unpublished/idle WAL and need never be a published commit endpoint. Timeout/cancellation returns `INCOMPLETE`; every other error response also marks `incomplete=true`. The last complete checkpoint is preserved. There is no success based on server WAL end. Controller fixtures request previous checkpoint + 1 only when the next known published fixture transaction has committed, then learn its actual endpoint from the result. The rollback oracle requests an unreachable increment without adding a marker transaction and expects `INCOMPLETE`.

## Control and output

Request fields: action, target (PostgreSQL hexadecimal LSN string), barrier (empty/snapshot/commit/feedback), timeout_ms (1..5000, zero defaults to 5000). Unknown fields/actions/barriers, action-incompatible barriers or targets, and out-of-range timeouts are rejected. `status` returns one complete SQLite image and linked-library qualification information. `stop` returns the final status then closes the writer. EOF also closes it. Status does not assert current source connectivity or catch-up.

Core error codes: `REBOOTSTRAP_REQUIRED`, `TRANSACTION_LIMIT`, `RELATION_LAYOUT`, `PRIMARY_KEY_MUTATION`, `UNSUPPORTED_TYPE`, `UNSUPPORTED_MESSAGE`, `INVALID_PROTOCOL`, `INVALID_ROW`, `APPLY_INVALID`, `INCOMPLETE`, `WRITER_BUSY`, `SQLITE_QUALIFICATION`. Unexpected I/O/library failures use `INTERNAL` or `SOURCE_ERROR`, with bounded diagnostics. A control-format error terminates the worker rather than attempting stream resynchronization.

The demo exits 0 when all eight assertions, including expected failure scenarios, pass. Its final JSON describes the experiment, not final replication health: the final slot is deliberately absent and the final source contains an unsupported transaction. Scenario success is separate from a `catchUp` success response.
