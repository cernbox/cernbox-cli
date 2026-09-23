package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/clipboard"
	"github.com/cernbox/cernbox-cli/pkg/output"
)

// Live handover: "cernbox copy --stream" on one machine waits, and the bytes
// move only once "cernbox paste" runs on the other.
//
// The bytes still pass through CERNBox, and that is not an implementation
// shortcut. A direct connection between the two machines is what one would want,
// and it does not work here: a laptop is behind NAT so nothing can dial into it,
// and lxplus does not accept inbound connections on arbitrary ports. There is no
// path between the two except the server they both already talk to.
//
// What a handover avoids is not the round trip but the *storage*. The sender runs
// only a small window ahead and the receiver deletes each piece as it reads it,
// so the slot holds a few tens of megabytes no matter how large the transfer, and
// the two halves overlap instead of running one after the other. Nothing is left
// behind to occupy quota, and there is nothing to clear afterwards.
//
// The whole protocol is files, because PUT, GET, PROPFIND and DELETE are the
// entire vocabulary reva gives two clients for talking to each other. There is no
// notification of any kind, so both sides poll.

const (
	// streamWindow is how many pieces the sender may run ahead of the receiver.
	// Enough to keep both links busy; small enough that the slot stays a pipe
	// rather than becoming storage.
	streamWindow = 4

	// streamPoll is how often each side looks for the other's next move. Every
	// poll is a request, so this trades latency against load on the server;
	// a quarter second is imperceptible next to the time a chunk takes to move.
	streamPoll = 250 * time.Millisecond
)

// ── sender ───────────────────────────────────────────────────────────────────

// streamCopy holds the source open and feeds it to whoever pastes.
func (a *App) streamCopy(ctx context.Context, src copySource, opts copyOptions) error {
	root, err := a.clipboardRoot(ctx)
	if err != nil {
		return err
	}

	body, size, err := a.openStreamSource(src)
	if err != nil {
		return err
	}
	defer body.Close()

	chunk, err := a.chunkSize()
	if err != nil {
		return err
	}

	// Anything the slot held is gone: a handover and a stored copy cannot share
	// one slot, and leaving the old payload would strand it with no manifest.
	prior, priorETag, err := a.priorSlot(ctx, root, opts.slot)
	if err != nil {
		return err
	}

	m := clipboard.New(opts.slot, time.Now(), opts.ttl, clipboard.LocalOrigin(workingDir()))
	m.Mode = clipboard.ModeStream
	m.To = opts.to
	m.Stream = &clipboard.StreamInfo{ChunkSize: chunk, Window: streamWindow}
	m.Entries = []clipboard.Entry{{
		Name:   src.name,
		Path:   clipboard.StreamDir(root, opts.slot),
		Size:   size, // -1 when the source is a pipe and its length is unknown
		Staged: true,
	}}

	if err := a.client.Mkdir(ctx, clipboard.StreamDir(root, opts.slot), true); err != nil {
		return err
	}
	if err := a.writeSlot(ctx, root, m, priorETag); err != nil {
		return err
	}
	a.discardUnreferenced(ctx, prior, m)

	// Everything from here leaves state on the server that only this process will
	// clean up, so the slot is torn down however this ends.
	defer a.teardownStream(root, opts.slot)

	// The recipient has to be able to see the slot before this blocks on them, and
	// to write in the pieces directory before they can announce themselves.
	if opts.to != "" {
		if err := a.shareStreamHandover(ctx, root, opts.slot, opts.to); err != nil {
			return err
		}
	}

	a.out.Msg("Waiting for %s...", a.streamPeer(ctx, opts))
	if err := a.waitForReader(ctx, root, m, opts.wait); err != nil {
		return err
	}
	a.out.Msg("Connected. Sending %s...", src.name)

	sent, parts, err := a.feedStream(ctx, root, opts.slot, body, chunk, opts.wait)
	if err != nil {
		return err
	}

	if err := a.putJSON(ctx, clipboard.DonePath(root, opts.slot),
		clipboard.Done{Parts: parts, Size: sent}); err != nil {
		return err
	}

	// The last pieces may still be in flight. Waiting for the receiver to drain
	// them is what lets the teardown below be unconditional: a slot deleted while
	// the far end is still reading would truncate the transfer.
	if err := a.waitForDrain(ctx, root, opts.slot, opts.wait); err != nil {
		return err
	}

	a.out.Msg("Sent %s. Nothing was stored in CERNBox.", proseSize(sent))
	if a.out.Format() == output.FormatJSON {
		return a.out.Object(m)
	}
	return nil
}

// streamPeer names who the sender is waiting for, since "another computer" is
// wrong when it is another person.
func (a *App) streamPeer(ctx context.Context, opts copyOptions) string {
	if opts.to != "" {
		return fmt.Sprintf("%s to run 'cernbox paste --from %s'", opts.to, a.username(ctx))
	}
	return fmt.Sprintf("'%s' on another computer", pasteHint(opts.slot))
}

// shareStreamHandover gives the recipient what a live handover needs, and no
// more: the slot to read, and the pieces directory to write in.
//
// The two grants are deliberately different. Everything that describes the
// handover — the manifest, the done marker — stays read-only, so the recipient
// cannot rewrite what they are being sent. Only the directory that holds the
// bytes in flight is writable, because the protocol needs them to announce
// themselves there and to delete each piece as they consume it. That deletion is
// the flow control: it is what keeps a slot a pipe rather than storage.
func (a *App) shareStreamHandover(ctx context.Context, root, slot, user string) error {
	if err := a.shareHandover(ctx, clipboard.SlotDir(root, slot), user, client.RoleViewer); err != nil {
		return err
	}
	return a.shareHandover(ctx, clipboard.StreamDir(root, slot), user, client.RoleEditor)
}

// openStreamSource opens what is being handed over, and reports its length when
// there is one to report.
func (a *App) openStreamSource(src copySource) (io.ReadCloser, int64, error) {
	if src.stdin {
		return io.NopCloser(a.in()), -1, nil
	}
	if src.spec.IsRemote() {
		return nil, 0, cberr.Usagef(
			"%s is already in CERNBox, so there is nothing to send. Copy it without "+
				"--stream: pasting it then costs no transfer at all.", src.spec.Raw)
	}

	info, err := os.Stat(src.spec.Path)
	if err != nil {
		return nil, 0, cberr.Wrap(cberr.KindNotFound, "read", src.spec.Path, err)
	}
	if info.IsDir() {
		return nil, 0, cberr.Usagef(
			"%s is a directory: stream a single file, or tar it — 'tar cz %s | cernbox copy --stream -'",
			src.spec.Path, src.spec.Path)
	}
	f, err := os.Open(src.spec.Path)
	if err != nil {
		return nil, 0, cberr.Wrap(cberr.KindOther, "read", src.spec.Path, err)
	}
	return f, info.Size(), nil
}

// waitForReader blocks until somebody pastes, or until the wait runs out.
func (a *App) waitForReader(ctx context.Context, root string, m *clipboard.Manifest, wait time.Duration) error {
	slot := m.Slot
	deadline := time.Now().Add(wait)
	for {
		if _, err := a.client.Stat(ctx, m.ReaderPath(root)); err == nil {
			return nil
		} else if cberr.KindOf(err) != cberr.KindNotFound {
			return err
		}
		if time.Now().After(deadline) {
			hint := pasteHint(slot)
			if m.To != "" {
				hint = "cernbox paste --from " + a.username(ctx)
			}
			return cberr.New(cberr.KindOther, "stream", slot,
				fmt.Sprintf("nobody pasted within %s. Run '%s' on the other computer while "+
					"this one waits, or use --wait to give it longer.", wait, hint))
		}
		if err := sleepCtx(ctx, streamPoll); err != nil {
			return err
		}
	}
}

// feedStream reads the source and publishes it a piece at a time, never running
// more than a window ahead of the receiver.
func (a *App) feedStream(ctx context.Context, root, slot string, body io.Reader, chunk int64, wait time.Duration) (int64, int, error) {
	buf := make([]byte, chunk)
	var sent int64
	parts := 0

	for {
		n, readErr := io.ReadFull(body, buf)
		if n > 0 {
			if err := a.awaitWindow(ctx, root, slot, wait); err != nil {
				return 0, 0, err
			}
			if err := a.putBytes(ctx, clipboard.ChunkPath(root, slot, parts), buf[:n]); err != nil {
				return 0, 0, err
			}
			sent += int64(n)
			parts++
		}
		switch readErr {
		case nil, io.ErrUnexpectedEOF:
			if readErr == io.ErrUnexpectedEOF {
				return sent, parts, nil
			}
		case io.EOF:
			return sent, parts, nil
		default:
			return 0, 0, cberr.Wrap(cberr.KindOther, "read the source", "", readErr)
		}
	}
}

// awaitWindow blocks while the receiver is behind, which is what keeps the slot a
// pipe instead of letting a fast sender fill it with the whole transfer.
func (a *App) awaitWindow(ctx context.Context, root, slot string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		pending, err := a.client.List(ctx, clipboard.StreamDir(root, slot))
		if err != nil {
			return err
		}
		if pendingChunks(pending) < streamWindow {
			return nil
		}
		if time.Now().After(deadline) {
			return cberr.New(cberr.KindOther, "stream", slot,
				fmt.Sprintf("the other computer stopped reading for %s. It may have been interrupted.", wait))
		}
		if err := sleepCtx(ctx, streamPoll); err != nil {
			return err
		}
	}
}

// pendingChunks counts the pieces still in flight, ignoring anything else in the
// directory.
//
// The directory is not only pieces: a handover's arrival marker lives there too,
// because it is the one place the recipient can write. Counting entries rather
// than pieces would make the window permanently one short and the drain never
// finish.
func pendingChunks(entries []client.ResourceInfo) int {
	n := 0
	for _, e := range entries {
		if _, ok := clipboard.ChunkIndex(e.Name); ok {
			n++
		}
	}
	return n
}

// waitForDrain blocks until the receiver has taken everything.
func (a *App) waitForDrain(ctx context.Context, root, slot string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		pending, err := a.client.List(ctx, clipboard.StreamDir(root, slot))
		if err != nil {
			if cberr.KindOf(err) == cberr.KindNotFound {
				return nil // the receiver tore it down already
			}
			return err
		}
		if pendingChunks(pending) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return cberr.New(cberr.KindOther, "stream", slot,
				fmt.Sprintf("the other computer did not finish reading within %s", wait))
		}
		if err := sleepCtx(ctx, streamPoll); err != nil {
			return err
		}
	}
}

// teardownStream removes the slot once the handover is over, whatever the
// outcome.
//
// It runs on its own context rather than the command's: the usual reason to be
// here is that the command was cancelled, and cleanup that needs a live context
// would then be exactly the cleanup that never happens, leaving a slot behind
// that no later run knows to collect.
func (a *App) teardownStream(root, slot string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Before the slot, since a share made on a path that has been deleted follows
	// it into the trash and stays in the user's share list naming it. Both grants
	// have to go: a live handover shares the pieces directory as well.
	a.unshareHandover(ctx, root, slot)
	if _, err := a.removeIfPresent(ctx, clipboard.SlotDir(root, slot)); err != nil {
		a.out.Warn("could not clean up the %q stream slot: %v", slot, err)
	}
}

// ── receiver ─────────────────────────────────────────────────────────────────

// streamPaste takes the other side of the handover, writing what arrives to a
// local path or to standard output.
func (a *App) streamPaste(ctx context.Context, root string, m *clipboard.Manifest, dest string, toStdout bool, opts pasteOptions) error {
	if len(m.Entries) != 1 {
		return cberr.New(cberr.KindOther, "paste", m.Slot,
			"this clipboard slot is damaged: it does not name a single file")
	}
	e := m.Entries[0]

	out, finish, err := a.openStreamSink(e, dest, toStdout, opts)
	if err != nil {
		return err
	}

	// Saying "I am here" is what releases the sender, which has been blocked
	// since it started.
	if err := a.putBytes(ctx, m.ReaderPath(root),
		[]byte(clipboard.LocalOrigin(workingDir()).String()+"\n")); err != nil {
		_ = finish(err) // the sink is discarded; this error is the one to report
		return err
	}
	a.out.Msg("Receiving %s from %s...", e.Name, m.Origin)

	// A handover of a pipe has no length until it ends, so the bar may have no
	// total to work against. It shows bytes and a rate in that case rather than
	// inventing a percentage.
	bar := a.newMeter(e.Name, e.Size)
	defer bar.Stop()

	received, err := a.drainStream(ctx, root, m.Slot, bar.Writer(out), opts.wait)
	bar.Stop()
	if ferr := finish(err); err == nil {
		err = ferr
	}
	if err != nil {
		return err
	}

	// The slot is the sender's to remove, but it waits for the pieces to be gone
	// and this is the side that knows they are.
	a.out.Msg("Received %s", proseSize(received))
	if a.out.Format() == output.FormatJSON {
		return a.out.Object(m)
	}
	return nil
}

// openStreamSink decides where the arriving bytes go, and returns the function
// that closes it out — committing on success, cleaning up on failure.
func (a *App) openStreamSink(e clipboard.Entry, dest string, toStdout bool, opts pasteOptions) (io.Writer, func(error) error, error) {
	if toStdout {
		return a.stdout, func(error) error { return nil }, nil
	}

	target := dest
	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		target = filepath.Join(dest, e.Name)
	}
	if !opts.force {
		if _, err := os.Stat(target); err == nil {
			return nil, nil, cberr.New(cberr.KindConflict, "paste", target,
				"already exists: pass -f to overwrite")
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, nil, cberr.Wrap(cberr.KindOther, "create", filepath.Dir(target), err)
	}

	// Written beside the destination and renamed at the end, so that an
	// interrupted handover never leaves a plausible-looking partial file.
	part := target + ".part"
	f, err := os.Create(part)
	if err != nil {
		return nil, nil, cberr.Wrap(cberr.KindOther, "create", part, err)
	}
	finish := func(streamErr error) error {
		closeErr := f.Close()
		if streamErr != nil {
			os.Remove(part)
			return nil // the caller reports the stream error
		}
		if closeErr != nil {
			os.Remove(part)
			return cberr.Wrap(cberr.KindOther, "write", part, closeErr)
		}
		if err := os.Rename(part, target); err != nil {
			return cberr.Wrap(cberr.KindOther, "write", target, err)
		}
		return nil
	}
	return f, finish, nil
}

// drainStream consumes pieces in order, deleting each one as it is read — which
// is both how the sender is given room to send the next and why nothing
// accumulates on the server.
func (a *App) drainStream(ctx context.Context, root, slot string, out io.Writer, wait time.Duration) (int64, error) {
	var received int64
	deadline := time.Now().Add(wait)

	for next := 0; ; {
		body, _, err := a.client.Download(ctx, clipboard.ChunkPath(root, slot, next), 0)
		switch {
		case err == nil:
			n, copyErr := io.Copy(out, body)
			body.Close()
			if copyErr != nil {
				return received, cberr.Wrap(cberr.KindOther, "receive", slot, copyErr)
			}
			received += n

			if _, err := a.removeIfPresent(ctx, clipboard.ChunkPath(root, slot, next)); err != nil {
				return received, err
			}
			next++
			deadline = time.Now().Add(wait)
			continue

		case cberr.KindOf(err) != cberr.KindNotFound:
			return received, err
		}

		// No piece yet. Either the sender is still working on it, or there will
		// never be one — and only the done marker tells those apart.
		done, err := a.readDone(ctx, root, slot)
		if err != nil {
			return received, err
		}
		if done != nil && next >= done.Parts {
			if done.Size != received {
				return received, cberr.New(cberr.KindOther, "receive", slot,
					fmt.Sprintf("%d bytes were sent but %d arrived", done.Size, received))
			}
			return received, nil
		}
		if time.Now().After(deadline) {
			return received, cberr.New(cberr.KindOther, "receive", slot,
				fmt.Sprintf("nothing arrived for %s. The other computer may have been interrupted.", wait))
		}
		if err := sleepCtx(ctx, streamPoll); err != nil {
			return received, err
		}
	}
}

// readDone reports the sender's end-of-stream marker, or nil while there is none.
func (a *App) readDone(ctx context.Context, root, slot string) (*clipboard.Done, error) {
	body, _, err := a.client.Download(ctx, clipboard.DonePath(root, slot), 0)
	if err != nil {
		if cberr.KindOf(err) == cberr.KindNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer body.Close()

	b, err := io.ReadAll(io.LimitReader(body, clipboard.MaxManifestSize))
	if err != nil {
		return nil, cberr.Wrap(cberr.KindOther, "receive", slot, err)
	}
	var done clipboard.Done
	if err := json.Unmarshal(b, &done); err != nil {
		return nil, cberr.Wrap(cberr.KindOther, "receive", slot, err)
	}
	return &done, nil
}

// ── shared ───────────────────────────────────────────────────────────────────

// putJSON writes a small control file.
func (a *App) putJSON(ctx context.Context, p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return cberr.Wrap(cberr.KindOther, "write", p, err)
	}
	return a.putBytes(ctx, p, append(b, '\n'))
}

// sleepCtx waits, but gives up the moment the command is cancelled — so that
// Ctrl-C on a waiting sender is immediate rather than up to a poll late.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
