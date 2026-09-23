package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/clipboard"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/cernbox/cernbox-cli/pkg/pathspec"
	"github.com/spf13/cobra"
)

// The cross-machine clipboard: "cernbox copy" on one machine, "cernbox paste" on
// another, with CERNBox carrying whatever is in between.
//
// There are two ways for bytes to get from one machine to the other, and the
// difference is worth understanding because it decides how much this costs:
//
//   - A path already inside CERNBox is *referenced*. Nothing is transferred on
//     copy, and if the paste destination is also a CERNBox path nothing is
//     transferred then either — the server copies it. Two requests, no data.
//   - A local path is *staged*: uploaded into the slot on copy, downloaded on
//     paste. That is unavoidable when the file only exists on one machine.
//
// Which one applies follows the same rule every transfer command uses: a "cb:"
// prefix or a space alias means CERNBox, anything else is local. Guessing from
// the string is not an option, because on lxplus /eos/user/g/gdelmont is both.

// newCopyCmd builds "cernbox copy".
func newCopyCmd(app *App) *cobra.Command {
	var opts copyOptions

	cmd := &cobra.Command{
		Use:   "copy PATH...",
		Short: "Copy files, to paste on another computer",
		Long: "Put files on a clipboard stored in CERNBox, then paste them on another\n" +
			"computer you are signed in on.\n\n" +
			"A CERNBox path (cb:/eos/...) is only pointed at, so nothing is\n" +
			"transferred. A file on your computer is uploaded, which is what lets you\n" +
			"paste it elsewhere.\n\n" +
			"Copying replaces what the clipboard held. Pasting does not empty it, so\n" +
			"you can paste on several computers. 'cernbox clipboard clear' frees the\n" +
			"space.\n\n" +
			"With --stream nothing is stored: this command waits for the paste and\n" +
			"sends the file straight to it.",
		Example: "  cernbox copy ./report.pdf\n" +
			"  cernbox copy cb:/eos/user/g/gdelmont/report.pdf\n" +
			"  cernbox copy -r ./data --slot build\n" +
			"  tar cz dir | cernbox copy - --name dir.tgz",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()
			return app.clipboardCopy(ctx, args, opts)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.slot, "slot", clipboard.DefaultSlot, "clipboard slot to copy into")
	f.BoolVarP(&opts.recursive, "recursive", "r", false, "copy directories and their contents")
	f.BoolVarP(&opts.recursive, "recursive-upper", "R", false, "same as --recursive")
	_ = f.MarkHidden("recursive-upper")
	f.DurationVar(&opts.ttl, "ttl", clipboard.DefaultTTL,
		"how long the copy lives; 0 keeps it until you clear it")
	f.StringVar(&opts.name, "name", "", `name to store standard input under (default "stdin")`)
	f.BoolVar(&opts.verify, "verify", false, "compute and check checksums when staging")
	f.IntVarP(&opts.jobs, "jobs", "j", 0, "number of files to transfer at once")
	f.BoolVar(&opts.stream, "stream", false,
		"wait for a paste on another computer and send the file straight to it")
	f.DurationVar(&opts.wait, "wait", 10*time.Minute,
		"how long to wait for the other computer, with --stream")
	return cmd
}

// copyOptions is what the copy flags select.
type copyOptions struct {
	slot      string
	recursive bool
	ttl       time.Duration
	name      string
	verify    bool
	jobs      int
	// stream hands the source over live instead of leaving a copy on the server.
	stream bool
	// wait bounds every step that depends on the other machine.
	wait time.Duration
}

// newPasteCmd builds "cernbox paste".
func newPasteCmd(app *App) *cobra.Command {
	var opts pasteOptions

	cmd := &cobra.Command{
		Use:   "paste [DEST]",
		Short: "Paste what was copied on another computer",
		Long: "Paste what was copied.\n\n" +
			"With no destination the files land in the current directory under their\n" +
			"own names. Give a CERNBox path (cb:/eos/...) and the server copies them\n" +
			"without sending any data. Use - to write a single file to standard output.\n\n" +
			"Pasting leaves the clipboard alone, so you can paste again elsewhere.\n" +
			"'cernbox clipboard clear' frees the space when you are done.\n\n" +
			"A progress bar is shown on a terminal. --no-progress turns it off.",
		Example: "  cernbox paste\n" +
			"  cernbox paste ./incoming/\n" +
			"  cernbox paste cb:/eos/project/c/cernbox/data/\n" +
			"  cernbox paste --slot build - | tar xz",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()
			return app.clipboardPaste(ctx, args, opts)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.slot, "slot", clipboard.DefaultSlot, "clipboard slot to paste from")
	f.BoolVarP(&opts.force, "force", "f", false, "overwrite existing destinations")
	f.BoolVar(&opts.verify, "verify", false, "compute and check checksums")
	f.BoolVar(&opts.noArchive, "no-archive", false, "download a directory file by file instead of as one archive")
	f.IntVarP(&opts.jobs, "jobs", "j", 0, "number of files to transfer at once")
	f.DurationVar(&opts.wait, "wait", 10*time.Minute,
		"how long to wait for a computer that is sending with --stream")
	return cmd
}

// pasteOptions is what the paste flags select.
type pasteOptions struct {
	slot      string
	force     bool
	verify    bool
	noArchive bool
	jobs      int
	// wait bounds how long a live handover will sit waiting for the far end.
	wait time.Duration
}

// newClipboardCmd builds the "cernbox clipboard" group, which manages the slots
// that copy and paste use.
func newClipboardCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clipboard",
		Short: "See and clear the clipboard",
		Long: "The clipboard that 'cernbox copy' and 'cernbox paste' share.\n\n" +
			"It is stored in CERNBox, so every computer you are signed in on sees the\n" +
			"same clipboard. Uploaded files use your quota until you clear them.\n\n" +
			"Use --slot to keep more than one copy at a time.",
	}
	cmd.AddCommand(newClipboardListCmd(app), newClipboardClearCmd(app))
	return cmd
}

func newClipboardListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show what is on the clipboard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()
			return app.clipboardList(ctx)
		},
	}
}

func newClipboardClearCmd(app *App) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "clear [SLOT...]",
		Short: "Empty the clipboard and free the space",
		Long: "Empty the clipboard. With no name, the default slot.\n\n" +
			"Files that were only pointed at are left alone. Only the uploaded copies\n" +
			"are deleted, and they go to the trash, so 'cernbox trash purge' frees the\n" +
			"quota.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()
			return app.clipboardClear(ctx, args, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "clear every slot")
	return cmd
}

// ── copy ─────────────────────────────────────────────────────────────────────

// copySource is one argument to copy, classified before anything is transferred.
type copySource struct {
	// stdin marks the "-" argument.
	stdin bool
	// spec is the parsed path, for everything else.
	spec pathspec.Spec
	// name is what the entry will be called on the clipboard, and what paste
	// writes it as.
	name string
}

func (a *App) clipboardCopy(ctx context.Context, args []string, opts copyOptions) error {
	if err := clipboard.ValidateSlot(opts.slot); err != nil {
		return cberr.Usagef("%v", err)
	}

	// Everything is parsed and named before anything moves. A typo in the last
	// argument should not leave a slot holding half a copy.
	sources, err := classifyCopySources(args, opts)
	if err != nil {
		return err
	}

	if opts.stream {
		// A handover is one source by construction: it is a live pipe between two
		// machines, and there is nothing sensible for a second one to do while the
		// first is being read.
		if len(sources) != 1 {
			return cberr.Usagef("--stream hands over one thing at a time, and %d were given",
				len(sources))
		}
		return a.streamCopy(ctx, sources[0], opts)
	}

	root, err := a.clipboardRoot(ctx)
	if err != nil {
		return err
	}

	// What the slot holds now, and the ETag that says so. Replacing the manifest
	// conditionally on that ETag is what turns two machines copying to the same
	// slot at the same moment into an error rather than a corrupt slot.
	prior, priorETag, err := a.priorSlot(ctx, root, opts.slot)
	if err != nil {
		return err
	}

	// Anything not already in CERNBox is uploaded into the slot, and a PUT into a
	// directory that is not there answers 409, so the payload directory has to
	// exist before the first one. A slot of pure references never needs it.
	if anyStaged(sources) {
		if err := a.client.Mkdir(ctx, clipboard.PayloadDir(root, opts.slot), true); err != nil {
			return err
		}
	}

	m := clipboard.New(opts.slot, time.Now(), opts.ttl, clipboard.LocalOrigin(workingDir()))
	for _, src := range sources {
		entry, err := a.copyOne(ctx, root, src, opts)
		if err != nil {
			return err
		}
		m.Entries = append(m.Entries, entry)
	}

	// Staging happens before the manifest is replaced, so until this line the
	// slot still holds the previous copy in full. A failure anywhere above leaves
	// that copy pastable, which is better than an empty slot — and much better
	// than a manifest pointing at bytes that have been deleted.
	if err := a.writeSlot(ctx, root, m, priorETag); err != nil {
		return err
	}

	// Committed. Only now is the previous copy's payload unreferenced, so only now
	// is it safe to delete — otherwise a clipboard would grow with every copy.
	a.discardUnreferenced(ctx, prior, m)

	// Collecting expired slots here rather than on a timer: copy is the command
	// that creates the quota pressure, so it is the natural place to give some
	// back, and there is nothing running on a schedule to do it instead.
	a.collectExpired(ctx, root)

	a.out.Msg("Copied %s (%s) to the %q clipboard slot", itemCount(len(m.Entries)),
		proseSize(m.Size()), opts.slot)
	a.out.Msg("Paste with '%s'", pasteHint(opts.slot))
	if a.out.Format() == output.FormatJSON {
		return a.out.Object(m)
	}
	return nil
}

// classifyCopySources parses the arguments and works out what each entry will be
// called, rejecting the cases that would silently lose one.
func classifyCopySources(args []string, opts copyOptions) ([]copySource, error) {
	out := make([]copySource, 0, len(args))
	seenStdin := false

	for _, arg := range args {
		if arg == "-" {
			if seenStdin {
				return nil, cberr.Usagef("standard input can only be copied once")
			}
			seenStdin = true
			name := opts.name
			if name == "" {
				name = "stdin"
			}
			out = append(out, copySource{stdin: true, name: name})
			continue
		}

		spec, err := pathspec.ParseTransfer(arg)
		if err != nil {
			return nil, cberr.Usagef("%v", err)
		}

		name := pathspec.Base(spec)
		if spec.IsLocal() {
			name = filepath.Base(strings.TrimRight(spec.Path, string(os.PathSeparator)))
		}
		if name == "" || name == "." || name == "/" || name == string(os.PathSeparator) {
			return nil, cberr.Usagef(
				"%q has no name to copy it under: name a file or a directory rather than a root", arg)
		}
		out = append(out, copySource{spec: spec, name: name})
	}

	// Two arguments with the same base name would land on top of each other, both
	// in the slot and wherever they are pasted.
	seen := make(map[string]bool, len(out))
	for _, s := range out {
		if seen[s.name] {
			return nil, cberr.Usagef(
				"two files are both called %q: copy them one at a time, or use --slot", s.name)
		}
		seen[s.name] = true
	}
	return out, nil
}

// anyStaged reports whether any source has to be uploaded, as opposed to merely
// pointed at.
func anyStaged(sources []copySource) bool {
	for _, s := range sources {
		if s.stdin || s.spec.IsLocal() {
			return true
		}
	}
	return false
}

// copyOne puts a single source on the clipboard, uploading it only when it is
// not already in CERNBox.
func (a *App) copyOne(ctx context.Context, root string, src copySource, opts copyOptions) (clipboard.Entry, error) {
	staged := path.Join(clipboard.PayloadDir(root, opts.slot), src.name)

	if src.stdin {
		size, parts, err := a.stageStdin(ctx, staged, opts)
		if err != nil {
			return clipboard.Entry{}, err
		}
		return clipboard.Entry{Name: src.name, Path: staged, Size: size, Staged: true, Parts: parts}, nil
	}

	if src.spec.IsRemote() {
		return a.referenceRemote(ctx, src, opts)
	}
	return a.stageLocal(ctx, src, staged, opts)
}

// referenceRemote records a path that is already in CERNBox, without moving a
// byte of it.
func (a *App) referenceRemote(ctx context.Context, src copySource, opts copyOptions) (clipboard.Entry, error) {
	p, err := a.resolveSpec(ctx, src.spec)
	if err != nil {
		return clipboard.Entry{}, err
	}
	info, err := a.client.Stat(ctx, p)
	if err != nil {
		return clipboard.Entry{}, err
	}
	if info.IsDir && !opts.recursive {
		return clipboard.Entry{}, cberr.Usagef("%s is a directory: pass -r to copy it", p)
	}
	return clipboard.Entry{
		Name:  src.name,
		Path:  p,
		IsDir: info.IsDir,
		Size:  info.Size,
		ETag:  info.ETag,
	}, nil
}

// stageLocal uploads a local path into the slot.
func (a *App) stageLocal(ctx context.Context, src copySource, staged string, opts copyOptions) (clipboard.Entry, error) {
	info, err := os.Stat(src.spec.Path)
	if err != nil {
		return clipboard.Entry{}, cberr.Wrap(cberr.KindNotFound, "read", src.spec.Path, err)
	}
	if info.IsDir() && !opts.recursive {
		return clipboard.Entry{}, cberr.Usagef("%s is a directory: pass -r to copy it", src.spec.Path)
	}

	// On lxplus this path may well be the FUSE mount of the very space the
	// clipboard lives in, in which case the upload is pure waste. The CLI cannot
	// tell — that ambiguity is why transfer commands need cb: at all — but it can
	// say so, because the saving is the whole file.
	if looksLikeCERNBoxMount(src.spec.Path) {
		a.out.Msg("Tip: cb:%s would point at the file instead of uploading a copy.", src.spec.Path)
	}

	engine, err := a.transferEngine(transferFlags{
		recursive: opts.recursive,
		force:     true, // the slot was just emptied; this writes into it
		verify:    opts.verify,
		jobs:      opts.jobs,
	})
	if err != nil {
		return clipboard.Entry{}, err
	}
	stats, err := engine.UploadTree(ctx, src.spec.Path, staged)
	if err != nil {
		return clipboard.Entry{}, err
	}

	return clipboard.Entry{
		Name:   src.name,
		Path:   staged,
		IsDir:  info.IsDir(),
		Size:   stats.Bytes,
		Staged: true,
	}, nil
}

// stageStdin sends standard input to CERNBox without ever writing it to local
// disk, and reports how many bytes and how many pieces it took.
//
// A pipe has no knowable length, and every way of sending bytes to reva needs one
// up front: the PUT handler parses Content-Length and answers 400 without it, and
// the TUS endpoint does not advertise creation-defer-length, so an upload of
// deferred size is refused there too.
//
// So the stream is read a chunk at a time and each chunk is sent as its own
// object, with a length that is known by the time it goes out. Memory stays at one
// chunk however large the stream is, and paste joins the pieces back up.
//
// The obvious alternative — spool to a temporary file to learn the length, which
// is what this used to do — needs as much free local disk as the stream is large.
// On lxplus that is exactly what one does not have, and it is a second full write
// of every byte for no purpose.
//
// A stream that fits in a single chunk is stored as one plain object instead, so
// the common case of a small pipe stays an ordinary entry.
func (a *App) stageStdin(ctx context.Context, base string, opts copyOptions) (int64, int, error) {
	chunk, err := a.chunkSize()
	if err != nil {
		return 0, 0, err
	}

	buf := make([]byte, chunk)
	n, readErr := io.ReadFull(a.in(), buf)
	if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
		if err := a.putBytes(ctx, base, buf[:n]); err != nil {
			return 0, 0, err
		}
		return int64(n), 0, nil
	}
	if readErr != nil {
		return 0, 0, cberr.Wrap(cberr.KindOther, "read standard input", "", readErr)
	}

	// Bigger than one chunk. The pieces go in a directory of their own so that
	// releasing them later is one recursive delete rather than one request each.
	if err := a.client.Mkdir(ctx, base, true); err != nil {
		return 0, 0, err
	}
	a.out.Msg("Sending in %s pieces...", output.HumanSize(chunk))

	total, parts := int64(0), 0
	for {
		if err := a.putBytes(ctx, clipboard.PartPath(base, parts), buf[:n]); err != nil {
			return 0, 0, err
		}
		total += int64(n)
		parts++

		n, readErr = io.ReadFull(a.in(), buf)
		switch readErr {
		case nil, io.ErrUnexpectedEOF:
			// A full chunk, or the short final one: send it on the next pass.
		case io.EOF:
			return total, parts, nil
		default:
			return 0, 0, cberr.Wrap(cberr.KindOther, "read standard input", "", readErr)
		}
	}
}

// putBytes uploads a buffer already in memory. It stays retryable, because the
// bytes are still there to be sent again.
func (a *App) putBytes(ctx context.Context, p string, b []byte) error {
	return a.client.Upload(ctx, p, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	}, int64(len(b)), "")
}

// chunkSize is how much of a stream is sent per request.
func (a *App) chunkSize() (int64, error) {
	n, err := ParseSize(a.cfg.Transfer.ChunkSize)
	if err != nil {
		return 0, cberr.Usagef("%v", err)
	}
	if n <= 0 {
		n = 8 << 20
	}
	return n, nil
}

// streamEntry writes an entry's contents to w, joining its pieces when it was
// streamed in. Nothing is buffered beyond what io.Copy uses, so this is how a
// streamed copy reaches a pipe, a file or another CERNBox path without ever being
// whole in one place.
func (a *App) streamEntry(ctx context.Context, e clipboard.Entry, w io.Writer) error {
	if e.Parts == 0 {
		return a.streamOne(ctx, e, e.Path, w)
	}
	for i := range e.Parts {
		if err := a.streamOne(ctx, e, clipboard.PartPath(e.Path, i), w); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) streamOne(ctx context.Context, e clipboard.Entry, p string, w io.Writer) error {
	body, _, err := a.client.Download(ctx, p, 0)
	if err != nil {
		return a.pasteError(err, e)
	}
	defer body.Close()
	if _, err := io.Copy(w, body); err != nil {
		return cberr.Wrap(cberr.KindOther, "paste", e.Name, err)
	}
	return nil
}

// looksLikeCERNBoxMount reports whether a local path is plausibly the FUSE mount
// of a CERNBox space. It only ever drives a hint, never a decision.
func looksLikeCERNBoxMount(p string) bool {
	return strings.HasPrefix(p, "/eos/")
}

// ── paste ────────────────────────────────────────────────────────────────────

func (a *App) clipboardPaste(ctx context.Context, args []string, opts pasteOptions) error {
	if err := clipboard.ValidateSlot(opts.slot); err != nil {
		return cberr.Usagef("%v", err)
	}

	root, err := a.clipboardRoot(ctx)
	if err != nil {
		return err
	}
	m, err := a.readSlot(ctx, root, opts.slot)
	if err != nil {
		return err
	}
	if len(m.Entries) == 0 {
		return cberr.New(cberr.KindNotFound, "paste", opts.slot, "the clipboard slot is empty")
	}
	if m.Expired(time.Now()) {
		// The bytes are evidently still here, since the manifest was readable.
		// Refusing to hand over data that is sitting right there would be
		// unhelpful; saying it is stale is not.
		a.out.Warn("this copy expired on %s and may be removed at any time",
			m.Expires.Local().Format(expiryLayout))
	}

	dest := "."
	if len(args) == 1 {
		dest = args[0]
	}

	// A live handover is a different thing from a stored copy: there is a process
	// on the other side waiting to be told somebody arrived, and nothing to
	// download until it has been.
	if m.IsStream() {
		if dest != "-" {
			if spec, err := pathspec.ParseTransfer(dest); err == nil && spec.IsRemote() {
				return cberr.Usagef(
					"%s is sending this live, so it can only be pasted to your computer "+
						"or to standard output. Nothing is stored in CERNBox to copy from.", m.Origin)
			}
		}
		return a.streamPaste(ctx, root, m, dest, dest == "-", opts)
	}

	if dest == "-" {
		return a.pasteToStdout(ctx, m)
	}

	spec, err := pathspec.ParseTransfer(dest)
	if err != nil {
		return cberr.Usagef("%v", err)
	}
	if spec.IsRemote() {
		return a.pasteRemote(ctx, m, spec, opts)
	}
	return a.pasteLocal(ctx, m, spec, opts)
}

// pasteLocal downloads the slot's contents to the local filesystem.
func (a *App) pasteLocal(ctx context.Context, m *clipboard.Manifest, spec pathspec.Spec, opts pasteOptions) error {
	dest := spec.Path

	// A trailing slash is the user saying "into a directory", so make one if it
	// is not there. Without the slash the rule is cp's: an existing directory
	// means "into it", anything else names the item.
	if spec.TrailingSlash {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return cberr.Wrap(cberr.KindOther, "create", dest, err)
		}
	}
	st, statErr := os.Stat(dest)
	destIsDir := statErr == nil && st.IsDir()

	targets, err := pasteTargets(m, dest, destIsDir, filepath.Join)
	if err != nil {
		return err
	}

	// Collisions are refused up front rather than per file, so a paste of several
	// items either happens or does not, instead of stopping halfway.
	if !opts.force {
		var clashes []string
		for _, t := range targets {
			if _, err := os.Stat(t); err == nil {
				clashes = append(clashes, t)
			}
		}
		if len(clashes) > 0 {
			return cberr.New(cberr.KindConflict, "paste", strings.Join(clashes, ", "),
				"already exists: pass -f to overwrite")
		}
	}

	bar := a.newMeter(pasteLabel(m), manifestTotal(m))
	defer bar.Stop()

	engine, err := a.transferEngineWith(transferFlags{
		recursive: true, // whatever is on the clipboard is pasted whole
		force:     true, // collisions were decided above
		verify:    opts.verify,
		noArchive: opts.noArchive,
		jobs:      opts.jobs,
	}, newEngineProgress(bar).progressFunc())
	if err != nil {
		return err
	}

	var total int64
	for i, e := range m.Entries {
		bar.SetLabel(e.Name)
		// A streamed entry is a run of pieces rather than one object, so the
		// transfer engine cannot fetch it: it is joined on the way to disk.
		if e.Parts > 0 {
			n, err := a.pasteStreamedToFile(ctx, e, targets[i], bar)
			if err != nil {
				return err
			}
			total += n
			continue
		}
		stats, err := engine.DownloadTree(ctx, e.Path, targets[i])
		if err != nil {
			return a.pasteError(err, e)
		}
		total += stats.Bytes
	}
	bar.Stop()

	a.out.Msg("Pasted %s (%s) into %s", itemCount(len(m.Entries)), proseSize(total), dest)
	a.reportSlotSurvives(m)
	if a.out.Format() == output.FormatJSON {
		return a.out.Object(m)
	}
	return nil
}

// pasteRemote copies the slot's contents to another CERNBox path, which the
// server does without the data passing through the client at all.
func (a *App) pasteRemote(ctx context.Context, m *clipboard.Manifest, spec pathspec.Spec, opts pasteOptions) error {
	dest, err := a.resolveSpec(ctx, spec)
	if err != nil {
		return err
	}
	if spec.TrailingSlash {
		if err := a.client.Mkdir(ctx, dest, true); err != nil {
			return err
		}
	}
	info, statErr := a.client.Stat(ctx, dest)
	destIsDir := statErr == nil && info.IsDir

	targets, err := pasteTargets(m, dest, destIsDir, path.Join)
	if err != nil {
		return err
	}

	if !opts.force {
		var clashes []string
		for _, t := range targets {
			if _, err := a.client.Stat(ctx, t); err == nil {
				clashes = append(clashes, t)
			}
		}
		if len(clashes) > 0 {
			return cberr.New(cberr.KindConflict, "paste", strings.Join(clashes, ", "),
				"already exists: pass -f to overwrite")
		}
	}

	bar := a.newMeter(pasteLabel(m), manifestTotal(m))
	defer bar.Stop()

	streamed := false
	for i, e := range m.Entries {
		bar.SetLabel(e.Name)
		// A server-side COPY cannot join pieces, so a streamed entry is pulled and
		// pushed back in one pass instead. It is the one case where a paste inside
		// CERNBox does move data.
		if e.Parts > 0 {
			streamed = true
			if err := a.pasteStreamedToRemote(ctx, e, targets[i], bar); err != nil {
				return err
			}
			continue
		}
		if err := a.client.Copy(ctx, e.Path, targets[i], opts.force); err != nil {
			return a.pasteError(err, e)
		}
	}

	if streamed {
		bar.Stop()
		a.out.Msg("Pasted %s (%s) into %s", itemCount(len(m.Entries)), proseSize(m.Size()), dest)
		a.reportSlotSurvives(m)
		if a.out.Format() == output.FormatJSON {
			return a.out.Object(m)
		}
		return nil
	}

	a.out.Msg("Pasted %s (%s) into %s. The server made the copy, so nothing was transferred.",
		itemCount(len(m.Entries)), proseSize(m.Size()), dest)
	a.reportSlotSurvives(m)
	if a.out.Format() == output.FormatJSON {
		return a.out.Object(m)
	}
	return nil
}

// pasteToStdout writes a single item to standard output, which is the receiving
// half of "tar cz dir | cernbox copy -".
func (a *App) pasteToStdout(ctx context.Context, m *clipboard.Manifest) error {
	if len(m.Entries) != 1 {
		return cberr.Usagef("the clipboard holds %s: standard output takes one at a time",
			itemCount(len(m.Entries)))
	}
	e := m.Entries[0]
	if e.IsDir {
		return cberr.Usagef("%s is a directory: paste it to a path rather than to standard output", e.Name)
	}

	bar := a.newMeter(e.Name, e.Size)
	defer bar.Stop()
	return a.streamEntry(ctx, e, bar.Writer(a.stdout))
}

// pasteStreamedToFile joins a streamed entry's pieces into a local file.
//
// The pieces go straight through to the file as they arrive, so this needs room
// for the result and nothing more — the same property that let the copy side
// avoid a temporary file.
func (a *App) pasteStreamedToFile(ctx context.Context, e clipboard.Entry, target string, bar *meter) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, cberr.Wrap(cberr.KindOther, "create", filepath.Dir(target), err)
	}

	// Written beside the destination and renamed at the end, as the transfer
	// engine does: a reader watching the target never sees a half-joined file.
	part := target + ".part"
	f, err := os.Create(part)
	if err != nil {
		return 0, cberr.Wrap(cberr.KindOther, "create", part, err)
	}

	err = a.streamEntry(ctx, e, bar.Writer(f))
	closeErr := f.Close()
	if err == nil && closeErr != nil {
		err = cberr.Wrap(cberr.KindOther, "write", part, closeErr)
	}
	if err != nil {
		os.Remove(part)
		return 0, err
	}

	if err := os.Rename(part, target); err != nil {
		return 0, cberr.Wrap(cberr.KindOther, "write", target, err)
	}
	return e.Size, nil
}

// pasteStreamedToRemote rebuilds a streamed entry at another CERNBox path.
//
// The pieces are pulled and pushed back through a pipe, so the whole thing never
// exists anywhere but in flight. The manifest knows the total length, which is
// what lets the PUT carry the Content-Length reva requires even though nothing
// here ever holds the bytes.
func (a *App) pasteStreamedToRemote(ctx context.Context, e clipboard.Entry, target string, bar *meter) error {
	pr, pw := io.Pipe()
	go func() {
		// Closing with the error is what makes a failed download surface as a
		// failed upload rather than as a silently truncated file.
		pw.CloseWithError(a.streamEntry(ctx, e, bar.Writer(pw)))
	}()
	// Closing the read end unblocks the writer if the upload gives up first.
	defer pr.Close()

	return a.client.UploadStream(ctx, target, pr, e.Size)
}

// pasteTargets decides where each entry lands, following cp's rule: an existing
// directory means "into it", anything else names the single item.
func pasteTargets(m *clipboard.Manifest, dest string, destIsDir bool, join func(...string) string) ([]string, error) {
	if destIsDir {
		out := make([]string, 0, len(m.Entries))
		for _, e := range m.Entries {
			out = append(out, join(dest, e.Name))
		}
		return out, nil
	}
	if len(m.Entries) != 1 {
		return nil, cberr.Usagef("the clipboard holds %s, so %s has to be an existing directory",
			itemCount(len(m.Entries)), dest)
	}
	return []string{dest}, nil
}

// pasteError explains a failure to read what the clipboard points at.
//
// A referenced entry is the user's own file, which may have been moved or
// deleted since the copy was made. The raw "no such file" names the clipboard's
// idea of the path and says nothing about why it is stale, so it is worth
// rewriting once here.
func (a *App) pasteError(err error, e clipboard.Entry) error {
	if cberr.KindOf(err) != cberr.KindNotFound {
		return err
	}
	if e.Staged {
		return cberr.New(cberr.KindNotFound, "paste", e.Name,
			"the copy is gone. It may have expired, or been cleared from another computer.")
	}
	return cberr.New(cberr.KindNotFound, "paste", e.Path,
		fmt.Sprintf("the clipboard points here, but %s has been moved or deleted since it was copied", e.Name))
}

// reportSlotSurvives says that pasting did not consume the clipboard, which is
// the one thing about this feature a user is most likely to assume wrongly.
func (a *App) reportSlotSurvives(m *clipboard.Manifest) {
	if !m.HasStaged() {
		a.out.Msg("Still on the clipboard. Paste it again anywhere, or 'cernbox clipboard clear' to forget it.")
		return
	}
	a.out.Msg("Still on the clipboard, using %s. Free it with 'cernbox clipboard clear%s'.",
		proseSize(m.StagedSize()), slotArg(m.Slot))
}

// ── clipboard list and clear ─────────────────────────────────────────────────

func (a *App) clipboardList(ctx context.Context) error {
	root, err := a.clipboardRoot(ctx)
	if err != nil {
		return err
	}
	a.collectExpired(ctx, root)

	manifests, err := a.readSlots(ctx, root)
	if err != nil {
		return err
	}
	if len(manifests) == 0 {
		a.out.Msg("The clipboard is empty. Use 'cernbox copy PATH' to put something on it.")
	}

	now := time.Now()
	table := output.Table{
		Headers: []string{"SLOT", "CONTENTS", "SIZE", "ORIGIN", "COPIED", "EXPIRES"},
		Items:   manifests,
	}
	for _, m := range manifests {
		table.Rows = append(table.Rows, []string{
			m.Slot,
			contentsColumn(m),
			output.HumanSize(m.Size()),
			m.Origin.String(),
			agoColumn(m.Created, now),
			manifestExpiry(m),
		})
	}
	return a.out.Render(table)
}

func (a *App) clipboardClear(ctx context.Context, slots []string, all bool) error {
	root, err := a.clipboardRoot(ctx)
	if err != nil {
		return err
	}

	switch {
	case all && len(slots) > 0:
		return cberr.Usagef("--all clears every slot, so it cannot be combined with a slot name")
	case all:
		manifests, err := a.readSlots(ctx, root)
		if err != nil {
			return err
		}
		slots = make([]string, 0, len(manifests))
		for _, m := range manifests {
			slots = append(slots, m.Slot)
		}
		if len(slots) == 0 {
			a.out.Msg("The clipboard is already empty.")
			return nil
		}
	case len(slots) == 0:
		slots = []string{clipboard.DefaultSlot}
	}

	var freed int64
	cleared := 0
	for _, slot := range slots {
		if err := clipboard.ValidateSlot(slot); err != nil {
			return cberr.Usagef("%v", err)
		}
		// Reading the manifest first is what makes it possible to say how much
		// quota comes back, and to know whether any did.
		if m, err := a.readSlot(ctx, root, slot); err == nil {
			freed += m.StagedSize()
		} else if cberr.KindOf(err) != cberr.KindNotFound {
			return err
		}

		removed, err := a.removeIfPresent(ctx, clipboard.SlotDir(root, slot))
		if err != nil {
			return err
		}
		if !removed {
			a.out.Msg("The %q clipboard slot is already empty.", slot)
			continue
		}
		cleared++
		a.out.Msg("Cleared the %q clipboard slot.", slot)
	}

	if freed > 0 {
		a.out.Msg("Freed %s. The files are in your trash; 'cernbox trash purge' frees the quota.",
			proseSize(freed))
	}
	if a.out.Format() == output.FormatJSON {
		return a.out.Object(map[string]any{"cleared": cleared, "freed_bytes": freed})
	}
	return nil
}

// ── storage ──────────────────────────────────────────────────────────────────

// clipboardRoot is where the clipboard lives: the caller's home space, which is
// the one space every account has and the one both machines can agree on
// without being told.
func (a *App) clipboardRoot(ctx context.Context) (string, error) {
	home, err := a.client.ResolveSpace(ctx, pathspec.HomeAlias)
	if err != nil {
		return "", err
	}
	return path.Join(home, clipboard.Dir), nil
}

// readSlot reads one slot's manifest.
func (a *App) readSlot(ctx context.Context, root, slot string) (*clipboard.Manifest, error) {
	p := clipboard.ManifestPath(root, slot)

	body, _, err := a.client.Download(ctx, p, 0)
	if err != nil {
		if cberr.KindOf(err) == cberr.KindNotFound {
			return nil, cberr.New(cberr.KindNotFound, "read the clipboard", slot,
				"nothing has been copied here yet")
		}
		return nil, err
	}
	defer body.Close()

	b, err := io.ReadAll(io.LimitReader(body, clipboard.MaxManifestSize))
	if err != nil {
		return nil, cberr.Wrap(cberr.KindOther, "read the clipboard", slot, err)
	}
	m, err := clipboard.Decode(b)
	if err != nil {
		return nil, cberr.Wrap(cberr.KindOther, "read the clipboard", slot, err)
	}
	if m.Slot == "" {
		m.Slot = slot
	}
	return m, nil
}

// readSlots reads every slot, in name order. A slot whose manifest cannot be
// read is reported and skipped rather than failing the listing: one damaged slot
// should not hide the others.
func (a *App) readSlots(ctx context.Context, root string) ([]*clipboard.Manifest, error) {
	entries, err := a.client.List(ctx, root)
	if err != nil {
		if cberr.KindOf(err) == cberr.KindNotFound {
			return nil, nil // nothing has ever been copied
		}
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	var out []*clipboard.Manifest
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		m, err := a.readSlot(ctx, root, e.Name)
		if err != nil {
			a.out.Warn("skipping the %q clipboard slot: %v", e.Name, err)
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// priorSlot reports what a slot holds before a copy replaces it, together with
// the manifest's ETag. Both are empty when the slot has never been written.
//
// A manifest that does not decode is treated as an empty slot rather than an
// error: a copy is about to replace it anyway, and refusing would leave the user
// stuck with a slot they cannot overwrite.
func (a *App) priorSlot(ctx context.Context, root, slot string) (*clipboard.Manifest, string, error) {
	info, err := a.client.Stat(ctx, clipboard.ManifestPath(root, slot))
	if err != nil {
		if cberr.KindOf(err) == cberr.KindNotFound {
			return nil, "", nil
		}
		return nil, "", err
	}
	m, err := a.readSlot(ctx, root, slot)
	if err != nil {
		return nil, info.ETag, nil
	}
	return m, info.ETag, nil
}

// writeSlot stores a manifest, creating the slot directory if needed.
//
// ifMatch is the ETag the caller believes the manifest still carries, empty when
// there was none. A mismatch means another machine copied to this slot in the
// meantime, and overwriting would leave that copy's staged bytes orphaned in the
// user's quota with nothing referencing them.
func (a *App) writeSlot(ctx context.Context, root string, m *clipboard.Manifest, ifMatch string) error {
	b, err := m.Encode()
	if err != nil {
		return cberr.Wrap(cberr.KindOther, "write the clipboard", m.Slot, err)
	}
	p := clipboard.ManifestPath(root, m.Slot)
	if err := a.client.Mkdir(ctx, path.Dir(p), true); err != nil {
		return err
	}
	open := func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	}

	if ifMatch == "" {
		// Nothing to be conditional on. reva ignores If-None-Match, so two
		// machines writing a slot for the very first time at the same moment
		// cannot be told apart; the origin recorded in the manifest is what makes
		// the result legible afterwards.
		return a.client.Upload(ctx, p, open, int64(len(b)), "")
	}

	err = a.client.UploadIfUnchanged(ctx, p, open, int64(len(b)), ifMatch)
	if cberr.KindOf(err) == cberr.KindConflict {
		return cberr.New(cberr.KindConflict, "copy", m.Slot,
			fmt.Sprintf("another computer copied to this slot at the same time. "+
				"Run 'cernbox clipboard list' to see what it holds, then copy again%s",
				slotHint(m.Slot)))
	}
	return err
}

// discardUnreferenced deletes what the previous copy staged and the new one does
// not, which is how a slot that is replaced over and over stops accumulating.
//
// It runs after the manifest is committed, never before: deleting first would
// mean a failed copy left a manifest pointing at bytes that are gone. It is best
// effort, because by this point the copy has succeeded and failing it over
// housekeeping would be the wrong trade.
func (a *App) discardUnreferenced(ctx context.Context, prior, current *clipboard.Manifest) {
	if prior == nil {
		return
	}
	kept := make(map[string]bool, len(current.Entries))
	for _, e := range current.Entries {
		kept[e.Path] = true
	}
	for _, e := range prior.Entries {
		// Only ever the clipboard's own duplicates. A referenced entry is the
		// user's real file, and deleting it here would be data loss.
		if !e.Staged || kept[e.Path] {
			continue
		}
		if _, err := a.removeIfPresent(ctx, e.Path); err != nil {
			a.out.Warn("could not release the previous copy of %s: %v", e.Name, err)
		}
	}
}

// removeIfPresent deletes a path, reporting whether there was anything there.
// A path that is already gone is not a failure: every caller here is making sure
// it is absent, not asserting that it was present.
func (a *App) removeIfPresent(ctx context.Context, p string) (bool, error) {
	if _, err := a.client.Stat(ctx, p); err != nil {
		if cberr.KindOf(err) == cberr.KindNotFound {
			return false, nil
		}
		return false, err
	}
	if err := a.client.Remove(ctx, p); err != nil {
		if cberr.KindOf(err) == cberr.KindNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// collectExpired deletes slots that are past their expiry.
//
// Nothing runs on a schedule to do this, so it happens opportunistically on the
// commands that look at the clipboard anyway. It is best effort throughout: a
// failure to collect is not a reason to fail the command the user actually ran.
func (a *App) collectExpired(ctx context.Context, root string) {
	entries, err := a.client.List(ctx, root)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		m, err := a.readSlot(ctx, root, e.Name)
		if err != nil || !m.Expired(now) {
			continue
		}
		if _, err := a.removeIfPresent(ctx, clipboard.SlotDir(root, e.Name)); err != nil {
			continue
		}
		a.out.Msg("Removed the expired %q clipboard slot.", e.Name)
	}
}

// ── display helpers ──────────────────────────────────────────────────────────

// contentsColumn names what a slot holds, in the space of one column.
func contentsColumn(m *clipboard.Manifest) string {
	switch len(m.Entries) {
	case 0:
		return "-"
	case 1:
		return entryLabel(m.Entries[0])
	default:
		return fmt.Sprintf("%s +%d more", entryLabel(m.Entries[0]), len(m.Entries)-1)
	}
}

func entryLabel(e clipboard.Entry) string {
	if e.IsDir {
		return e.Name + "/"
	}
	return e.Name
}

// agoColumn renders how long ago something happened, which is what a reader of a
// clipboard listing actually wants to know.
func agoColumn(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func manifestExpiry(m *clipboard.Manifest) string {
	if m.Expires.IsZero() {
		return "never"
	}
	if m.Expired(time.Now()) {
		return "expired"
	}
	return m.Expires.Local().Format(expiryLayout)
}

// proseSize renders a byte count inside a sentence.
//
// output.HumanSize drops the unit below 1K because that is what ls -lh does, and
// a bare number is right in a size column. In a sentence it is not: "Copied 1
// item (21)" reads as nonsense.
func proseSize(n int64) string {
	switch {
	case n == 1:
		return "1 byte"
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	default:
		return output.HumanSize(n)
	}
}

func itemCount(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

// slotArg renders the flag needed to name a slot, and nothing at all for the
// default one, so that the hints the commands print are the commands to type.
func slotArg(slot string) string {
	if slot == "" || slot == clipboard.DefaultSlot {
		return ""
	}
	return " " + slot
}

// slotHint renders the flag needed to name a slot in a sentence, and nothing for
// the default one.
func slotHint(slot string) string {
	if slot == "" || slot == clipboard.DefaultSlot {
		return ""
	}
	return " with --slot " + slot
}

func pasteHint(slot string) string {
	if slot == clipboard.DefaultSlot {
		return "cernbox paste"
	}
	return "cernbox paste --slot " + slot
}

// workingDir is recorded on a copy so a listing can distinguish two files of the
// same name. It is not load-bearing, so a failure to read it is not an error.
func workingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// in returns the reader standard input is taken from, which is a field so that
// tests can drive "cernbox copy -" without a real pipe.
func (a *App) in() io.Reader {
	if a.stdin != nil {
		return a.stdin
	}
	return os.Stdin
}
