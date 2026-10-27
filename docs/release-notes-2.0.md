# cernbox 2.0: release notes and migration guide

`cernbox` 2.0 is a new program under the old name. It is reva's command line client, built for CERNBox: the same code
as the `reva` CLI, with the CERNBox commands compiled in and the CERNBox defaults built in. cernbox-cli 1.x, cernboxcop
and the stock `reva` CLI become one program, so users and operators run the same binary.

What stays the same:

- the name of the binary (`cernbox`, `cernbox.exe`), of the archives (`cernbox-cli_<version>_<os>_<arch>`) and of the
  packages (`cernbox-cli`, RPM and deb);
- the install paths: `/usr/bin/cernbox`, and the shell completions in the same places;
- `curl cli.cernbox.cern.ch | sh`;
- the defaults: `https://cernbox.cern.ch`, Kerberos sign-in with a ticket, the device flow elsewhere, 8 MiB chunks, the
  archiver for folders;
- the command names you know: `ls`, `cp` with `cb:`, `sync`, `share`, `link`, `trash`, `token`, `space info`,
  `doctor`, `status`.

What changes, in order of how likely it is to break a script:

1. the JSON output is the CS3 JSON;
2. the exit codes are 0, 1 and 2;
3. some flags and commands moved;
4. the configuration file is TOML;
5. the token cache moves to the config directory.

Check your scripts against each section before you upgrade a machine that runs them.

## 1. JSON output is CS3 protojson

`--output json` (`-o json`) now prints the CS3 message of each resource, in the standard protobuf JSON mapping. Field
names are lowerCamelCase, enums are their names, and 64-bit numbers are **strings**. A field with its zero value is
left out: an empty file has no `size`.

### Files: `ls`, `stat`, `find`

| 1.x | 2.0 |
|---|---|
| `name` | `name` |
| `path` | `path` |
| `size` (number) | `size` (string, absent when 0) |
| `is_dir` (bool) | `type`: `"RESOURCE_TYPE_CONTAINER"` or `"RESOURCE_TYPE_FILE"` |
| `modified` (RFC 3339) | `mtime.seconds` (string, Unix time) and `mtime.nanos` |
| `id` (one string) | `id.storageId`, `id.spaceId`, `id.opaqueId` |
| `etag` | `etag` |
| `mime_type` | `mimeType` |
| `permissions` (WebDAV letters) | `permissionSet` (one boolean per permission) |
| `checksums` (map) | `checksum.type` and `checksum.sum` |
| `web_url` | (none) |

For example:

```bash
# 1.x
cernbox -o json ls data | jq -r '.[] | select(.size > 1e9) | .name'
cernbox -o json ls data | jq -r '.[] | select(.is_dir) | .name'
# 2.0
cernbox -o json ls data | jq -r '.[] | select((.size // "0" | tonumber) > 1e9) | .name'
cernbox -o json ls data | jq -r '.[] | select(.type == "RESOURCE_TYPE_CONTAINER") | .name'
```

### Shares: `share list`, `share create`, `share get`

A share is a `cs3.sharing.collaboration.v1beta1.Share`: `id.opaqueId`, `resourceId`, `permissions`, `grantee`,
`owner`, `creator`, `ctime`, `mtime`, `expiration`, `description`. It replaces `{id, role, granted_to, link,
expires_at, path}`:

| 1.x | 2.0 |
|---|---|
| `id` | `id.opaqueId` |
| `role` | `permissions.permissions` (the CS3 permissions; the role is no longer a field) |
| `granted_to.id`, `granted_to.type` | `grantee.userId` or `grantee.groupId`, and `grantee.type` |
| `expires_at` | `expiration.seconds` |
| `path` | `resourceId` (stat it for the path) |

Received shares (`share received list`) are `ReceivedShare`: `share`, `state` (`SHARE_STATE_PENDING`,
`SHARE_STATE_ACCEPTED`, `SHARE_STATE_REJECTED`), `mountPoint`, `hidden`.

Over HTTPS (laptops, and every machine without a gateway), the server does not return some fields, and they are
empty: the grantee's `idp`, the share's `ctime`, `mtime` and `creator`, and the received share's `mountPoint`.
`permissions` is derived from the role.

### Public links: `link list`, `link create`, `link get`

A link is a `cs3.sharing.link.v1beta1.PublicShare`: `id.opaqueId`, `token`, `resourceId`, `permissions`,
`passwordProtected`, `expiration`, `displayName`, `description`. 1.x's `link.url` and `link.has_password` become
`token` (the URL is the endpoint, `/s/`, and the token) and `passwordProtected`.

### App tokens: `token list`, `token create`

An app token is a `cs3.auth.applications.v1beta1.AppPassword`: `password`, `label`, `tokenScope`, `expiration`,
`ctime`, `utime`. **The id that `token revoke` takes is in `password`**: over HTTPS it is the token's client id, not
the secret.

### Trash, spaces, users, versions

- `trash list`: `RecycleItem`, with `key`, `ref.path`, `size`, `deletionTime.seconds` and `type`.
- `space list`, `space info`: `StorageSpace`.
- `user get`, `whoami`: `User`, with `id.opaqueId`, `id.idp`, `username`, `mail`, `displayName`.
- `versions list`: `FileVersion`.

### Unchanged

The statistics of `cp`, `sync` and `archive`, and the report of `doctor` (`.problems` included), keep their 1.x
shape.

### Changed in shape, not CS3

- `version -o json`: `{version, commit, go_version, build_date}`. 1.x had `{version, commit, build_date, go,
  platform}`. `version` is now the version of reva that the build is from; `version --plugins` adds the brand and the
  CERNBox plugins with their versions.
- `du -o json`: `{path, bytes, dir}`. 1.x had `{path, size, listed, unlisted}`.

## 2. Exit codes

| | 1.x | 2.0 |
|---|---|---|
| success | 0 | 0 |
| an error | 1 | 1 |
| a mistake in the command (unknown flag, wrong arguments) | 2 | 2 |
| a credentials problem | 3 | 1 |
| not allowed | 4 | 1 |
| not found | 5 | 1 |
| conflict (lock, etag, quota) | 6 | 1 |

A script that retried after `kinit` on exit code 3, or gave up on 4, cannot tell these apart by the exit code any
more. Read the message on standard error, or check before the call (`cernbox status`, `cernbox stat`).

## 3. Commands and flags

### Names: one set for `cernbox` and `reva`

`cernbox` and `reva` are one program, so they have one set of names. The names are cernbox-cli's, so most of them do
not change for you. For people who used the `reva` CLI or cernboxcop, these are the renames:

| What | `reva` (old) | `cernbox` 1.x | 2.0, both programs |
|---|---|---|---|
| public links | `public-share …` | `link …` | `link` |
| trash | `recycle list\|restore\|purge` (one key) | `trash list\|restore KEY…\|purge` | `trash`, several keys |
| app passwords | `app-password create\|list\|remove <password>` | `token create\|list\|revoke ID` | `token`, by id |
| transfer | `upload`, `download` | `cp SOURCE DEST` (`cb:` marks the remote side) | `cp` (there is no `get` or `put`) |
| sign-in | `login <type>` | `login [--method M]`, `logout`, `status` | `login [--method]`, `logout`, `status` |
| OCM | `ocm invite generate\|forward`, `ocm user find\|remove`, `ocm share …` | `ocm invite create\|list\|accept`, `ocm contacts`, `ocm providers`, `ocm received` | cernbox-cli's verbs; `ocm share …` and `ocm transfer …` stay, for operators |
| output | `-o text\|json` | `-o table\|json\|csv` | `-o table\|json\|csv` |
| server | `--host` | `--endpoint` | `--endpoint` (HTTPS), `--gateway` (gRPC); `--host` is gone |
| token input | `--token-file` | `--token`, `--app-token-file` | `--token`, `--app-token-file` |
| `--insecure` | no TLS on gRPC | plain HTTP allowed | plaintext allowed on the transport in use; `--skip-verify` for TLS without verification |
| completion | cobra's default | own `completion` | cernbox-cli's |
| quiet, debug | none | `-q`, `--debug`, `--no-progress` | `-q`, `--debug`, `--no-progress` |
| space detail | none | `space info ALIAS` | `space info <ref>` |
| JSON | protojson | own structs | CS3 protojson (section 1) |

### What moved for cernbox-cli users

| 1.x | 2.0 |
|---|---|
| `--method M` before any command | `login --method M`. Commands sign in by themselves when the method needs no question (a ticket, an app token) |
| `--config FILE` | `CERNBOX_CONFIG=FILE` |
| `--kerberos-spn SPN` | `login --spn SPN`, `CERNBOX_KERBEROS_SPN`, or `cernbox config set kerberos-spn SPN` |
| `quota` | `space quota get` |
| `share received --accept ID` / `--decline ID` | `share received update ID --state accepted` / `--state rejected`; the list is `share received list` |
| `share create --expiry`, `share update --expiry` | `share update ID --expires DATE` |
| `link create --expiry`, `--name`, `--password` | `link update ID --expires DATE`, `--description`, `--password` |
| `link update --expiry`, `--name`, `--no-expiry` | `--expires DATE`, `--description`, `--expires none` |
| `link password PATH ID`, `--clear` | `link update ID --password`, `--no-password` |
| link role `upload` | `uploader` |
| `sync -r` | gone: `sync` copies folders without it |

New in 2.0: `config view|set`, `lock get|set|remove`, `share get`, `link get`, `space update`, `space quota`,
`user get|find|groups`, `preferences get|set`, `ocm received get|update`, `elevate`, `version --plugins`. Global
flags: `--transport`, `--gateway`, `--admin-host`, `--as`, `--reason`, `--no-elevate`, `-y`/`--yes`. Commands for
operators show in `cernbox --help` only on a host with the admin socket or an admin host, or after `cernbox elevate`;
`cernbox help --all` lists them anyway.

### Environment variables

These keep working: `CERNBOX_CONFIG`, `CERNBOX_ENDPOINT`, `CERNBOX_TOKEN`, `CERNBOX_TOKEN_CACHE`,
`CERNBOX_APP_TOKEN`, `CERNBOX_PASSWORD`, `CERNBOX_USER` and `CERNBOX_USERNAME` (now one setting: the user of an app
token and of basic sign-in).

New: `CERNBOX_TRANSPORT`, `CERNBOX_GATEWAY`, `CERNBOX_ADMIN_HOST`, `CERNBOX_APP_TOKEN_FILE`,
`CERNBOX_KERBEROS_SPN`, `CERNBOX_NO_ELEVATE`, `CERNBOX_OUTPUT`.

Gone: `CERNBOX_AUTH_METHOD`, `CERNBOX_SSO_ISSUER`, `CERNBOX_SSO_CLIENT_ID`, `CERNBOX_JOBS` (use `-j`),
`CERNBOX_EDIT_FOLDER`, `CERNBOX_EDITOR` (use `VISUAL` or `EDITOR`), `CERNBOX_NO_TUI`.

`cernbox` warns once when it sees a `REVA_*` variable: those belong to the `reva` build, and a script written for it
should not reach cernbox.cern.ch by accident.

### Transports

`cernbox` talks HTTPS to `https://cernbox.cern.ch`, as 1.x did. On a CERNBox host whose `/etc/cernbox/config.toml`
sets a `gateway`, it talks CS3 gRPC to that gateway instead. `--transport http|grpc|auto` chooses; `auto` is the
default. `status` shows which one is in use.

## 4. Configuration

The configuration files are TOML:

| 1.x | 2.0 |
|---|---|
| `~/.config/cernbox/config.yaml` | `~/.config/cernbox/config.toml` |
| `/etc/cernbox/config.yaml` | `/etc/cernbox/config.toml` |

- The first run of 2.0 copies your `config.yaml` into `config.toml`, and says so. It reads the YAML file only that
  once. Settings that have no place in the new file are listed, and not copied.
- The site file `/etc/cernbox/config.yaml` is **not** read. The package installs `/etc/cernbox/config.toml`; on a host
  where you changed the old file, move the changes over by hand.
- An unknown key is an error, not ignored, so that a typo shows. `cernbox config view` shows what is in effect.
- The outbox, inbox and editor settings live in `[plugins.<name>]` sections, for example `[[plugins.outbox.folders]]`.

## 5. Token cache

1.x kept tokens in node-local `/tmp`, keyed by endpoint and principal. 2.0 keeps them in the config directory
(`~/.config/cernbox/token`), with mode 0600. The old cache is not read: sign in once after the upgrade (with a
Kerberos ticket, that happens by itself). `CERNBOX_TOKEN_CACHE` still chooses the file. On a machine whose home is
shared between nodes, set it to a node-local path if you do not want the token there.

## 6. For maintainers of scripts and tests

- Every consumer of `-o json` needs the changes of section 1, including cernbox-cli's own integration suite
  (`integration/`), which parses the 1.x JSON throughout.
- Every branch on exit codes 3 to 6 needs section 2.
- The integration suite passes `--method basic` before every command, and 2.0 rejects that with exit code 2. Basic
  sign-in is not in the CERNBox sign-in chain, so a script that uses it runs `cernbox login --method basic` first,
  with the user in `CERNBOX_USERNAME` and the password in `CERNBOX_PASSWORD` or on stdin.

## Not in this build yet

Found on the build of reva 452dc98fd. Each item is either fixed before 2.0 is tagged, or stays in these notes as a
change:

- `rm -r`, `rm -f`, `mkdir -p`, `mv --force`, `touch --no-create`, `logout --all` are not there;
- `ls` takes one path, and has `-l` and `--full` only: no `-a`, `-F`, `-h`, `-1`, `-R`, `-r`, `--sort`;
- `trash list --since|--from|--to`, and `--space` on the `trash` commands, are not there; `trash browse` is not
  there;
- `du --all|--jobs|--versions`, `edit --editor|--force|--in|--interval|--no-watch`, `open --launch|--web`,
  `ocm invite create --recipient`, `share create --with-remote` are not there;
- `apps`, `copy`, `paste`, `clipboard`, `inbox` and `outbox` are not there (the CERNBox plugins add the last four);
- a `config.yaml` with an outbox is copied into a `config.toml` that 2.0 then refuses (`unknown key
  plugins.outbox.folders.after`), so every command fails until the file is fixed.
