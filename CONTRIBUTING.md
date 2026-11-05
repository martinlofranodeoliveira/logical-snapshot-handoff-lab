# Contributing

This is an original MIT-licensed development project by Lofrano Tecnologia. Contributions should remain small, inspectable and consistent with the fixed 0.1.0 contract.

## Local checks

Prepare the dependencies in docs/dependencies.md under a nonroot Linux UID. Set `CGO_ENABLED=1`, `GOFLAGS=-tags=libsqlite3` and the actual external SQLite include/library/runtime paths. Then:

```sh
go mod tidy
go mod verify
gofmt -w cmd internal
LAB_INTEGRATION=1 go test -p 1 ./...
go run ./cmd/demo
```

The formatter command touches only source directories. Keep generated module checksums from actual Go tooling; never invent them. Do not commit runtime SQLite files, private clusters, captured personal data or compiled binaries.

## Review expectations

- Explain a behavioral change and update both language versions of the contract when needed.
- Include an independent expected result or regression, not merely a test that repeats the implementation.
- Preserve import-before-query, whole-transaction buffering, commit-before-feedback and unsigned LSN storage.
- Do not relax resource bounds, add endpoint configuration or silently recover missing history.
- New PostgreSQL types/messages require an explicit contract change and adversarial tests, not an implied compliance claim.
- Fixtures must be synthetic and fixed. Never include private client code, credentials or real personal data.
- Keep CI read-only with timeouts; no release, publishing or remote deployment automation.

Use repository issues or pull requests for ordinary changes. General organizational contact is available through https://lofranotecnologia.com.br. Security reports follow SECURITY.md and should not disclose vulnerabilities in public issues.
