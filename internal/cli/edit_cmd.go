package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/cernbox/cernbox-cli/pkg/pathspec"
	"github.com/spf13/cobra"
)

const (
	// DefaultEditFolder is where a bare file name lands, relative to the home
	// space. It exists so that "cernbox edit notes.txt" has an answer to "where
	// did that go" that does not depend on where you were standing.
	DefaultEditFolder = "myfiles"

	// defaultEditInterval is how often the file is checked while the editor
	// runs. Short enough that a save reaches CERNBox while you are still
	// looking at the editor, and cheap: it stats the file, and only reads it
	// when the size or the modification time moved.
	defaultEditInterval = time.Second

	// maxEditSize refuses to open something that is not an edit. Reading the
	// whole file to hash and upload it is fine for the text files people edit
	// and wrong for a dataset, which 'get' and 'put' already handle properly.
	maxEditSize = 64 << 20
)

func newEditCmd(app *App) *cobra.Command {
	var opts editOptions

	cmd := &cobra.Command{
		Use:   "edit NAME",
		Short: "Open a file in your editor, saving it to CERNBox as you go",
		Long: "Open a file in your editor, with CERNBox keeping up with every save.\n\n" +
			"A CERNBox file is fetched to a temporary place, edited there, and written\n" +
			"back on each save. A file on this machine is edited where it lies — nothing\n" +
			"is downloaded over it — and uploaded on each save. Either way, closing the\n" +
			"editor is not a special moment: the server is already up to date.\n\n" +
			"Which one it is:\n" +
			"  notes.txt            a bare name: CERNBox, in your " + DefaultEditFolder + " folder\n" +
			"  ./notes.txt  ~/x.md  this machine\n" +
			"  file:PATH            this machine, said explicitly\n" +
			"  cb:PATH  home:PATH   CERNBox, said explicitly\n" +
			"  any other path       CERNBox if it is there, otherwise this machine\n\n" +
			"A local file is saved into the " + DefaultEditFolder + " folder under its own name, so\n" +
			"'cernbox edit ./notes.txt' needs no destination. Whichever way it goes, the\n" +
			"paths are printed before the editor opens.\n\n" +
			"The editor comes from --editor, then CERNBOX_EDITOR, then VISUAL, then EDITOR.",
		Example: "  cernbox edit notes.txt\n" +
			"  cernbox edit ./draft.md\n" +
			"  cernbox edit /eos/user/g/gdelmont/Documents/report.md\n" +
			"  cernbox edit todo.md --in Scratch\n" +
			"  cernbox edit notes.txt --no-watch",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			opts.editorGiven = cmd.Flags().Changed("editor")
			return app.runEdit(ctx, args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.in, "in", "",
		"folder a bare name goes to (default "+DefaultEditFolder+", or edit.folder)")
	cmd.Flags().StringVar(&opts.editor, "editor", "", "editor to run, instead of VISUAL or EDITOR")
	cmd.Flags().BoolVar(&opts.noWatch, "no-watch", false,
		"upload once when the editor exits, rather than on every save")
	cmd.Flags().DurationVar(&opts.interval, "interval", defaultEditInterval,
		"how often to check for a save")
	cmd.Flags().BoolVar(&opts.force, "force", false,
		"write over the file even if it changed on the server meanwhile")
	return cmd
}

type editOptions struct {
	in          string
	editor      string
	editorGiven bool
	noWatch     bool
	interval    time.Duration
	force       bool
}

// runEdit is the whole flow: work out the path, fetch it, run the editor, and
// keep the server in step with what the editor writes.
func (a *App) runEdit(ctx context.Context, arg string, opts editOptions) error {
	target, err := a.resolveEditTarget(ctx, arg, opts.in)
	if err != nil {
		return err
	}

	editor, err := a.editorCommand(opts)
	if err != nil {
		return err
	}

	s := &editSession{app: a, target: target, force: opts.force}
	if err := s.fetch(ctx); err != nil {
		return err
	}
	defer s.cleanup()

	// Which file, and where it goes. For a local file these are two different
	// places, and saying so is the difference between a useful command and a
	// surprising one.
	if target.inPlace {
		a.out.Msg("Editing %s", target.local)
		a.out.Msg("Saving to %s", target.remote)
		if s.overwrites {
			a.out.Warn("replacing the %s that is already there", target.remote)
		}
	} else {
		a.out.Msg("Editing %s", target.remote)
		if s.isNew {
			a.out.Msg("It does not exist yet; it will be created when you save.")
		}
	}

	if err := s.run(ctx, editor, opts); err != nil {
		return err
	}
	return s.finish(ctx)
}

// editTarget is what a session works on: a file to put in front of the editor,
// and the CERNBox path that keeps up with it.
type editTarget struct {
	// remote is the CERNBox path saves go to.
	remote string
	// local is the file the editor opens. For a remote target this is a working
	// copy in a temporary directory; for a local one it is the user's own file.
	local string
	// inPlace marks a local file, which is edited where it lies and must never
	// be deleted or replaced by a download.
	inPlace bool
}

// resolveEditTarget works out whether the argument names a file here or in
// CERNBox, and where saves go.
//
// The rule, in order: an explicit marker wins; "./", "../" and "~/" mean here; a
// bare name with no path in it means CERNBox, in the edit folder, which is the
// case this command exists for; and any other path is looked for in CERNBox
// first, then here. Looking is the only honest way to settle "/eos/user/g/x",
// which on lxplus is both a CERNBox path and a real local mount — and whichever
// way it goes, the answer is printed before the editor opens.
func (a *App) resolveEditTarget(ctx context.Context, arg, in string) (editTarget, error) {
	if rest, ok := strings.CutPrefix(arg, pathspec.LocalPrefix); ok {
		if rest == "" {
			return editTarget{}, cberr.Usagef("%q has no path after %q", arg, pathspec.LocalPrefix)
		}
		return a.localEditTarget(ctx, rest, in)
	}

	switch {
	case strings.HasPrefix(arg, pathspec.RemotePrefix):
		remote, err := a.resolve(ctx, arg)
		return editTarget{remote: remote}, err
	case isBareEditName(arg):
		// The headline case: one known folder, so that "cernbox edit notes.txt"
		// means the same file from any directory.
		remote, err := a.remoteInEditFolder(ctx, arg, in)
		return editTarget{remote: remote}, err
	}

	if looksLocal(arg) {
		return a.localEditTarget(ctx, arg, in)
	}

	// A path with no marker. CERNBox first, because this is a CERNBox command.
	remote, err := a.resolve(ctx, arg)
	if err != nil {
		return editTarget{}, err
	}
	if _, err := a.client.Stat(ctx, remote); err == nil {
		return editTarget{remote: remote}, nil
	} else if cberr.KindOf(err) != cberr.KindNotFound {
		return editTarget{}, err
	}

	// Not in CERNBox. If it is here, it is what the user meant.
	if _, err := os.Stat(arg); err == nil {
		return a.localEditTarget(ctx, arg, in)
	}
	return editTarget{remote: remote}, nil
}

// localEditTarget prepares editing a file on this machine, saving it into the
// edit folder under its own name.
func (a *App) localEditTarget(ctx context.Context, p, in string) (editTarget, error) {
	abs, err := filepath.Abs(expandHome(p))
	if err != nil {
		return editTarget{}, cberr.Usagef("%v", err)
	}
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		return editTarget{}, cberr.Usagef("%s is a directory", abs)
	}

	remote, err := a.remoteInEditFolder(ctx, filepath.Base(abs), in)
	if err != nil {
		return editTarget{}, err
	}
	return editTarget{remote: remote, local: abs, inPlace: true}, nil
}

// remoteInEditFolder is where a name with no CERNBox path of its own goes.
func (a *App) remoteInEditFolder(ctx context.Context, name, in string) (string, error) {
	folder := in
	if folder == "" {
		folder = a.cfg.Edit.Folder
	}
	if folder == "" {
		folder = DefaultEditFolder
	}
	base, err := a.resolve(ctx, folder)
	if err != nil {
		return "", err
	}
	return path.Join(base, name), nil
}

// looksLocal reports whether an argument says plainly that it is a path on this
// machine. Shells expand "~" themselves, so a literal one only survives when it
// was quoted — in which case it still meant here.
func looksLocal(arg string) bool {
	return strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") ||
		arg == "." || arg == ".." || strings.HasPrefix(arg, "~/") || arg == "~"
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
}

// isBareEditName reports whether arg is a plain file name rather than a path.
//
// ':' counts as a path character because it introduces a space alias
// ("home:Documents"), so a name containing one is never bare here.
func isBareEditName(arg string) bool {
	switch arg {
	case "", ".", "..":
		return false
	}
	return !strings.ContainsAny(arg, "/:")
}

// editorCommand works out what to run, in the order a user would expect: the
// flag they just typed, then this CLI's own setting, then the two variables
// every other tool reads, then vi — which is what git and crontab fall back to.
func (a *App) editorCommand(opts editOptions) ([]string, error) {
	candidates := []string{opts.editor, a.cfg.Edit.Command, os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi"}

	var chosen string
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			chosen = strings.TrimSpace(c)
			break
		}
	}

	// Split rather than handed to a shell: the file name never reaches an
	// interpreter this way, and "code -w" or "emacs -nw" still work. An editor
	// command needing shell quoting has to be wrapped in a script.
	fields := strings.Fields(chosen)
	if len(fields) == 0 {
		return nil, cberr.Usagef("no editor configured: set EDITOR, or pass --editor")
	}

	// An editor without a terminal cannot be used, and a scheduled job that
	// starts one waits for a keystroke nobody will type. An explicit --editor is
	// taken as knowing better, which is also how this is tested.
	if !opts.editorGiven && !a.hasTerminal() {
		return nil, cberr.Usagef(
			"editing needs a terminal to run %s in; use 'cernbox get' and 'cernbox put' in a script", fields[0])
	}
	return fields, nil
}

func (a *App) hasTerminal() bool {
	return output.IsTerminal(a.stdinFile()) && output.IsTerminal(a.stdoutFile())
}

// ── the session ──────────────────────────────────────────────────────────────

// editSession holds one file being edited.
type editSession struct {
	app    *App
	target editTarget
	force  bool

	// dir is the temporary directory holding the working copy, and is empty when
	// the file is being edited where it lies. That emptiness is what stops
	// cleanup from ever touching a file of the user's own.
	dir   string
	isNew bool
	// overwrites records that a local file is about to replace something that is
	// already in CERNBox under the same name.
	overwrites bool

	// etag is the version on the server that the working copy came from, and
	// what every upload is conditional on. Keeping it current is what turns a
	// concurrent change into a refusal rather than a silent overwrite.
	etag string
	// sum is the hash of the content last known to be on the server, so a save
	// that changed nothing costs nothing.
	sum string
	// mtime and size are the cheap gate: hashing happens only once these move.
	mtime time.Time
	size  int64

	saves int
	// keep marks a working copy that must not be deleted, because it holds an
	// edit the server does not have.
	keep bool
}

// fetch gets the editor something to open.
//
// For a CERNBox file that is a working copy in a temporary directory. For a local
// one it is the file itself, which is deliberately *not* replaced by whatever is
// on the server: the user asked to edit what is here, and overwriting it with a
// download would be the opposite of that.
func (s *editSession) fetch(ctx context.Context) error {
	info, err := s.app.client.Stat(ctx, s.target.remote)
	switch {
	case err != nil && cberr.KindOf(err) == cberr.KindNotFound:
		s.isNew = true
	case err != nil:
		return err
	case info.IsDir:
		return cberr.Usagef("%s is a directory", s.target.remote)
	case info.Size > maxEditSize && !s.target.inPlace:
		return cberr.Usagef("%s is %s, too big to edit; use 'cernbox get' and 'cernbox put'",
			s.target.remote, output.HumanSize(info.Size))
	default:
		// Even for a local file: adopting the version that is there now means a
		// change made by somebody else *after* this point is still caught, while
		// the copy in front of the user stays the one they asked to edit.
		s.etag = info.ETag
	}

	if s.target.inPlace {
		return s.openInPlace(ctx)
	}

	dir, err := os.MkdirTemp("", "cernbox-edit-")
	if err != nil {
		return err
	}
	s.dir = dir
	// The real name, so that the editor picks the right syntax highlighting and
	// the window title says what is being edited.
	s.target.local = filepath.Join(dir, path.Base(s.target.remote))

	var data []byte
	if !s.isNew {
		body, _, err := s.app.client.Download(ctx, s.target.remote, 0)
		if err != nil {
			return err
		}
		defer body.Close()
		if data, err = io.ReadAll(io.LimitReader(body, maxEditSize+1)); err != nil {
			return err
		}
		if len(data) > maxEditSize {
			return cberr.Usagef("%s is too big to edit; use 'cernbox get' and 'cernbox put'", s.target.remote)
		}
	}

	if err := os.WriteFile(s.target.local, data, 0o600); err != nil {
		return err
	}
	s.sum = hashBytes(data)
	s.note()
	return nil
}

// openInPlace prepares a local file for editing, creating it when it is not
// there — which is what an editor does when given a name that does not exist.
//
// The file itself is never written to: whatever is in CERNBox under the same name
// is read to compare, not to install.
func (s *editSession) openInPlace(ctx context.Context) error {
	local, err := os.ReadFile(s.target.local)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(filepath.Dir(s.target.local), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(s.target.local, nil, 0o644); err != nil {
			return err
		}
		local = nil
	case err != nil:
		return err
	case len(local) > maxEditSize:
		return cberr.Usagef("%s is %s, too big to keep uploading; use 'cernbox put'",
			s.target.local, output.HumanSize(int64(len(local))))
	}

	if s.isNew {
		// Nothing there yet, so the first save creates it. An empty sum means
		// "the server has nothing", which any content differs from.
		s.sum = ""
		s.note()
		return nil
	}

	// Something is already at that name. Whether that matters depends entirely on
	// whether it is this same file — editing the same local file twice must not
	// need a flag, while quietly replacing an unrelated file must not be possible.
	remote, err := s.readRemote(ctx)
	if err != nil {
		return err
	}
	s.sum = hashBytes(remote)

	if s.sum == hashBytes(local) {
		// The same file. Nothing to upload until something changes.
		s.note()
		return nil
	}

	s.overwrites = true
	if !s.force {
		return cberr.New(cberr.KindConflict, "save to CERNBox", s.target.remote,
			fmt.Sprintf("%s already exists in CERNBox with different content. "+
				"Pass --force to replace it with %s, --in to save somewhere else, "+
				"or 'cernbox edit %s' to edit the CERNBox one instead",
				s.target.remote, s.target.local, s.target.remote))
	}
	s.note()
	return nil
}

// readRemote fetches the CERNBox side into memory. Into memory because the point
// of editing in place is that the local file is the one being edited; this is for
// comparing against, never for installing over it.
func (s *editSession) readRemote(ctx context.Context) ([]byte, error) {
	body, _, err := s.app.client.Download(ctx, s.target.remote, 0)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, maxEditSize+1))
}

// note records the cheap file facts, so that a tick can tell "untouched" from
// "worth reading" without reading anything.
func (s *editSession) note() {
	if st, err := os.Stat(s.target.local); err == nil {
		s.mtime, s.size = st.ModTime(), st.Size()
	}
}

// run starts the editor and keeps the server in step while it is up.
//
// Polling rather than inotify, for three reasons that point the same way.
//
// How an editor saves is its own business and varies: measured here, vim writes
// in place — with its default settings and with backupcopy=no — while sed -i
// replaces the file, and the "atomic save" editors (VS Code, gedit and friends)
// are known for doing the same. A watch on the file itself follows the inode, so
// it works for one editor and silently stops working for the next, or after a
// user changes one setting. Watching a path instead of a file has neither
// problem. inotify is also Linux-only and this is built for macOS too, and
// fsnotify would be the first new direct dependency in a module that has exactly
// one. Comparing the file against what was last uploaded is immune to all of it,
// costs a stat per second, and notices a save about as fast as a person can.
func (s *editSession) run(ctx context.Context, editor []string, opts editOptions) error {
	args := append(append([]string{}, editor[1:]...), s.target.local)
	cmd := exec.CommandContext(ctx, editor[0], args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.app.stdinFile(), s.app.stdoutFile(), s.app.stderrFile()
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		return cberr.Usagef("cannot run %s: %v", editor[0], err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	interval := opts.interval
	if interval <= 0 {
		interval = defaultEditInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			if err != nil {
				// The editor's own failure is worth saying out loud, but it does
				// not make the edit disappear: whatever was saved is still on
				// disk and still belongs on the server.
				s.app.out.Warn("%s exited with an error: %v", editor[0], err)
			}
			return nil
		case <-ticker.C:
			if opts.noWatch {
				continue
			}
			if err := s.syncIfChanged(ctx, false); err != nil {
				return err
			}
		}
	}
}

// finish does the last save and says what happened.
func (s *editSession) finish(ctx context.Context) error {
	// Unconditionally, ignoring the cheap gate. A file rewritten to the same
	// length inside one modification-time tick looks untouched to a stat, and on
	// a filesystem with coarse timestamps that is not far-fetched. The hash still
	// decides whether anything is uploaded, so the only cost of being sure here
	// is reading the file once.
	if err := s.syncIfChanged(ctx, true); err != nil {
		return err
	}
	switch s.saves {
	case 0:
		s.app.out.Msg("No changes.")
	case 1:
		s.app.out.Msg("Saved %s", s.target.remote)
	default:
		s.app.out.Msg("Saved %s (%d times)", s.target.remote, s.saves)
	}
	return nil
}

// syncIfChanged uploads the working copy when it differs from what the server
// has. With always set, the cheap stat gate is skipped and the content is
// compared whatever the file's timestamps say.
func (s *editSession) syncIfChanged(ctx context.Context, always bool) error {
	st, err := os.Stat(s.target.local)
	if err != nil {
		// Missing for a moment is normal: an editor saving by rename unlinks the
		// old file first. A tick that cannot see the file waits for the next one.
		return nil
	}
	if !always && st.Size() == s.size && st.ModTime().Equal(s.mtime) {
		return nil
	}
	s.mtime, s.size = st.ModTime(), st.Size()

	data, err := os.ReadFile(s.target.local)
	if err != nil {
		return nil
	}
	sum := hashBytes(data)
	if sum == s.sum {
		// Saved without changing anything, which editors do often.
		return nil
	}

	// Only now, when there is something to write: opening an editor on a new
	// file and quitting without saving should leave nothing behind.
	if s.isNew {
		if err := s.app.mkdirForEdit(ctx, s.target.remote); err != nil {
			return s.uploadFailed(err)
		}
	}

	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	if s.force || s.etag == "" {
		err = s.app.client.Upload(ctx, s.target.remote, open, int64(len(data)), "")
	} else {
		err = s.app.client.UploadIfUnchanged(ctx, s.target.remote, open, int64(len(data)), s.etag)
	}
	if err != nil {
		return s.uploadFailed(err)
	}

	s.sum = sum
	s.saves++
	s.isNew = false

	// The new ETag is what the next save will be conditional on. Without this
	// every save after the first would look like a conflict.
	if info, err := s.app.client.Stat(ctx, s.target.remote); err == nil {
		s.etag = info.ETag
	}
	return nil
}

// uploadFailed keeps the edit and explains where it is.
//
// The working copy is the only place the user's work exists, so it stays on disk
// and its path is printed. Deleting it because an upload failed would be the
// worst thing this command could do.
func (s *editSession) uploadFailed(err error) error {
	s.keep = true

	if s.target.inPlace {
		// The file is the user's own and never in danger, so the message is
		// about the upload rather than about where their work went.
		if cberr.KindOf(err) == cberr.KindConflict {
			return cberr.New(cberr.KindConflict, "save to CERNBox", s.target.remote,
				"it changed on the server while you were editing, so it was left alone. "+
					"Your file is untouched; run edit again with --force to send it anyway")
		}
		return err
	}

	if cberr.KindOf(err) == cberr.KindConflict {
		return cberr.New(cberr.KindConflict, "save to CERNBox", s.target.remote,
			fmt.Sprintf("it changed on the server while you were editing, so it was left alone. "+
				"Your version is %s — compare it, then 'cernbox put --force' it, "+
				"or run edit again with --force", s.target.local))
	}
	return fmt.Errorf("%w (your version is still %s)", err, s.target.local)
}

// cleanup removes the working copy, unless it holds something the server does
// not have.
//
// A local file is never touched: dir is empty for one, so there is nothing here
// that could delete it even if the upload failed.
func (s *editSession) cleanup() {
	if s.dir == "" {
		return
	}
	if s.keep {
		s.app.out.Warn("kept your version at %s", s.target.local)
		return
	}
	_ = os.RemoveAll(s.dir)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// mkdirForEdit makes sure the folder a new file is about to land in exists,
// since a PUT into a missing collection is refused.
func (a *App) mkdirForEdit(ctx context.Context, remote string) error {
	dir := path.Dir(remote)
	if dir == "" || dir == "/" {
		return nil
	}
	if _, err := a.client.Stat(ctx, dir); err == nil {
		return nil
	} else if cberr.KindOf(err) != cberr.KindNotFound {
		return err
	}
	return a.client.Mkdir(ctx, dir, true)
}

// stderrFile is the real terminal behind the App's error stream, or nil when
// there is none, so that an editor's messages reach the same place the user's do.
func (a *App) stderrFile() *os.File {
	if f, ok := a.stderr.(*os.File); ok {
		return f
	}
	return nil
}
