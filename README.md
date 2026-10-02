# cernbox-cli

Work with your CERNBox files from the command line: browse them, move them between your computer and CERNBox, move them between two computers, and share them.

```console
$ cernbox ls /eos/user/g/gdelmont
$ cernbox cp ./report.pdf cb:/eos/user/g/gdelmont/Documents/
$ cernbox share create /eos/user/g/gdelmont/Documents --with marie --role editor
```

## Install

```bash
curl cli.cernbox.cern.ch | sh
```

That fetches the release built for your system, checks it against the published checksum, and installs it where you can write — `/usr/local/bin` if that is yours, otherwise `~/.local/bin`, telling you what to add to `PATH`. It never asks for a password. `CERNBOX_VERSION` pins a version and `CERNBOX_INSTALL_DIR` chooses where it goes.

From source, or from a package:

```bash
go install github.com/cernbox/cernbox-cli/cmd/cernbox@latest
```

RPM and deb packages are attached to each release.

On Windows, download `cernbox-cli_<version>_windows_amd64.zip` (or `_arm64`) from the [latest release](https://github.com/cernbox/cernbox-cli/releases/latest) and put `cernbox.exe` somewhere on your `PATH`. It runs in PowerShell, `cmd` and Windows Terminal alike.

## Signing in

Usually nothing: the client finds your credentials and uses them. On a machine where you already have a CERN ticket it signs in silently. Elsewhere it prints a link to open and a code to enter, once, and remembers the session afterwards.

```bash
cernbox status     # how you are signed in, and to which server
cernbox login      # sign in now, rather than on the next command
cernbox logout
```

For scripts and scheduled jobs, use an app token: create one in the CERNBox web interface and put it in `CERNBOX_APP_TOKEN`. `cernbox token list` and `cernbox token revoke` manage the ones you have.

## When something does not work

```bash
cernbox status     # what is configured: server, user, credential, token cache
cernbox doctor     # what actually works, and what to do about what does not
```

`doctor` tries things rather than reporting settings: the server answers, the two clocks agree, the credential is accepted and would still work from a scheduled job, a file can actually be written, the recycle bin can be listed, the configured folders and the editor are there. Every line that is not a pass names the next step, and the output is meant to be pasted into a support request: it names providers, expiry times and paths, and never a token or a password.

Checks stop at the first failure they depend on, so a server nobody can reach is reported once rather than as ten mysterious failures. Only a problem makes it exit non-zero — a warning never does, so `cernbox doctor` works as a health check, and `--output json` gives you `.problems` to watch.

It writes one small file in your home space and removes it again, which is the only thing it changes. `--skip-write` leaves that out.

## Paths

Paths look like the ones you already use:

```bash
cernbox ls /eos/user/g/gdelmont/Documents
cernbox ls /eos/project/c/cernbox/data
cernbox ls home:Documents            # your own space, by name
```

Commands that copy between your computer and CERNBox are the exception: there you mark the CERNBox side with `cb:`, because the same path can exist on both.

```bash
cernbox cp ./report.pdf cb:/eos/user/g/gdelmont/Documents/
cernbox cp cb:/eos/user/g/gdelmont/Documents/report.pdf .
```

## Browsing

```bash
cernbox ls -l /eos/user/g/gdelmont     # long listing
cernbox ls -lt                          # newest first
cernbox stat report.pdf                 # everything about one file
cernbox find . --name report            # search by name
cernbox find . --name '*.root' --size +1G   # and by size, age or kind
cernbox du -h -d 2 data                 # what is taking up space
cernbox du -h --top 20                  # the 20 biggest things you have
cernbox du -h --versions                # how much of that is old versions
cernbox cat notes.txt
```

If a quota looks bigger than the files you can see, it usually is: earlier versions of a file are charged to you but appear in no listing. `du --versions` splits each size into the part listings account for and the part they do not, which is almost always that history. [docs/behaviour.md](docs/behaviour.md) explains how it is measured.

`find` takes more than a name. `--name` matches a glob when it contains `*` or `?` and plain text anywhere in the name otherwise; `--size +1G` and `-10M` mean larger and smaller; `--newer 7d` and `--older 30d` take a span or a date; `--type f` and `--type d` narrow to files or directories. `--print0` writes bare paths for `xargs -0`.

`mkdir`, `touch`, `rm` and `mv` work as you would expect. They follow the flags you already type — `-p`, `-r`, `-f` — and are a little more careful than the local versions: `mv` and `cp` will not overwrite without `-f`, and `touch` will not empty a file that already exists.

## Following a file that is still being written

```bash
cernbox tail job.log              # the last ten lines
cernbox tail -f -n 50 job.log     # and keep printing what gets appended
cernbox tail -f --timeout 1h job.log
```

This is for the log a batch job is writing into CERNBox: only the bytes that were appended are transferred, so following a large log costs almost nothing. Following polls, because the server has nothing to announce a change with, and `--interval` sets how often.

**It needs the storage to serve byte ranges, and the EOS behind this instance currently does not** — it answers a ranged request with chunked framing inside a body it has already given a length, so the bytes are not the file's. The client refuses that rather than print the wrong thing, and says so. The same gap silently corrupted a resumed `cernbox cp` download until it was caught; see [docs/behaviour.md](docs/behaviour.md).

## How much space you have

```bash
cernbox quota                        # your own space
cernbox quota project/cernbox        # any space you can reach
cernbox quota --all                  # every one of them
cernbox quota --versions             # split into files and their old versions
```

A project's quota is the project's, shared by everyone in it. If the number looks larger than anything you can find, `--versions` is usually the answer: earlier copies of files are charged to you and appear in no listing. Anything the space's own directory does not explain is reported as its own line rather than folded away.

## Editing a file in place

```bash
cernbox edit notes.txt
```

That opens your editor and writes the file back **every time you save**, so the copy in CERNBox keeps up with the one in front of you and closing the editor is not a special moment.

It works on both sides. A CERNBox file is fetched, edited, and written back. A file on **your own machine** is edited where it lies — nothing is downloaded over it — and uploaded on each save, which makes this a way to keep a local file mirrored into CERNBox while you work on it.

```bash
cernbox edit notes.txt               # CERNBox, in your myfiles folder
cernbox edit ./draft.md              # this machine, saved to myfiles/draft.md
cernbox edit Documents/report.md     # a CERNBox path, taken as it is
cernbox edit todo.md --in Scratch    # a different folder
cernbox edit notes.txt --no-watch    # save once, when the editor closes
```

Which side an argument means: a bare name is CERNBox, in your `myfiles` folder; `./x`, `../x` and `~/x` are this machine; `file:` and `cb:` say so outright; and any other path is CERNBox if it is there and this machine otherwise — which is how `/eos/user/...` resolves sensibly on lxplus, where it is both. Whichever way it goes, the paths are printed before the editor opens.

The editor comes from `--editor`, then `CERNBOX_EDITOR`, then `VISUAL`, then `EDITOR`. `edit.folder` in the configuration file, or `CERNBOX_EDIT_FOLDER`, changes where bare names go.

If somebody else changes the file while you have it open, your save is refused rather than allowed to overwrite theirs, and your version is kept on disk with its path printed so nothing is lost. Sending a local file to a name CERNBox already uses for *different* content is refused too, the way `cp` refuses to overwrite. `--force` says yours should win.

## A folder that uploads itself

Point an outbox at a local folder and whatever appears in it goes to CERNBox — a screenshots folder, a scratch directory, anywhere you drop things.

```bash
cernbox outbox push                  # upload what is waiting, then stop
cernbox outbox watch                 # keep going as files appear
cernbox outbox status                # what is waiting, and what it is waiting on
```

Folders usually live in the configuration file, so those commands need no arguments:

```yaml
outbox:
  - local: ~/Pictures/Screenshots
    remote: Screenshots
    layout: date
    after: delete
    link: true
  - local: ~/scratch
    remote: Scratch
```

| Key | Default | What it does |
| --- | --- | --- |
| `local` | required | the folder to upload from; a leading `~` is expanded |
| `remote` | required | the CERNBox folder it goes to, created if it is not there |
| `layout` | `flat` | `date` files each upload under `2026/09/30/`, taken from the file's own timestamp rather than today's |
| `after` | `keep` | `keep` leaves the file alone, `move` puts it in `.uploaded/` beside it, `delete` removes it |
| `link` | `false` | make a public link for each upload and print it |

`exec` runs a command over each file **before** it is uploaded, with the file's path as its last argument — strip a screenshot's metadata, shrink it, convert it:

```yaml
outbox:
  - local: ~/Pictures/Screenshots
    remote: Screenshots
    exec: ~/bin/strip-exif
```

The hook may rewrite the file where it is, and what goes up is what it left there. A file it **renames** is not followed, and that is reported rather than silently skipped. If the hook fails, or outlives `--exec-timeout`, nothing is uploaded and nothing is tidied away — not even under `after: delete`.

Rewriting in place is what keeps this stable: the next pass scans the rewritten file, finds its size equal to the uploaded one and skips it, so a shrinking hook does not shrink the same file again on every run. The same `--exec` exists on `inbox` for the other direction.

`layout`, `after` and `link` are flags too, and a flag overrides the configuration for that one run — useful as a safety net over a config that says `delete`. `--to` names the CERNBox folder for a local folder that is not in the configuration at all:

```bash
cernbox outbox push --after keep                       # ignore what the config says, this once
cernbox outbox push ~/scratch --to Scratch             # a folder not in the configuration
cernbox outbox push ~/Desktop --to Inbox --layout date --link
```

`--settle` sets how long a file must be unchanged before it counts as finished, and `watch` takes `--sweep` for how often it looks through the folders anyway. With `link: true` each URL goes to standard output as its upload finishes, so piping into `xclip` or `pbcopy` gives you a link ready to paste.

It is one way, always: nothing in CERNBox is changed except by adding to it, and `sync` is the command for keeping two sides matching. A few more rules, all pointed the same way:

- Nothing is uploaded until a file has stopped changing, so a screenshot still being written is left for next time rather than arriving half finished.
- `delete` removes the local copy only after the server confirms a matching checksum.
- A name already taken in CERNBox is never overwritten — the upload goes alongside it as `name (2).png`.
- Subdirectories are skipped, and you are told they were.

[docs/configuration.md](docs/configuration.md) has the full reference, including where the configuration file lives.

## A folder that collects itself

The inbox is the outbox the other way round: a CERNBox folder whose arrivals come down to a local one. It pairs with an upload link — somebody drops a file in without an account and it turns up on your laptop — and with a project folder collaborators write into.

```bash
cernbox inbox pull                   # download what is waiting, then stop
cernbox inbox watch                  # keep going as things appear
cernbox inbox status                 # what is waiting, and what it is waiting on
```

```yaml
inbox:
  - remote: /eos/project/c/cernbox/incoming
    local: ~/from-cernbox
    layout: date
    after: move
```

The keys mean what the outbox's mean, with `remote` and `local` the other way about: `layout` `flat` or `date`, and `after` `keep`, `move` to `.collected/` **in CERNBox**, or `delete`. Nothing local is ever written over — a name already taken by a different file gets a `(2)` — and `keep` is the default because it is the only choice that cannot lose anything.

`--exec` runs a command for each file collected, with the file's local path as its last argument and the details in `CERNBOX_INBOX_FILE`, `CERNBOX_INBOX_NAME`, `CERNBOX_INBOX_SIZE` and `CERNBOX_INBOX_REMOTE`:

```yaml
inbox:
  - remote: /eos/project/c/cernbox/incoming
    local: ~/from-cernbox
    after: move
    exec: /usr/local/bin/ingest
```

The command is split on spaces and never handed to a shell, so a file name cannot be interpreted — which matters here more than anywhere else, because the names come from whoever is putting things in the folder. Something needing shell features goes in a script.

If the hook fails, or does not finish within `--exec-timeout`, the `after` policy does not run: the CERNBox copy stays in the inbox, which is what tells you something is unfinished. With `move` or `delete` that also makes the next pass retry it, because a file still in the inbox is a file whose hook did not complete. With `keep` there is no such evidence, so the hook only ever runs on a fresh download.

Two differences from the outbox worth knowing. `inbox watch` **asks on a timer**, because nothing on this surface will tell a client that a remote folder changed, so `--interval` is a direct cost: one listing per folder per pass. And whether a file has finished arriving is judged against **the server's clock**, since the server wrote the timestamp being compared; `cernbox doctor` is what tells you if the two clocks disagree.

## Copying and mirroring

```bash
cernbox cp -r cb:/eos/project/c/cernbox/data ./data  # a whole directory
cernbox sync ./data cb:/eos/project/c/cernbox/data   # make the far side match
cernbox archive /eos/project/c/cernbox/data          # download it as one .tar
```

`sync` is one way: it copies what changed and leaves the rest alone. `--delete` also removes what the source no longer has, `--exclude` leaves things out, and `--dry-run` shows the whole plan without doing any of it — worth running first when `--delete` is involved.

`archive` asks the server to pack a directory and sends it as a single file, which is much faster than fetching thousands of small ones. `--format zip` and `--to -` (straight into another program) both work.

## Moving files between computers

A clipboard. Copy on one computer, paste on another:

```bash
# on one computer
cernbox copy ./report.pdf

# on another
cernbox paste
```

Pasting does not empty the clipboard, so the same copy reaches as many computers as you like. `cernbox clipboard list` shows what is on it and `cernbox clipboard clear` lets go of it.

It works with pipes, which makes it a pipe between two computers:

```bash
tar cz ./analysis | cernbox copy - --name analysis.tgz
cernbox paste - | tar xz
```

`--slot NAME` keeps several copies in flight at once. `--stream` stores nothing at all: the sending command waits, and the file moves only once you paste on the other side.

### Sending something to somebody else

The same clipboard, between two people:

```bash
# you
cernbox copy ./plots.tar --to marie

# marie, on her own account
cernbox clipboard list                    # shows what is waiting, and from whom
cernbox paste --from gdelmont ./incoming/
```

It stays on your quota until `cernbox clipboard clear to-marie`, which is what ends it. `--stream --to` works too, and then nothing is stored anywhere.

## Sharing

```bash
cernbox share create /eos/user/g/gdelmont/Documents --with marie --role editor
cernbox share list                        # everything you have shared
cernbox share received                    # what others have shared with you
```

Sharing a directory shares what is inside it, so "who can see this file?" cannot be answered by looking at the file. `share audit` answers it properly:

```bash
cernbox share audit Documents/2026/report.pdf
```

It walks from the space root down to the path and reports every grant it finds, saying which directory each one came from and marking the ones inherited from above. Unlike `share list`, which shows what *you* shared, this shows every permission the server reports on those directories, whoever made it — and for a project it reminds you that everyone with access to the space can reach it too, share or no share.

A share can be changed or withdrawn afterwards with `share update` and `share remove`. Public links work the same way, and a link keeps its address when you change it, so anybody already holding it is unaffected:

```bash
cernbox link create report.pdf --expiry 2026-12-31
cernbox link update report.pdf LINK_ID --role viewer
cernbox link password report.pdf LINK_ID
```

`--role upload` makes the opposite of a link you hand out: a folder people can put files into without an account, and without being able to read what is already there. It is how you collect something from somebody who has no CERN account at all.

```bash
cernbox link create incoming --role upload --expiry 2026-12-31
```

Give it a folder of its own. An upload link carries permission to list the folder, so do not point it at a directory whose file names you would rather not show. Uploads get a suffix added to their name, so nothing already there can be written over, and the sender does not choose the final name.

To share with someone at another institution, exchange an invitation once and then share as usual:

```bash
cernbox ocm invite create --recipient alice@other-lab.org
cernbox ocm contacts
cernbox share create data --with-remote alice@other-lab.org
```

## Undoing things

```bash
cernbox trash browse                  # walk through what you deleted, and pick
cernbox trash list                    # what you deleted in the last two days
cernbox trash list --since 30d        # further back
cernbox trash restore KEY
cernbox versions list report.pdf      # earlier versions of a file
cernbox versions diff report.md       # what changed since the last save
cernbox versions restore report.pdf VERSION
```

`cernbox trash browse` is usually the one you want. Deleted files remember where they lived, so it shows the bin as the folders it came from: walk into them, pick what you want with SPACE, and press `r` to bring it back. Nothing moves until you confirm, and you never have to copy a key. `cernbox trash restore` with nothing after it opens the same thing.

`versions diff` shows a unified diff: with no version, the most recent one against the file as it is; with one, that version against now; with two, one against the other. `-U` sets how much surrounding context to show, and a file that is not text is refused rather than printed as noise.

A trash listing covers a stretch of time rather than the whole bin, because that is what the server answers, and two days is as far back as it goes unless asked. `--since 30d`, or `--from` and `--to` for a particular period, look further; the listing always says which period it searched, so "nothing there" is never mistaken for "nothing there at all".

## In scripts

`--output json` and `--output csv` turn any listing into something a program can read, and informational messages go to standard error so a pipe sees only data.

```bash
cernbox --output json ls data | jq -r '.[] | select(.size > 1e9) | .name'
```

Exit codes distinguish the cases worth branching on: `0` success, `2` a mistake in the command, `3` a credentials problem, `4` not allowed, `5` not found.

## Shell completion

```bash
source <(cernbox completion bash)     # or zsh, or fish
```

TAB then completes CERNBox paths as you type them, along with space names, share ids and the other things nobody remembers.

## Development

```bash
make build            # build
make test             # unit tests
make dev-up           # start a local CERNBox to test against
make test-integration # run the tests that need it
```

`make help` lists the rest.

## More

- [docs/configuration.md](docs/configuration.md) — the configuration file: where it goes, every setting, and the environment variables
- [docs/behaviour.md](docs/behaviour.md) — what the client does when the server is awkward, and what each kind of transfer actually costs
- [docs/design.md](docs/design.md) — the architecture and the decisions behind it

## Licence

Apache 2.0. See [LICENSE](LICENSE).
