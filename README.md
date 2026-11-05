# Logical Snapshot Handoff Lab

Development version **0.1.0**, by **Lofrano Tecnologia**. MIT, original implementation. [Português](README.pt-BR.md) · [Homepage](https://lofranotecnologia.com.br).

A deliberately narrow PostgreSQL 17 → SQLite projection experiment. It runs a real, private PostgreSQL cluster, a separate Go replicator process and an independent SQL controller. No database service, supplied endpoint, model, API, telemetry or network connection is used during tests/demo. Installing dependencies needs network access unless they are already cached.

This is not a CDC connector or a production-readiness claim. The useful artifact is the inspectable snapshot/change-stream handoff and its failure oracles.

## Prerequisites and installation

Linux, an **unprivileged UID**, Go 1.27+ (CI requests Go 1.27), a C compiler, PostgreSQL **17** binaries at `/usr/lib/postgresql/17/bin`, and an external SQLite **>=3.51.3** library plus matching headers are required. The included driver must be built with `libsqlite3`; old bundled SQLite is rejected, not silently used. Do not run the demo as root. PostgreSQL is initialized only in a private, owned `/tmp/lsh-*` directory and listens only on its private Unix socket.

See [dependency preparation](docs/dependencies.md), including qualification and source-download limitations. Direct modules are pinned in `go.mod`; the checksum file was resolved by real Go tooling. Resolve and verify the module graph with Go tooling before offline use:

```sh
export CGO_ENABLED=1
export GOFLAGS='-tags=libsqlite3'
# For a custom SQLite prefix, set these to its actual installation directory:
export CGO_CFLAGS='-I/opt/sqlite/include'
export CGO_LDFLAGS='-L/opt/sqlite/lib -Wl,-rpath,/opt/sqlite/lib'
export LD_LIBRARY_PATH=/opt/sqlite/lib
export GOMAXPROCS=2
export GOMEMLIMIT=256MiB
go mod tidy
go mod verify
go test -p 1 ./...
go run ./cmd/demo
```

Omit the three `/opt/sqlite` settings when the qualified system installation is already the compiler/linker's default. Environment settings apply to both commands. The exact linked SQLite version and source ID are queried and cross-checked against the driver's library functions, then included in the report. No execution or dependency qualification is claimed by merely providing these files.

`go test` always exercises the offline applier and decoder. Integration tests run when PG17 is available under nonroot; use `LAB_INTEGRATION=1 go test -p 1 ./...` to make unavailable integration prerequisites an error instead of a skip. The demo never skips them.

## Demo and output

Eight sequential scenarios exercise:

1. Pause after snapshot import, commit `2=B`, then obtain exactly `1=A, 2=B`.
2. Replay an actual captured supported transaction into an independent SQLite image twice.
3. Update/delete atomically while a separate inspector pins the old image.
4. Roll back an insert, without adding a marker transaction; an unattainable target reports `INCOMPLETE`.
5. Kill the worker after SQLite commit but before feedback; restart from the slot's confirmed point and observe a duplicate endpoint without a duplicate count.
6. Apply truncate plus insert transactionally.
7. Reject 65 changes without checkpoint advancement.
8. Drop the stopped slot and require explicit rebootstrap, preserving the complete projection.

Representative human-readable lines (runtime source ID and LSN are intentionally not invented):

```text
Logical Snapshot Handoff Lab — development 0.1.0
PASS: exported snapshot handoff
PASS: captured transaction replayed twice
PASS: atomic update/delete and pinned inspector
PASS: rollback without marker transaction; explicit incomplete target
PASS: process kill after commit before feedback; duplicate endpoint ignored
PASS: transactional truncate and insert
PASS: 65-change transaction rejected without checkpoint advance
PASS: missing slot requires explicit rebootstrap, preserving projection
PASS: all 8 sequential scenarios; source/projection equality asserted before intentional failures
```

The last stdout line is JSON. Stable fields include `version`, `passed_scenarios`, `sqlite_version`, `sqlite_source_id`, `checkpoint`, and `incomplete`; failures also contain `error`. PostgreSQL-generated identity, source ID and LSN values vary by installation/run. Scenario order and expected row values are fixed. Exit 0 means all scenario assertions passed, including the expected failure cases; it does **not** mean the intentionally damaged final source is caught up.

## Small API examples

The APIs are intentionally internal to this repository; there is no remote endpoint or arbitrary connection-string API. The controller uses inherited-pipe requests such as:

```json
{"action":"bootstrap","target":"0/0","barrier":"snapshot","timeout_ms":5000}
```

```json
{"action":"catchUp","target":"0/1234","barrier":"","timeout_ms":1000}
```

The second request succeeds **only** if a durable commit endpoint reaches the target. It may time out even when the server's WAL position is larger. `status` reads a complete SQLite snapshot; `stop` closes the owned writer. `--worker` is an implementation entry point requiring inherited controller descriptors, not a standalone connection interface.

A bounded offline-applier example within the repository:

```go
tx := model.Transaction{End: endpoint, Changes: []model.Change{
    {Kind: "update", Item: model.Item{ID: 1, Label: "A2", Revision: 2}},
    {Kind: "delete", Item: model.Item{ID: 2}},
}}
applied, err := projection.Apply(ctx, tx, nil)
```

`projection` must already have a bootstrap image and source identity. `endpoint` is the captured `Commit.TransactionEndLSN`, not a clock or transport position.

## Rationale and limitations

The exporter stays untouched and alive while a separate repeatable-read transaction imports its snapshot before any data query. SQLite atomically publishes the copied rows, consistent point and source identity. Replay buffers whole transactions, validates their subset, commits rows plus unsigned checkpoint under WAL/FULL, then acknowledges. A lifetime exclusive file lock fences writers; inspectors use read transactions.

Only `public.items(id bigint PRIMARY KEY, label text NOT NULL, revision bigint NOT NULL)` is supported: UTF-8/NUL-free label <=256 bytes, positive revision, <=32 final rows, <=64 changes and <=64 KiB decoded payload per transaction. Primary-key changes, schema changes, TOAST placeholders, binary tuples, arbitrary types/publications, two-phase commit and automatic slot-loss recovery are excluded. See the [precise contract](docs/contract.md), [design](docs/design.md), [protocol subset](docs/protocol.md), and [testing notes](docs/testing.md).

WAL settings and SQLite thresholds are advisory, not disk quotas. Storage durability remains the operating system/storage's responsibility. Process-kill tests are not power-loss tests, and idempotent endpoints are not an exactly-once transport claim. This version does not authenticate stored metadata against malicious local tampering or support source failover.

[Contributing](CONTRIBUTING.md) · [Security reports](SECURITY.md) · [Changes](CHANGELOG.md).
