# How the client behaves

Notes on behaviour that the README deliberately leaves out: what the client does
when the server is awkward, what a transfer actually costs, and why a few
commands are stricter than the tools they imitate. The README says what the
commands do; this says how, and why it was built that way.

For the architecture and the decisions behind it, see [design.md](design.md).

## Server-side gaps this client works around

Running the CLI against a real reva turned up several endpoints that exist but do nothing. Each is handled deliberately rather than left to fail:

| Gap | What the CLI does |
| --- | --- |
| `LOCK` returns a hardcoded token and records nothing; `UNLOCK` returns 501 | No `lock` command at all. One built on this would report success and lock nothing |
| `search-files` REPORT is a stub returning 501 | `find` asks the server first, then falls back to walking the tree client-side |
| OCS `remote_shares` is an empty handler that writes nothing | `ocm received` reads the graph `sharedWithMe` endpoint, filtering on the OCM id prefix |
| App-token creation is not exposed publicly | `token create` explains where to create one; `list` and `revoke` work normally |
| Reva's demo app provider advertises no mime types, so nothing can open anything | `open --web` works regardless; the application link needs a real provider such as Collabora |
| `If-None-Match: *` on PUT is ignored, so a "create only" write silently overwrites | `touch` checks for an existing path before writing, rather than trusting the precondition. `If-Match` *is* honoured, and the clipboard uses it |
| No upload accepts a body of unknown length: `PUT` requires `Content-Length`, and TUS does not offer `creation-defer-length` | `copy -` splits a stream into known-length pieces rather than spooling it to disk to measure it |
| Downloading a directory answers 501 | `cat` reports "is a directory", as `cat(1)` does |
| A recycle-bin listing with no date range silently means "the last two days", and a range the storage thinks is too wide is refused outright | `trash list` always sends a range, reports which one, and splits a refused one — see [the trash bin covers a period](#the-trash-bin-covers-a-period-not-a-bin) |
| `EmptyRecycle` is not implemented, so emptying a bin in one request is an internal error | No `trash purge --all`. Purging takes the keys from `trash list` |
| A restore ignores the destination it is given: EOS restores to the path it recorded, while ocdav deletes whatever is at the requested destination first and then reports failure when the file does not arrive there | No `trash restore --to`. A restore goes back where it came from, which is all the storage can do |
| Nothing in the recycle-bin API filters, pages or drills down: the listing takes only a date range, `ListRecycle` ignores the key and relative path it is passed, and there is no cursor within a day | `trash browse` rebuilds the tree client-side from the paths the entries carry — see [browsing the bin](#browsing-the-bin) |

**Two-way sync** is a deliberate omission rather than a gap. `sync` is a one-way mirror: genuine bidirectional synchronisation needs persistent per-file state to tell "changed here" from "deleted there", and without it the two are indistinguishable, which is how a sync tool deletes data it should have uploaded. That state is the desktop client's job.

## Why transfer commands need `cb:`

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

## Listing

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

## Disk usage

`du` prints what `du(1)` prints: a size, a tab, a path, with a directory reported after everything it contains.

```console
$ cernbox du -h -d 2 /eos/user/g/gdelmont/data
2.9K	/eos/user/g/gdelmont/data/raw/2026
2.9K	/eos/user/g/gdelmont/data/raw
5.9K	/eos/user/g/gdelmont/data
```

`--top N` answers the other question people have. `du` tells you how big something is; `--top` tells you **what is using the space**, which is what you want when a quota is full. It ranks by size, biggest first, and prints only the N largest.

```console
$ cernbox du -h --top 4 data
3.0M	data/deep
3.0M	data/deep/deeper
3.0M	data/deep/deeper/big.bin
20.0K	data/small.txt
```

Three deliberate differences from plain `du`, all because the useful answer is rarely at the top level. It walks the whole tree rather than stopping at the depth `du` defaults to, so a large file buried deep is found — pass `-d` to bound it again. It counts files as well as directories, since a single file is usually the answer. And it leaves out the argument's own total, which is the biggest entry by definition and is what plain `du` already prints. A directory and the file inside it both appear, as they do in `du -a | sort -n`, which is what shows you whether one file or many is responsible.

The cost follows from the walk: one listing per directory, where plain `du` is a single request. Ties break on the path, so two equal sizes do not swap places between runs and two listings can be compared.

### Where the quota went

The commonest confusion about space is that a quota is much larger than the files anybody can find. It is not a mistake in the accounting: **earlier versions of a file are charged to you and appear in no listing.** EOS keeps them in a `.sys.v#.<name>/` directory next to the file, and reva filters those out of every listing — `hiddenReg` is `\.sys\..#.`, applied unless the server sets `show_hidden_sys_files`, which is off by default. The container total counts them; the listing does not.

`du --versions` measures the difference:

```console
$ cernbox du -h --versions -d 1 ver-check
CHARGED	LISTED	UNLISTED	PATH
6.0M	2.0M	4.0M	ver-check/deep
12.0M	4.0M	8.0M	ver-check
```

Those numbers are from a real instance: two 2M files, each written three times. EOS reported 12,582,912 for the tree with 4,194,304 in each of the two version directories, which is what the `UNLISTED` column adds up to.

Nothing reads a version directory to do this — nothing can. The measurement is a subtraction between two numbers that come from different places: the recursive total the server reports for a container, which counts the hidden entries, and the sum of the entries a listing returns, which does not. Per directory it is local arithmetic, since a subdirectory's reported size is already recursive. The figures are then **rolled up**, so a directory reports the hidden bytes of its whole subtree — which is why `ver-check` shows 8M including the 4M that lives one level further down, and why the flag walks the whole tree even when printing one level.

Three limits worth stating plainly:

- **It measures hidden bytes, not provably versions.** The same filter hides `.sys.a#.` attribute files, and at a space root reva additionally drops anything whose name begins with a dot. Versions are the usual and usually the entire cause; `cernbox versions list FILE` is what turns the inference into a fact for one file.
- **Version bytes are attributed to the directory that holds the file**, not to the file, because that is where EOS puts them. A file's row always reads `UNLISTED 0`.
- **Versions cannot be deleted through the API.** CS3 has no such call and reva's versions handler answers `501` to anything but list, restore, head and download — its own comment says `cs3api has no delete file version call`. The space comes back when the file itself is deleted and then purged from the trash, which discards its version directory too.

One more thing that misleads people: EOS maintains container totals and quota **asynchronously**. Straight after an upload, `du` can read `0` and the quota can read the old figure, for ten seconds or more. Nothing is wrong; the number has not caught up.

`-h`, `-s`, `-a` and `-d`/`--max-depth` carry their usual meanings. One deliberate difference: only the total for each argument is reported unless `--max-depth` asks for more — that is `du -s` rather than `du`'s own default, because descending a whole tree here costs one request per directory, and the totals CERNBox reports are already recursive. Sizes are apparent bytes, not disk blocks, which the server does not report.

## The trash bin covers a period, not a bin

A recycle-bin listing is a query over a span of deletion times, and the server picks the span when the client does not. Left to itself it reaches **two days** back. That is the whole explanation for the commonest complaint about CERNBox trash — a file deleted last week is simply not in the answer, and nothing in the answer says so.

So `trash list` always sends a range, and always reports the one it sent:

```console
$ cernbox trash list
KEY      TYPE  SIZE   DELETED      ORIGINAL PATH
a1b2c3   file  1.2K   2 hours ago  Documents/notes.txt
1 file deleted in the last 2 days. Look further back with 'trash list --since 30d'.
```

The footer is on standard error, so it informs a person without reaching a pipe.

Three server limits shape how a wider range is asked for, all of them in the EOS storage driver:

| Limit | Default | What happens |
| --- | --- | --- |
| No range given | last 2 days | A silent choice, not an error |
| `max_days_in_recycle_list` | 14 | A wider range is refused outright, 400 |
| `max_recycle_entries` | 2000 | Too many deletions in range refuses the **whole** listing, not the surplus |

A refusal of either kind is answered by halving the range and asking again, down to single days. This costs one request in the ordinary case, needs no knowledge of a deployment's configured limits — which is the point, since they are a deployment's to change — and turns the 2000-entry wall into a per-day one. It also costs the server nothing extra: the driver already walks the range a day at a time internally, one call per day either way.

A single day that still cannot be listed becomes a **gap**, reported as a warning rather than swallowed. The rest of the window is worth showing, but a listing that quietly omits a day is worse than one that fails, because the user concludes the file is gone.

Two details that make this easy to get wrong, both of which fail silently rather than loudly:

- **The layout is `2006-01-02T15:04:05Z0700`**, or a bare date. ocdav *discards* a value it cannot parse and falls back to its own two days, so a wrong format produces a plausible listing that ignores `--since`.
- **Both ends or neither.** The driver honours a range only when it has `from` and `to`; one alone is ignored the same way.

There is no way to restore somewhere else, and no way to empty the bin in one go. Both were offered once and both were removed, because neither did what it said: a destination is ignored by the driver while ocdav deletes what is already there and then calls the restore a failure, and emptying a bin reaches a method the driver does not implement. A flag that appears to work and does something else is worse than its absence.

Restoring has the same window underneath it. There is no "put it back" on the wire: a restore is a `MOVE` and needs a `Destination`, and the only source for an item's original location is the listing. Looking in the default window is why restoring a week-old file used to fail with *no item with this key*, which reads as a bad key rather than a search too narrow to contain it. The lookup now widens — 2 days, then 14, then 90 — so the common restore still costs one request and an old one still works.

## Browsing the bin

`trash browse` shows the trash as a directory tree. Every entry carries the path it used to have, so the shape the user remembers can be rebuilt from a flat list of opaque keys — which is also how the key stops being something anybody has to see or copy.

Three things this buys that flags cannot:

- **You need not know when.** `--since` requires already knowing roughly when the file went. `t` widens the window from inside, and the tree grows.
- **A folder is one row.** Forty thousand deletions under one directory are one line with a count, not forty thousand lines. And a directory deleted *whole* is a single entry in the storage whose restore brings back everything underneath, so it is marked apart (`▪ whole folder`) and costs one request instead of forty thousand.
- **Holes are shown where they matter.** A day the storage refuses appears in the title bar as `⚠ N days unlistable`, which makes every count on screen visibly a lower bound. A listing that quietly omits a day is worse than one that fails, because the user concludes the file is gone.

Nothing moves until a plan has been shown and confirmed. The plan makes two corrections a user should not have to make by hand: an entry covered by a folder also being restored is dropped, because restoring the folder brings it back and the child would then fail against a path that already exists; and restores are ordered parents first, because a child arriving before its directory is a 409.

### Launching it

The order is *fetch, then take the screen* — never the reverse. Credentials resolve on the first request rather than when the client is built, so a device-code sign-in prints a URL and a code and waits; that has to happen while the normal screen is still visible. The same goes for the first failure: a 403, or a space with no bin, is an ordinary command error, not something to discover inside a full-screen UI with no obvious way out.

It refuses to start rather than half-work:

| Condition | Why |
| --- | --- |
| Either end is not a terminal | A frame written to a pipe is gibberish, and a scheduled job that meets a full-screen UI hangs until somebody kills it |
| `--output json` or `csv` | An explicit request for something this cannot produce; ignoring the flag would be the wrong kind of helpful |
| `--quiet` | It and a full-screen browser ask for opposite things |
| `TERM` unset or `dumb` | Nothing to draw on |

`--plain`, or `CERNBOX_NO_TUI`, prints the listing instead — and deliberately skips every check above, since it is the escape hatch for exactly the places where the browser cannot run.

The alternate screen takes its contents with it when it closes, so anything worth keeping is reprinted afterwards: what was restored, what failed and why, and the equivalent plain command. Quitting is exit 0; an operation with failures is not.

### What it does not do yet

- **No live `exists now` mark per row.** Conflicts are detected when the plan is built, where it matters most, because marking every visible row means a stat per row and an asynchronous redraw the event loop does not have yet.
- **No streaming of days as they arrive.** A wide window is fetched by halving refused ranges, so days do not arrive in order and there is no natural per-day boundary to stream on. A long fetch shows a notice, not a count.
- **No resume across runs.** Failures are listed and can be retried in the session; a journal on disk that survives `Ctrl-C` is not there.

## Editing without thinking about transfers

`edit` fetches a working copy, runs your editor on it, and uploads whenever the file changes — including while the editor is still open, which is the part that makes it feel like editing rather than like two transfers with a pause in between.

**It polls the file rather than watching it.** This is the one decision worth explaining, because the obvious answer is inotify and the obvious answer is wrong here. How an editor saves varies: measured on this machine, vim writes in place, with its default settings *and* with `backupcopy=no`, while `sed -i` replaces the file, and the "atomic save" editors are known for doing the same. A watch follows the inode, so it works for one editor and silently stops working for the next, or after a user changes a single setting. Watching the path has neither problem. inotify is also Linux-only and this is built for macOS too, and a file-watching library would be the first new direct dependency in a module that has exactly one. A stat per second, with the content hashed only once the size or the timestamp moves, notices a save about as fast as a person can and does not care how the editor wrote it.

The final check before exiting deliberately skips the cheap stat gate and hashes regardless. A file rewritten to the same length inside one timestamp tick looks untouched to a stat, and on a filesystem with coarse timestamps that is not far-fetched — so the one sync that must never miss anything does not rely on timestamps at all.

**A local file is edited where it lies.** `edit ./notes.txt` opens your own file, and nothing is ever downloaded over it — the CERNBox side is read into memory to compare, never to install. That comparison is what decides whether a name already in use is a problem: the same content means this is the file's own earlier upload, so editing the same file twice needs no flag, while *different* content under that name is refused until `--force`, the way `put` refuses to overwrite a destination. A warning would be too late, since closing the editor is enough to trigger the upload.

**Each save leaves its predecessor somewhere.** Saving ten times sends ten writes, and the storage keeps what each one replaced: a version where version history is on, an entry in the trash bin where it is not — the dev EOS keeps no versions, and there an overwrite puts the content it replaced straight into the bin. Neither is lost work, but both consume quota, and `--no-watch` is the way to spend one write per session instead of one per save.

**Every upload is conditional.** The `ETag` the working copy came from is sent as `If-Match`, and refreshed after each save so the next one is conditional on the version just written. A file changed by somebody else meanwhile is therefore a refusal rather than a silent overwrite. When that happens the working copy stays on disk and its path is printed, because it is the only place that edit exists; deleting it would be the worst thing this command could do. `--force` drops the precondition.

Two smaller decisions. The folder for a bare name is created on the first save, not when the editor opens, so quitting without saving leaves nothing behind. And the editor command is split on spaces and executed directly rather than handed to a shell, so `code -w` works while the file name never reaches an interpreter.

## Unix conventions

The filesystem commands follow their coreutils namesakes, including the flags people type without thinking: `-p` on `mkdir`, `-r`/`-R` and `-f` on `rm` and `cp`, `-c` on `touch`, `-h` on `ls` and `du`.

Two places where this CLI is deliberately more cautious than the original, both because the target is remote and a mistake is not local:

- **`touch` never rewrites an existing file.** `touch(1)` would update its timestamp; CERNBox offers no way to do that without rewriting the contents, so an existing path is reported and left alone.
- **`mv` and `cp` refuse to overwrite** unless given `-f`. The originals overwrite silently.

## Handing a file over live

Everything above is store-and-forward: `copy` finishes, and `paste` can happen next week. `--stream` is the other shape — the two commands run at the same time and the bytes move between them, with nothing left on the server at all.

```bash
# on the machine with the file: this blocks, holding the file open
cernbox copy --stream ./hugefile.root
```

```bash
# on the other machine, whenever you get there
cernbox paste ./hugefile.root
```

```console
$ cernbox copy --stream ./hugefile.root
Waiting for 'cernbox paste' on another machine...
Connected. Sending hugefile.root...
Sent 200.0M. Nothing was stored in CERNBox.
```

Nothing is uploaded until somebody pastes. Afterwards `cernbox clipboard list` is empty: no quota consumed, nothing to clear, nothing in your trash. The two halves overlap, so the wall-clock is roughly one transfer rather than two in sequence. `--wait` bounds how long either side will hang around, ten minutes by default, and a sender that gives up cleans its slot up on the way out.

**The bytes still pass through CERNBox.** That is worth being plain about, because it is the one thing `--stream` cannot fix. A direct connection between the two machines is what you would want, and it does not work here: a laptop is behind NAT so nothing can dial into it, and lxplus does not accept inbound connections on arbitrary ports. There is no path between the two except the server they both already talk to. What `--stream` avoids is the *storage* — the sender runs only four pieces ahead of the receiver, which deletes each one as it reads it, so the slot holds a few tens of megabytes no matter whether you are sending a gigabyte or a hundred.

The constraint that comes with it: both machines have to be running the command at once. That is the trade against the default, where the sending machine can close its laptop lid and the file is still there on Monday. Neither is better; they are for different situations.

Two things `--stream` will not do. It refuses a source that is already in CERNBox, because referencing it is strictly better — `cernbox copy cb:/eos/...` transfers nothing at all and pasting it to another CERNBox path is a server-side copy. And a live stream can only be received to a local path or a pipe, since there is nothing on the server for the graph to copy from. For a directory, tar it: `tar cz ./dir | cernbox copy --stream -`.

## Progress

`paste` draws a bar while the bytes move:

```console
report.pdf  ███████████████░░░░░░░░░░░░░░░░░░░   43%  86.1M/200.0M  10.2M/s  eta 11s
```

It is erased when the transfer finishes, leaving the summary line. `--no-progress` turns it off, and so do `--quiet` and `--output json`; it is never drawn when stderr is not a terminal, so a log file or a pipe stays clean. The gate is on **stderr**, not stdout, so `cernbox paste - | tar xz` still shows you the bar while the payload goes down the pipe.

`--no-progress` is global rather than a flag on `paste` alone, because it also silences the per-file lines `cp`, `get`, `put` and `sync` print.

A live handover of a pipe has no length until it ends, so there is no percentage to show and none is invented — the line becomes a byte count and a rate. On a narrow window the bar is dropped and then whole fields are, in order: the estimate, the rate, the total. Nothing is ever cut mid-number, because a figure you cannot trust is worse than one that is absent.

## Slots

One slot is used unless you name another, so the two-command case stays two commands. `--slot` keeps several copies in flight without them treading on each other:

```bash
cernbox copy -r ./build-logs --slot logs
cernbox paste --slot logs ./incoming/
cernbox clipboard list
cernbox clipboard clear logs
```

```console
$ cernbox clipboard list
SLOT      FROM      CONTENTS          SIZE   ORIGIN              COPIED   EXPIRES
default   -         report.pdf        2.1M   gdelmont@lxplus812  20m ago  2026-09-30 00:00
logs      -         build-logs/ +2 m  118M   gdelmont@nb-042     3h ago   2026-09-30 00:00
to-marie  -         plots.tar         14M    gdelmont@nb-042     5m ago   2026-09-30 00:00
default   asmith    dataset.root      1.2G   asmith@lxplus701    1h ago   2026-09-30 00:00
```

Rows with a `FROM` are handovers waiting for you, on somebody else's quota rather than yours.

The `ORIGIN` column is there because a clipboard shared by every machine on one account is otherwise ambiguous — knowing a copy came from lxplus twenty minutes ago is most of what you want from a listing.

## What it does and does not do

**Pasting does not empty the clipboard.** That is deliberate: it is a clipboard, so the same copy reaches as many machines as you like, and a paste that fails halfway has not destroyed the only copy. `cernbox clipboard clear` is how you let go of it.

**Uploaded copies count against your quota** until the slot is cleared or expires. `--ttl` sets the lifetime, a week by default, and `0` means "until I clear it". Nothing runs on a schedule to collect expired slots, so `copy` and `clipboard list` do it on the way past. `clipboard list` shows what each slot is costing; note that a cleared slot's bytes land in your trash first, so `cernbox trash purge` is what actually returns the quota.

**Clearing never touches a referenced file.** A copy made with `cb:` points at your real file; only the duplicates the CLI uploaded for the clipboard itself are deleted. If you move or delete a referenced file, a later paste says so rather than reporting a bare "no such file".

**A handover exposes one slot, and grants as little on it as the job needs.** `--to` shares the slot directory and nothing else: the recipient cannot read the rest of your clipboard, or the directory it sits in. A stored handover is read-only, so the copy stays yours to clear. A live one (`--stream --to`) additionally makes the `stream/` directory inside the slot writable, because the protocol needs the receiver to announce itself there and to delete each piece as it consumes it — that deletion is the flow control. The manifest and everything else stay read-only either way.

**Two machines copying to the same slot is detected, not silently resolved.** The manifest is replaced only while its ETag is unchanged, so the second copy is told that the first one landed instead of overwriting it and orphaning its staged bytes. A copy that fails partway leaves the previous one intact and pastable, because nothing is deleted until the replacement has been committed. The one gap is two machines writing a slot for the *very first* time simultaneously: reva ignores `If-None-Match`, so there is no way to make creating a file exclusive, and that case is last-writer-wins.
