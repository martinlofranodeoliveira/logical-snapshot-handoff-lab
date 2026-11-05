# Reproducible runtime and dependency preparation

Development 0.1.0 is qualified with Go 1.27.3, PostgreSQL 17.11 and external SQLite 3.54.1. `Dockerfile` pins the official Go Bookworm base by digest, verifies the official PGDG signing-key fingerprint and installs PostgreSQL 17.11 from its signed repository. `tools/build-sqlite.sh` verifies the official SQLite archive SHA-256 before extracting/building FTS5. It checks the actual SQL version/source ID. Public source metadata is tracked in `dependency-metadata/`; no private image is needed.

```sh
docker build -t logical-snapshot-handoff-lab:local .
docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --memory 1g --cpus 2 --read-only --tmpfs /tmp:rw,exec,nosuid,nodev,size=512m logical-snapshot-handoff-lab:local go test -p 1 ./...
docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --memory 1g --cpus 2 --read-only --tmpfs /tmp:rw,exec,nosuid,nodev,size=512m logical-snapshot-handoff-lab:local go run ./cmd/demo
```

The image executes under UID 1000 and has `LAB_INTEGRATION=1`, so an unavailable private-cluster test fails instead of skipping. PostgreSQL is a normal unprivileged foreground process inside that environment; the harness never invokes Docker or a host database. The final runtime needs only private Unix sockets and /tmp. Build preparation needs the public package/source registries; runtime execution is offline.

`go.mod` and `go.sum` were resolved with real Go tooling, the official Go proxy and Go checksum database; `go mod verify` checks the downloaded graph. Direct pins and hashes are recorded in `dependency-metadata/go-modules.json`. No lock/checksum is invented. Transitive dependency licenses remain their upstream licenses; repository-original code is MIT. Installed Debian versions are recorded in `/usr/local/share/wave8-package-versions.txt`; this does not pin every OS package or promise byte-identical whole images.

The `libsqlite3` build tag deliberately selects external SQLite and rejects the driver's bundled library. Actual SQL version/source ID is cross-checked with the driver API and included in reports; the application floor is SQLite 3.51.3. The prepared image qualifies 3.54.1 and includes matching headers/library. It does not infer backports from a distribution package version.

For direct local use, prepare the same PostgreSQL path `/usr/lib/postgresql/17/bin`, a qualified library/compiler, `CGO_ENABLED=1`, `GOFLAGS=-tags=libsqlite3`, and appropriate `CGO_CFLAGS`, `CGO_LDFLAGS`/loader configuration. Keep Go's module/cache directories outside ordinary source-package traversal (a dot-directory such as `.build` works). Module/runtime downloads are preparation, never application inference or credentials. No general CDC or sandbox claim is made.

The supplied image recipe qualifies Linux amd64. Its PGDG architecture entry and tested native toolchain are explicit; no other architecture is claimed by these validation records.
