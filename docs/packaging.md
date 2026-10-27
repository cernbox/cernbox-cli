# Packaging the cernbox CLI

From 2.0, `cernbox` is reva's CLI (`cmd/reva`, `pkg/cli` in cs3org/reva), rebranded at build time. gaia builds it,
the same way as revad:

```
gaia build --binary reva --brand cernbox.toml --with github.com/cernbox/reva-plugins/cli -o cernbox
```

- `--binary reva` builds the CLI instead of the daemon;
- `--with github.com/cernbox/reva-plugins/cli` compiles in the CERNBox CLI plugins (`otg`, `user account`, CERN SSO,
  `clipboard`, `inbox`, `outbox`);
- `--brand cernbox.toml` embeds the CERNBox brand: the program name, the endpoint, the sign-in chain, the
  environment variables and the texts. The brand is `cli/cernbox.toml` in reva-plugins, next to the plugins it names.

This repository no longer holds the client. It holds what turns that build into releases: the goreleaser
configuration, the packages, `install.sh`, the documentation, and the end-to-end tests with their dev environment.

## The pipeline

```
packaging/versions.env ──> packaging/prepare.sh ──> ws/ ──> goreleaser ──> dist/
   GAIA_VERSION              gaia build                go.mod, go.sum     linux, darwin, windows
   REVA_VERSION              --only-prepare            main.go            amd64, arm64
   REVA_PLUGINS_VERSION      --binary reva             brand.toml         tar.gz, zip, rpm, deb
                             --brand …/cernbox.toml    bflags             checksums.txt
                             --with …/cli@version
```

1. **Prepare.** `packaging/prepare.sh` runs gaia's prepare stage into `ws/`. gaia writes a Go module whose `main.go`
   imports reva's CLI and the plugins and embeds the brand, then runs `go run . __brandcheck` in it: reva checks the
   brand strictly against the plugins of the same build (every sign-in method of the chain exists, every hidden group
   exists) and checks the plugins. A bad brand fails here. The brand is read from the module cache, at the version of
   reva-plugins that the build uses, so the brand and the plugins cannot come from two versions.
2. **Version.** gaia writes `ws/bflags`: the `-X` flags that set reva's `pkg/rversion` (version, commit, Go version,
   build date). `cernbox version` prints them, and `cernbox version --plugins` adds the brand (with its SHA-256) and
   every plugin with its module version.
3. **Build.** goreleaser cross-compiles `ws/` (`dir: ws`, `main: .`) with `CGO_ENABLED=0`, `-trimpath` and
   `-ldflags "-s -w $BFLAGS"`, where `BFLAGS` is the content of `ws/bflags`. goreleaser reads its environment before
   any hook runs, so prepare runs before goreleaser, not as a hook. `GOWORK=off` keeps a `go.work` above the
   workspace out of the build.
4. **Package.** The same artefacts, names and install paths as 1.x:
   - archives `cernbox-cli_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) with `cernbox` at the root, which
     `install.sh` unpacks;
   - packages `cernbox-cli` (RPM and deb): `/usr/bin/cernbox`, `/etc/cernbox/config.toml` (a config file, kept on
     upgrade), and the bash, zsh and fish completions, generated from the binary by `packaging/completions.sh`;
   - `checksums.txt`.

The CI (`.github/workflows/_build.yml`) prepares the workspace once, on Linux, and hands it to the other jobs as an
artefact: the build and smoke test on Linux, macOS (arm64 and amd64) and Windows, the end-to-end tests, and the
release. What is tested is what is released.

## Building locally

```bash
go install github.com/cs3org/gaia@<GAIA_VERSION>
make prepare        # ws/, from the published versions of packaging/versions.env
make build          # ./cernbox for this machine
make snapshot       # dist/, as a release, without publishing (needs goreleaser)
```

With local checkouts of reva and reva-plugins:

```bash
make prepare REVA_DIR=~/reva REVA_PLUGINS_DIR=~/reva-plugins
make prepare REVA_DIR=~/reva REVA_PLUGINS_DIR=~/reva-plugins BRAND=/tmp/test.toml
```

A local checkout puts its absolute path into `ws/go.mod`, so a release uses published versions only. `GAIA` names
another gaia binary. The workspace must not be inside a tree with a `go.work`: gaia does not pass `GOWORK` to `go`,
and `prepare.sh` refuses such a workspace.

Without goreleaser, the same build by hand:

```bash
make prepare
cd ws && GOWORK=off CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
    go build -trimpath -ldflags "-s -w $(cat bflags)" -o ../dist/darwin_arm64/cernbox .
```

Name the output: the module of the workspace is `reva`, so `go build` without `-o` writes `reva`.

## Releasing

1. Set the three versions in `packaging/versions.env` to published releases (tags, not branches). gaia looks the reva
   version up on api.github.com to find its commit, and panics if that call fails: an unauthenticated runner shares
   60 calls an hour with its neighbours, so a failed prepare is worth one retry before anything else.
2. Update `docs/release-notes-<major>.md` if the CLI changed in a way users see.
3. Tag `v<version>`. The release workflow builds, runs the tests, and publishes.

The version of the packages and of the archives is the tag of this repository. `cernbox version` prints the version
of reva, and `cernbox version --plugins` the versions of the plugins: together they say what a binary contains.

## Installing on CERNBox hosts

The package is `cernbox-cli`. It installs `/usr/bin/cernbox` and `/etc/cernbox/config.toml`. On a host, puppet
writes `/etc/cernbox/config.toml` with the local `gateway` and `admin_host`; the brand's `transport = "auto"` then
uses gRPC there. The admin socket (`/run/reva/admin.sock`) needs no configuration.

Today the RPM is attached to the GitHub release (`https://github.com/cernbox/cernbox-cli/releases`). The CERNBox
hosts install from the koji tag `cernbox-ops` (`reva-cernbox`, `reva-cernbox-cli`), so the RPM also has to be
imported there. Note that the `ocis` hosts' manifest (`hg_box::include::ocis::reva::base`) has `cernbox-cli` set to
`ensure => absent`, for an older package of the same name: that has to go in the same change that installs this one.

## Retiring the Go code of this repository

The client code (`cmd/`, `internal/`, `pkg/`) is now in reva and reva-plugins. It stays here until the merged CLI
passes the end-to-end suite, so that the suite can still be run against 1.x for comparison
(`go test -tags integration ./integration/...` without `CERNBOX_BINARY` builds `cmd/cernbox`). Then, in this order:

1. **Port the end-to-end suite** (`integration/`) to 2.0, against `CERNBOX_BINARY`:
   - sign in once per account with `login --method basic` instead of passing `--method basic` to every command;
   - read the CS3 JSON (release notes, section 1) in the helpers and the tests: `sweep`, the share, link, token and
     trash tests, and every `json.Unmarshal` of a listing;
   - check exit code 1 where the tests expect 3 to 6;
   - follow the moved commands and flags (release notes, section 3);
   - keep the dev environment (`dev/`): it is what the suite runs against.
   The release workflow already runs the suite against the merged binary, so CI is red until this is done.
2. **Close the gaps** listed under "Not in this build yet" in the release notes, in reva and reva-plugins, or move
   them into the notes as changes.
3. **Release 2.0** from this repository.
4. **Delete the client**, in one commit: `cmd/`, `internal/`, `pkg/`, `.golangci.yml`, and the unit-test and lint
   jobs that cover them. `go.mod` stays, for `integration/` and `packaging/` (the tests of `install.sh`), with only
   their dependencies. The `TestMain` fallback that builds `cmd/cernbox` goes with it.
5. **Rewrite the user documentation** for 2.0: `README.md`, `docs/configuration.md` (TOML, the `[plugins.*]`
   sections, the environment variables of the brand) and `docs/behaviour.md` move to the CERNBox CLI docs, or are
   rewritten here. `docs/design.md` is the design of 1.x, and stays as history. Until then, the unit tests of 1.x
   check the YAML example in `docs/configuration.md`, so it changes together with the code.
6. **Update `CLAUDE.md`**: the repository has no client code, and its rules about the token cache, exit codes and
   `pkg/client` belong to reva now.
