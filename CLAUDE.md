# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Build & test

```bash
make build              # build ./cernbox
make test               # unit tests
make test-race          # unit tests with the race detector
make test-integration   # integration tests (needs make dev-up first)
make dev-up             # revad + EOS in Docker
make dev-down
make lint               # go vet + golangci-lint
```

Run a single test:

```bash
go test ./pkg/pathspec/ -run TestParse
```

## Architecture

`cernbox` is a single statically-linkable binary. It is a client of the public CERNBox HTTPS surface — the same one the web UI uses — and never speaks CS3 gRPC.

**Packages:**

- `cmd/cernbox` — entry point, version stamping, exit-code mapping
- `internal/cli` — cobra command tree, one file per command group
- `pkg/pathspec` — parses remote/local path arguments, handles the `cb:` prefix and `space:` aliases
- `pkg/client` — HTTP layer: capabilities discovery, WebDAV, TUS, ocgraph, OCS, archiver
- `pkg/auth` — credential chain, providers, token cache
- `pkg/transfer` — parallel transfer engine with resume and checksums
- `pkg/output` — table / JSON / CSV renderers
- `integration` — build-tagged (`integration`) end-to-end tests that drive the real binary against the dev revad

**Server endpoints used:** `PROPFIND /remote.php/dav/files/{user}/{path}` for path-addressed operations, `/remote.php/dav/spaces/{space-id}/{rel}` for id-addressed ones, `/graph/v1beta1/...` for spaces and shares, `/ocs/v1.php/cloud/capabilities` for feature discovery, and the archiver for recursive downloads.

## Project rules

### Path arguments

Namespace-only commands take bare remote paths. Transfer commands require the remote side to carry a `cb:` prefix, because on lxplus `/eos/...` is simultaneously a valid remote path and a local FUSE mount. Never add a heuristic that guesses which side is which — `pkg/pathspec` is the single place this is decided.

### Credentials

Never log a token, even at trace level. The token cache lives in node-local `/tmp`, never in the user's home directory, because home is cluster-shared on lxplus. Cache entries are keyed by `(endpoint, principal)` so switching principal with `kinit` does not silently reuse the previous identity.

### Errors

User-facing errors are sentences, not Go error chains. Map them through `pkg/client.Error` so the exit code is right: 3 for auth, 4 for permission, 5 for not found, 6 for conflict. Scripts branch on 3 versus 4, so that distinction must stay accurate.

### Verification

- Always build and run the tests after a change and before declaring a task complete.
- Integration tests use the dev environment (`make dev-up`). Check revad logs with `make dev-logs` when they fail.
- After writing or modifying Go code, run `go fix` on the affected packages and apply modernizations within the regions you touched.
