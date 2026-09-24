# cernbox-cli

Work with your CERNBox files from the command line: browse them, move them between your computer and CERNBox, move them between two computers, and share them.

```console
$ cernbox ls /eos/user/g/gdelmont
$ cernbox put ./report.pdf /eos/user/g/gdelmont/Documents/
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

## Signing in

Usually nothing: the client finds your credentials and uses them. On a machine where you already have a CERN ticket it signs in silently. Elsewhere it prints a link to open and a code to enter, once, and remembers the session afterwards.

```bash
cernbox status     # how you are signed in, and to which server
cernbox login      # sign in now, rather than on the next command
cernbox logout
```

For scripts and scheduled jobs, use an app token: create one in the CERNBox web interface and put it in `CERNBOX_APP_TOKEN`. `cernbox token list` and `cernbox token revoke` manage the ones you have.

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
```

`get` and `put` need no marker, because your computer always comes first:

```bash
cernbox put ./report.pdf /eos/user/g/gdelmont/Documents/
cernbox get /eos/user/g/gdelmont/Documents/report.pdf .
```

## Browsing

```bash
cernbox ls -l /eos/user/g/gdelmont     # long listing
cernbox ls -lt                          # newest first
cernbox stat report.pdf                 # everything about one file
cernbox find . --name report            # search by name
cernbox du -h -d 2 data                 # what is taking up space
cernbox cat notes.txt
```

`mkdir`, `touch`, `rm` and `mv` work as you would expect. They follow the flags you already type — `-p`, `-r`, `-f` — and are a little more careful than the local versions: `mv` and `cp` will not overwrite without `-f`, and `touch` will not empty a file that already exists.

## Copying and mirroring

```bash
cernbox get -r /eos/project/c/cernbox/data ./data    # a whole directory
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

A share can be changed or withdrawn afterwards with `share update` and `share remove`. Public links work the same way, and a link keeps its address when you change it, so anybody already holding it is unaffected:

```bash
cernbox link create report.pdf --expiry 2026-12-31
cernbox link update report.pdf LINK_ID --role viewer
cernbox link password report.pdf LINK_ID
```

To share with someone at another institution, exchange an invitation once and then share as usual:

```bash
cernbox ocm invite create --recipient alice@other-lab.org
cernbox ocm contacts
cernbox share create data --with-remote alice@other-lab.org
```

## Undoing things

```bash
cernbox trash list                    # what you have deleted
cernbox trash restore KEY
cernbox versions list report.pdf      # earlier versions of a file
cernbox versions restore report.pdf VERSION
```

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

- [docs/behaviour.md](docs/behaviour.md) — what the client does when the server is awkward, and what each kind of transfer actually costs
- [docs/design.md](docs/design.md) — the architecture and the decisions behind it

## Licence

Apache 2.0. See [LICENSE](LICENSE).
