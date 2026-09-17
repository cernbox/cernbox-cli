# cernbox-cli

Command-line client for CERNBox. Browse, transfer, and share files from your terminal, with Kerberos single sign-on on lxplus.

```console
$ cernbox ls /eos/user/g/gdelmont
$ cernbox put ./report.pdf /eos/user/g/gdelmont/Documents/
$ cernbox share create /eos/user/g/gdelmont/Documents --with marie --role editor
```

On lxplus there is nothing to configure and nothing to log into: the CLI picks up the Kerberos ticket from your session, exactly like the `eos` command.

## Install

From source:

```bash
go install github.com/cernbox/cernbox-cli/cmd/cernbox@latest
```

RPM and deb packages are built by the release pipeline and attached to each release. Once the package is in the CERN repositories, `dnf install cernbox-cli` will be the way in on AlmaLinux.

## What works today

| Area | Commands |
| --- | --- |
| Identity | `login` `logout` `status` `whoami` |
| Browse | `ls` `stat` `find` `du` `cat` |
| Namespace | `mkdir` `touch` `rm` `mv` |
| Transfer | `cp` `get` `put` `sync` |
| Sharing | `share create/list/update/remove/received` |
| Links | `link create/list/remove/password` |
| Federated | `ocm invite/contacts/providers/received` |
| History | `trash list/restore/purge`, `versions list/restore/download` |
| Spaces | `space list/info` |
| Apps | `open` `apps` |
| Tokens | `token list/revoke` |
| Shell | `version` `completion` |

### Two things the CLI deliberately does not do

**Locking.** There is no `cernbox lock`. Reva's WebDAV `LOCK` handler is a placeholder: it returns the same hardcoded token (`opaquelocktoken:0000…`) to every caller and records nothing, and `UNLOCK` returns 501. A lock command built on it would report success and lock nothing, which is worse than not having one. It can be added once reva implements locking for real.

**Two-way sync.** `sync` is a one-way mirror. Genuine bidirectional synchronisation needs persistent per-file state to tell "changed here" from "deleted there"; without it the two are indistinguishable, which is how a sync tool deletes data it should have uploaded. That state is the desktop client's job.

`token create` reports how to create one instead of failing with a 404: CERNBox exposes listing and revocation of app tokens over its public API, but creation goes through the browser enrolment flow.

## Authentication

`cernbox` resolves credentials through an ordered chain and uses the first one that works. `cernbox status` tells you which one it picked.

| Order | Method | When it applies |
| --- | --- | --- |
| 1 | `--token` / `$CERNBOX_TOKEN` | Explicit token, mostly for CI |
| 2 | Cached session | A previous login that has not expired |
| 3 | Kerberos | A TGT is present — the lxplus path, silent |
| 4 | App token | `$CERNBOX_APP_TOKEN`, for batch jobs and cron |
| 5 | OIDC device flow | Laptops and accounts without a Kerberos principal |
| 6 | Basic auth | Dev instances only, and only with `--method basic` |

Basic authentication is never reached automatically. Against a server that expects Kerberos, falling through to a password prompt would be the wrong thing to do, so it joins the chain only when asked for by name.

Kerberos works in two modes, selected by `auth.kerberos.mode`:

- `sso` (default) — the ticket authenticates you to CERN SSO, which issues an OIDC token that CERNBox already accepts. No server-side change.
- `spnego` — the ticket is presented directly to CERNBox as a SPNEGO token. Requires the Kerberos auth provider to be enabled server side.
- `auto` — try `spnego`, fall back to `sso`.

Tokens are cached in `/tmp/cernbox_cc_$(id -u)` at mode `0600`, not in your home directory: on lxplus home is shared across every node in the cluster, and a bearer token there has a wider blast radius than one in node-local `/tmp`. Override with `$CERNBOX_TOKEN_CACHE`.

## Paths

Absolute CERNBox paths are canonical, and look like what you already type for `eos`:

```bash
cernbox ls /eos/user/g/gdelmont/Documents
cernbox ls /eos/project/c/cernbox/data
cernbox ls /home/Documents          # your own home space
```

Space-qualified aliases also work: `home:Documents`, `project/cernbox:data`.

### Why transfer commands need `cb:`

On lxplus `/eos/user/g/gdelmont` is *both* a CERNBox path and a local FUSE mount, so `cernbox cp /eos/... /eos/...` would be genuinely ambiguous. Commands that only ever touch the remote (`ls`, `rm`, `share`, …) take bare paths. Commands that move data between local and remote need the remote side marked:

```bash
cernbox cp ./report.pdf cb:/eos/user/g/gdelmont/Documents/
cernbox cp -r cb:/eos/project/c/cernbox/data ./data
```

`get` and `put` are unambiguous by position and are usually what you want:

```bash
cernbox put ./report.pdf /eos/user/g/gdelmont/Documents/
cernbox get /eos/user/g/gdelmont/Documents/report.pdf .
```

## Sharing

```bash
cernbox share create /eos/user/g/gdelmont/Documents --with marie --role editor
cernbox link create /eos/user/g/gdelmont/report.pdf --expiry 2026-12-31
cernbox share received
```

To share with someone at another institution, exchange an invitation first, then share as usual:

```bash
cernbox ocm invite create --recipient alice@other-lab.org
cernbox ocm contacts
cernbox share create /eos/user/g/gdelmont/data --with-remote alice@other-lab.org
```

## Scripting

Every command takes `--output json`, and exit codes are stable:

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Generic failure |
| 2 | Usage error |
| 3 | Authentication failure — try `kinit` |
| 4 | Permission denied |
| 5 | Not found |
| 6 | Conflict (lock, etag mismatch, quota) |

Code 3 versus 4 is the distinction that matters in scripts: 3 is worth retrying after refreshing credentials, 4 is not.

## Development

```bash
make build              # build ./cernbox
make test               # unit tests
make dev-up             # revad + EOS in Docker
make test-integration   # integration tests against the dev environment
make dev-down
make lint
```

See [docs/design.md](docs/design.md) for the full design, including the server-side Kerberos work.

## Licence

Apache 2.0. See [LICENSE](LICENSE).
