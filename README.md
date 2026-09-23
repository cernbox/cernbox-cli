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

### Server-side gaps this client works around

Running the CLI against a real reva turned up several endpoints that exist but do nothing. Each is handled deliberately rather than left to fail:

| Gap | What the CLI does |
| --- | --- |
| `LOCK` returns a hardcoded token and records nothing; `UNLOCK` returns 501 | No `lock` command at all. One built on this would report success and lock nothing |
| `search-files` REPORT is a stub returning 501 | `find` asks the server first, then falls back to walking the tree client-side |
| OCS `remote_shares` is an empty handler that writes nothing | `ocm received` reads the graph `sharedWithMe` endpoint, filtering on the OCM id prefix |
| App-token creation is not exposed publicly | `token create` explains where to create one; `list` and `revoke` work normally |
| Reva's demo app provider advertises no mime types, so nothing can open anything | `open --web` works regardless; the application link needs a real provider such as Collabora |

**Two-way sync** is a deliberate omission rather than a gap. `sync` is a one-way mirror: genuine bidirectional synchronisation needs persistent per-file state to tell "changed here" from "deleted there", and without it the two are indistinguishable, which is how a sync tool deletes data it should have uploaded. That state is the desktop client's job.

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

Kerberos is native: the ticket is presented straight to CERNBox, which verifies it against its own keytab. Nothing stands between the ticket and the service, so a login depends on nothing being up but CERNBox itself.

Native Kerberos needs the server side that ships with this work: the `kerberos` auth manager and the `spnego` credential strategy, both in reva. `dev/revad/cernbox.toml` is a working configuration of the pair, against the realm the `kdc` container serves.

The ticket is presented to `/graph/v1.0/me` rather than to an endpoint built for the purpose. Every authenticated reva response already carries the issued token in `x-access-token`, so a dedicated one would only return a header the server already sends — and this one returns the user's identity too, so the login costs a single request.

If the endpoint is a DNS alias, the client may ask the KDC for a ticket under the node's name rather than the alias, and the KDC will refuse it. `--kerberos-spn` names the service principal explicitly; `rdns = false` in your `krb5.conf` fixes it for every Kerberos client on the machine.

Tokens are cached in `/tmp/cernbox_cc_$(id -u)` at mode `0600`, not in your home directory: on lxplus home is shared across every node in the cluster, and a bearer token there has a wider blast radius than one in node-local `/tmp`. Override with `$CERNBOX_TOKEN_CACHE`.

## Paths

Absolute CERNBox paths are canonical, and look like what you already type for `eos`:

```bash
cernbox ls /eos/user/g/gdelmont/Documents
cernbox ls /eos/project/c/cernbox/data
cernbox ls /home/Documents          # your own home space
```

Space-qualified aliases also work: `home:Documents`, `project/cernbox:data`.

### Listing

`ls` behaves like `ls(1)`: no header, one entry per line when piped, columns when a terminal is attached, and the familiar switches.

```bash
cernbox ls -l /eos/user/g/gdelmont     # long listing
cernbox ls -lt                          # newest first
cernbox ls -lSr                         # smallest first
cernbox ls -aF                          # include dotfiles, mark directories
cernbox ls -lh                          # human-readable sizes
```

| Flag | Meaning |
| --- | --- |
| `-l` | long listing: rights, size, modification time |
| `-h` | human-readable sizes (`1.2K`) instead of bytes |
| `-a` | include entries beginning with a dot |
| `-R` | recurse into subdirectories |
| `-1` | one entry per line, even on a terminal |
| `-t` `-S` `-r` | sort by time, by size, or reverse the order |
| `-F` | append `/` to directory names |
| `--sort` | `name`, `time` or `size` |

`-h` means human-readable, as in `ls` and `du`, so on those two commands help is `--help` only. Every other command keeps `-h` for help.

Colours come from **`LS_COLORS`**, the same variable `ls` reads, so whatever you configured with `dircolors` applies here too — directories, and per-extension rules like `*.pdf`. With the variable unset, the built-in defaults `ls` uses apply. Colour is emitted only to a terminal: piping gives clean text, as `ls` does. The BSD `LSCOLORS` variable is a different syntax and is deliberately not read.

One deliberate difference from `ls`: the long listing shows **one** rights column rather than owner/group/other:

```
total 3003
-rw-    3 Sep 23 08:58 notes.txt
-rw- 3000 Sep 23 08:58 report.pdf
drwx    0 Sep 23 08:58 sub
```

CERNBox reports the rights *you* have on an entry, and has no owner/group/other split to show — nor a POSIX mode, an owner, or a link count. Those columns are absent rather than invented. `????` in place of the rights means the server reported none, which is not the same as reporting none granted.

`--output json` and `--output csv` are unchanged by any of this: they keep their labelled columns, and directories keep their trailing slash there, so existing scripts are unaffected.

### Disk usage

`du` prints what `du(1)` prints: a size, a tab, a path, with a directory reported after everything it contains.

```console
$ cernbox du -h -d 2 /eos/user/g/gdelmont/data
2.9K	/eos/user/g/gdelmont/data/raw/2026
2.9K	/eos/user/g/gdelmont/data/raw
5.9K	/eos/user/g/gdelmont/data
```

`-h`, `-s`, `-a` and `-d`/`--max-depth` carry their usual meanings. One deliberate difference: only the total for each argument is reported unless `--max-depth` asks for more — that is `du -s` rather than `du`'s own default, because descending a whole tree here costs one request per directory, and the totals CERNBox reports are already recursive. Sizes are apparent bytes, not disk blocks, which the server does not report.

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
make dev-up             # revad + EOS + the federation partner, in Docker
make test-integration   # integration tests against the dev environment
make dev-down
make lint
```

The dev environment runs three containers: EOS, the CERNBox under test, and a second reva acting as a federation partner so the `ocm` commands have a real far end. Every command in the tree is exercised against it — `TestEveryCommandIsCovered` compares the binary's own command list against a map of which test covers what, and fails when a command is added without one.

See [docs/design.md](docs/design.md) for the full design, including the server-side Kerberos work.

## Licence

Apache 2.0. See [LICENSE](LICENSE).
