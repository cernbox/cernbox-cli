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

`-h`, `-s`, `-a` and `-d`/`--max-depth` carry their usual meanings. One deliberate difference: only the total for each argument is reported unless `--max-depth` asks for more — that is `du -s` rather than `du`'s own default, because descending a whole tree here costs one request per directory, and the totals CERNBox reports are already recursive. Sizes are apparent bytes, not disk blocks, which the server does not report.

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
