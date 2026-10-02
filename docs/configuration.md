# Configuration

Every setting has a default that works against production CERNBox, so the CLI is useful with no configuration at all. A file is for the things you would otherwise retype: an outbox folder, a different editor, a slower link.

## Where the file goes

```
~/.config/cernbox/config.yaml
```

Precisely: `$XDG_CONFIG_HOME/cernbox/config.yaml`, which is `~/.config/cernbox/config.yaml` unless you set that variable, and the equivalent per-user location on macOS. On Windows it is `%AppData%\cernbox\config.yaml`. Create it yourself; nothing writes it for you.

A `cernbox.cfg` sitting in the same directory belongs to the desktop sync client and is ignored by this one.

Two other places are read:

| Where | When |
| --- | --- |
| `/etc/cernbox/config.yaml` (`%ProgramData%\cernbox\config.yaml` on Windows) | always, first, for deployment-wide settings |
| `~/.config/cernbox/config.yaml` | always, second, overriding the site file |
| `$CERNBOX_CONFIG` | **instead of** the user file — the site file is still read |
| `--config PATH` | **instead of both** |

Two differences between the last two are easy to trip over. `--config` requires the file to exist and fails if it does not; `CERNBOX_CONFIG` pointing at a file that is not there is silently ignored, so a typo there looks like configuration that has no effect. And a file that exists but is **malformed is always an error**, at every level, because silently discarding it would leave you wondering why a setting did nothing.

## What wins

```
built-in defaults  →  /etc file  →  user file  →  CERNBOX_* variables  →  command-line flags
```

Later beats earlier, per setting. A flag always wins, which is what makes `--after keep` usable as a safety net over a configuration that says `delete`.

## A complete file

Every key is optional. This shows them all with their defaults, so anything you delete simply reverts.

```yaml
# The server. Rarely worth changing outside development.
endpoint: https://cernbox.cern.ch
insecure: false            # allow plain HTTP; development only, and warned about on every use

auth:
  method: ""               # pin one of: kerberos, device, app-token, basic, token
  token_cache: ""          # where sessions are remembered between commands
  kerberos:
    service_principal: ""  # when the endpoint is a DNS alias with a different keytab entry
    path: /graph/v1.0/me   # the endpoint that accepts a SPNEGO token
    ccache: ""             # override the Kerberos credential cache
  sso:
    issuer: https://auth.cern.ch/auth/realms/cern
    client_id: cernbox-cli
    scopes: []

transfer:
  jobs: 0                  # files at once; 0 means one per core, up to 8
  chunk_size: 8M           # resumable upload chunk
  verify: false            # checksum every transfer, not just the ones that need it
  archive: true            # let recursive downloads use the server's archiver

edit:
  folder: myfiles          # where "cernbox edit notes.txt" puts a bare name
  command: ""              # editor for this CLI only, ahead of VISUAL and EDITOR

# Local folders whose contents are uploaded. See below.
outbox:
  - local: ~/Pictures/Screenshots
    remote: Screenshots
    layout: date
    after: delete
    link: true
  - local: ~/scratch
    remote: Scratch

# CERNBox folders whose contents are downloaded. See below.
inbox:
  - remote: /eos/project/c/cernbox/incoming
    local: ~/from-cernbox
    layout: date
    after: move
```

## The outbox

This is the one section with no equivalent flag or variable for the whole list: `cernbox outbox push` with no arguments works only from a file. A single folder can still be given on the command line with `--to`.

| Key | Default | Meaning |
| --- | --- | --- |
| `local` | required | the folder to upload from; a leading `~` is expanded |
| `remote` | required | the CERNBox folder it goes to |
| `layout` | `flat` | `date` files each upload under `YYYY/MM/DD`, taken from the file's own timestamp |
| `after` | `keep` | `keep`, `move` to `.uploaded/`, or `delete` once the upload is verified |
| `link` | `false` | create a public link for each upload and print it |
| `exec` | none | command to run over each file before it is uploaded, with its path as the last argument |

`after: keep` is the default because it is the only choice that cannot lose anything. `delete` is the one irreversible option, so it belongs in a file where you wrote it down deliberately rather than in a flag you might repeat from shell history.

`exec` runs before the upload and may rewrite the file where it is; the bytes that go up are the ones it left. A file it renames is not followed — the upload is of the path the outbox saw, and an empty path is reported. A hook that fails, or that outlives `--exec-timeout` (five minutes by default), stops both the upload and the `after` policy, so the file stays in the folder.

Why it runs after the already-uploaded check rather than before: a hook that shrinks a file must not be run again on every pass. What prevents that is the in-place rule — the next pass scans the rewritten file, finds its size equal to the uploaded one and skips it before the hook is reached. `CERNBOX_OUTBOX_FILE`, `CERNBOX_OUTBOX_NAME`, `CERNBOX_OUTBOX_SIZE` and `CERNBOX_OUTBOX_REMOTE` are in the environment, and the size is the one before the hook ran.

A configured folder that does not exist is a warning, not a failure — an unmounted drive or a folder you have not created yet must not stop the others from uploading, least of all from a timer where nobody is watching. A folder named on the command line is treated as a typo and fails.

## The inbox

The same shape as the outbox, with the two sides swapped, and the same rule about the list living only in a file.

| Key | Default | Meaning |
| --- | --- | --- |
| `remote` | required | the CERNBox folder to collect from |
| `local` | required | where its contents go; a leading `~` is expanded |
| `layout` | `flat` | `date` files each arrival under `YYYY/MM/DD`, taken from the file's own timestamp |
| `after` | `keep` | `keep`, `move` to `.collected/` in CERNBox, or `delete` once the download is verified |
| `exec` | none | command to run for each file collected, with its local path as the last argument |

Two things behave differently from the outbox, and neither is cosmetic.

`inbox watch` polls. The outbox is told when a local folder changes, by the filesystem; nothing here will tell a client that a remote folder did. So `--interval` (default one minute) is a direct cost of one listing per folder per pass, and for anything that has to survive sleep, a lost network and a reboot, `inbox pull` from a timer is the better shape.

`exec` is split on spaces and started directly, never through a shell: the names come from whoever is putting things in the folder, which with an upload link is a stranger. The file's path arrives as the last argument, and `CERNBOX_INBOX_FILE`, `CERNBOX_INBOX_NAME`, `CERNBOX_INBOX_SIZE` and `CERNBOX_INBOX_REMOTE` are in the environment. Both of the hook's output streams go to standard error, so a hook that prints cannot land in the middle of `--output json`.

A hook that fails, or that outlives `--exec-timeout` (five minutes by default), stops the `after` policy for that file. That ordering is the point: if the hook is why the inbox exists, moving the CERNBox copy out of the way after it failed throws away the only evidence that something did not finish. With `move` or `delete` it also gives you a retry, since a file still sitting in the inbox is one whose hook did not complete; with `keep` there is nothing to tell that from, so the hook runs only when a file is newly downloaded.

Whether a file has finished arriving is judged against the **server's** clock. A file still being uploaded keeps its modification time current, so one listing is enough to tell it from a finished one — but that compares a timestamp the server wrote against a now, and taking now from this machine would make the test wrong by however far the two clocks are apart. If the clock cannot be read the local one is used and a warning says so; `cernbox doctor` reports the drift.

## Environment variables

Useful in a shell, a script, or a service unit. They sit between the file and the flags.

| Variable | Replaces |
| --- | --- |
| `CERNBOX_CONFIG` | the user configuration file |
| `CERNBOX_ENDPOINT` | `endpoint` |
| `CERNBOX_AUTH_METHOD` | `auth.method` |
| `CERNBOX_TOKEN_CACHE` | `auth.token_cache` |
| `CERNBOX_SSO_ISSUER` | `auth.sso.issuer` |
| `CERNBOX_SSO_CLIENT_ID` | `auth.sso.client_id` |
| `CERNBOX_JOBS` | `transfer.jobs` |
| `CERNBOX_EDIT_FOLDER` | `edit.folder` |
| `CERNBOX_EDITOR` | `edit.command` |

And these carry credentials rather than settings, so they have no place in a file:

| Variable | Used by |
| --- | --- |
| `CERNBOX_APP_TOKEN` | an app token, which is what a script or a service should use |
| `CERNBOX_USER` | the account an **app token** belongs to |
| `CERNBOX_USERNAME` | the account for **basic** sign-in — a different variable from the one above, confusingly |
| `CERNBOX_PASSWORD` | the password for basic sign-in; also what makes basic count as unattended |
| `CERNBOX_TOKEN` | a bearer token to use as it is |

One more, which is not configuration so much as an escape hatch: `CERNBOX_NO_TUI` stops `trash browse` taking over the terminal, the same as `--plain`.

## Running unattended

A timer or a service has no terminal, so the sign-in that prints a URL and waits cannot work there. Give it an app token:

```bash
CERNBOX_APP_TOKEN=… cernbox outbox push
```

Kerberos also works silently where a ticket is present. `cernbox outbox watch` checks for one of these before it starts, rather than beginning cheerfully and failing an hour later on the first upload.
