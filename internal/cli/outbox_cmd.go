package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/auth"
	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

const (
	// defaultSettle is how long a file must sit unchanged before it is considered
	// finished. A screenshot appears while it is still being written, and
	// uploading it then produces half an image.
	defaultSettle = 2 * time.Second

	// defaultSweep is how often a watch looks through the folder anyway, as a
	// safety net behind the filesystem notifications. Long, because the
	// notifications carry the ordinary case and this exists only for what they
	// miss.
	defaultSweep = time.Minute

	// settleCheck is how often the files that have had events are looked at to
	// see whether they have stopped changing. It touches only those, not the
	// whole folder, which is the point of watching rather than polling.
	settleCheck = time.Second

	// uploadedDir is where "--after move" puts a file once it is safely away.
	uploadedDir = ".uploaded"
)

func newOutboxCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "outbox",
		Short: "Upload whatever appears in a local folder",
		Long: "Treat a local folder as an outbox: anything that turns up in it is\n" +
			"uploaded to CERNBox.\n\n" +
			"It is one way, always. Nothing in CERNBox is changed except by adding to\n" +
			"it, and 'cernbox sync' is still the command for keeping two sides matching.\n\n" +
			"Folders are usually listed in the configuration file under 'outbox', each\n" +
			"with its own destination, so that 'cernbox outbox push' needs no arguments.\n" +
			"A folder can also be given on the command line with --to.",
	}
	cmd.AddCommand(newOutboxPushCmd(app), newOutboxWatchCmd(app), newOutboxStatusCmd(app))
	return cmd
}

// ── flags ────────────────────────────────────────────────────────────────────

type outboxFlags struct {
	to     string
	layout string
	after  string
	link   bool
	settle time.Duration
}

func (o *outboxFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&o.to, "to", "", "CERNBox folder to upload into, for a folder not in the configuration")
	f.StringVar(&o.layout, "layout", "", "where each file lands: flat, or date for YYYY/MM/DD folders")
	f.StringVar(&o.after, "after", "", "what to do with the local file once it is safely up: keep, move or delete")
	f.BoolVar(&o.link, "link", false, "create a public link for each upload and print it")
	f.DurationVar(&o.settle, "settle", defaultSettle,
		"how long a file must be unchanged before it counts as finished")
}

// jobs works out which folders to act on: the one named on the command line, or
// every folder in the configuration.
func (a *App) outboxJobs(args []string, flags outboxFlags) ([]outboxJob, error) {
	if len(args) == 1 {
		if flags.to == "" {
			return nil, cberr.Usagef("pass --to with the CERNBox folder to upload %s into", args[0])
		}
		job, err := newOutboxJob(OutboxFolder{
			Local:  args[0],
			Remote: flags.to,
			Layout: flags.layout,
			After:  flags.after,
			Link:   flags.link,
		})
		if err != nil {
			return nil, err
		}
		return []outboxJob{job}, nil
	}

	if flags.to != "" {
		return nil, cberr.Usagef("--to describes one folder, so name the folder it applies to")
	}
	if len(a.cfg.Outbox) == 0 {
		return nil, cberr.Usagef("no outbox folders are configured. Name one with " +
			"'cernbox outbox push DIR --to REMOTE', or list them under 'outbox' in the configuration file")
	}

	jobs := make([]outboxJob, 0, len(a.cfg.Outbox))
	for _, f := range a.cfg.Outbox {
		// Flags override the configuration for every folder, which is what makes
		// "--after keep" usable as a safety net over a config that says delete.
		if flags.layout != "" {
			f.Layout = flags.layout
		}
		if flags.after != "" {
			f.After = flags.after
		}
		if flags.link {
			f.Link = true
		}
		job, err := newOutboxJob(f)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// ── push ─────────────────────────────────────────────────────────────────────

func newOutboxPushCmd(app *App) *cobra.Command {
	var flags outboxFlags

	cmd := &cobra.Command{
		Use:   "push [DIR]",
		Short: "Upload what is waiting, then stop",
		Long: "Upload everything waiting in the outbox folders and exit.\n\n" +
			"This is the form to run from a timer: it does one pass and stops, so a\n" +
			"laptop that slept or lost its network simply catches up on the next run.\n\n" +
			"A file still being written is left for next time. Nothing is uploaded until\n" +
			"it has been unchanged for --settle, because a screenshot that appears while\n" +
			"the tool is still writing it would otherwise arrive half finished.",
		Example: "  cernbox outbox push\n" +
			"  cernbox outbox push ~/Pictures/Screenshots --to Screenshots --layout date",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			jobs, err := app.outboxJobs(args, flags)
			if err != nil {
				return err
			}
			total := outboxResult{}
			for _, job := range jobs {
				got, err := app.runOutbox(ctx, job, flags.settle)
				total.add(got)
				if err != nil {
					return err
				}
			}
			total.report(app.out)
			return nil
		},
	}

	flags.register(cmd)
	return cmd
}

// ── watch ────────────────────────────────────────────────────────────────────

func newOutboxWatchCmd(app *App) *cobra.Command {
	var flags outboxFlags
	var sweep time.Duration

	cmd := &cobra.Command{
		Use:   "watch [DIR]",
		Short: "Keep uploading as files appear",
		Long: "Watch the outbox folders and upload what appears, until interrupted.\n\n" +
			"Convenient for a session at a desk. For something that has to survive\n" +
			"sleep, a lost network and a reboot, run 'outbox push' from a timer instead:\n" +
			"a process that has to stay alive is the weaker arrangement.\n\n" +
			"It is driven by filesystem notifications, so a folder with thousands of\n" +
			"files in it is not read through on a timer. Notifications do get dropped —\n" +
			"by a full kernel queue, by network filesystems, by anything that arrived\n" +
			"before the watch started — so the folders are also looked through every\n" +
			"--sweep as a safety net.\n\n" +
			"There is nobody to sign in here, so this needs a credential that works\n" +
			"without a terminal: a Kerberos ticket, or an app token in CERNBOX_APP_TOKEN.",
		Example: "  cernbox outbox watch\n  cernbox outbox watch ~/scratch --to Scratch",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			jobs, err := app.outboxJobs(args, flags)
			if err != nil {
				return err
			}
			if err := app.checkUnattended(); err != nil {
				return err
			}

			for _, job := range jobs {
				app.out.Msg("Watching %s → %s", job.local, job.remote)
			}

			return app.watchOutbox(ctx, jobs, flags.settle, sweep)
		},
	}

	flags.register(cmd)
	cmd.Flags().DurationVar(&sweep, "sweep", defaultSweep,
		"how often to look through the folders anyway, behind the notifications")
	return cmd
}

// watchOutbox uploads as files appear.
//
// Driven by filesystem notifications rather than by looking at every file on a
// timer: a folder with thousands of screenshots in it should not be read through
// every few seconds to find the one that just arrived.
//
// Notifications are not enough on their own, though, which is why the sweep is
// still here. They are dropped when the kernel queue overflows, they often do not
// arrive at all on network filesystems, and anything that appeared before the
// watch was established was never announced. A slow full pass costs little and
// turns "silently never uploaded" into "uploaded a minute late".
//
// The settle wait is unchanged and unrelated: notifications say a file exists,
// not that whatever is writing it has finished.
func (a *App) watchOutbox(ctx context.Context, jobs []outboxJob, settle, sweep time.Duration) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		a.out.Warn("cannot watch for changes (%v), so looking every %s instead", errLine(err), sweep)
	} else {
		defer watcher.Close()
		for _, job := range jobs {
			if err := watcher.Add(job.local); err != nil {
				a.out.Warn("cannot watch %s (%v), so it will only be looked at every %s",
					job.local, errLine(err), sweep)
			}
		}
	}

	// Nil channels are never ready, so a failed watcher simply leaves the timers
	// to do the work.
	var events chan fsnotify.Event
	var watchErrs chan error
	if watcher != nil {
		events, watchErrs = watcher.Events, watcher.Errors
	}

	// Everything already there goes first, since nothing announced it.
	a.sweepOutbox(ctx, jobs, settle)

	pending := map[string]bool{}
	settleTicker := time.NewTicker(settleCheck)
	defer settleTicker.Stop()
	sweepTicker := time.NewTicker(sweep)
	defer sweepTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev := <-events:
			// Removals and renames away need nothing; a file that arrived by rename
			// is a Create, and one still being written is a Write.
			if ev.Op&(fsnotify.Create|fsnotify.Write) != 0 && !skipOutboxName(filepath.Base(ev.Name)) {
				pending[ev.Name] = true
			}

		case err := <-watchErrs:
			if err != nil {
				a.out.Warn("watching: %v", errLine(err))
			}

		case <-settleTicker.C:
			for p := range pending {
				done, err := a.tryPending(ctx, jobs, p, settle)
				if err != nil {
					a.out.Warn("%s: %v", filepath.Base(p), errLine(err))
				}
				if done || err != nil {
					delete(pending, p)
				}
			}

		case <-sweepTicker.C:
			a.sweepOutbox(ctx, jobs, settle)
		}
	}
}

// sweepOutbox makes a pass over every folder, reporting failures rather than
// giving up: a watch that exits on the first bad file is not running when it
// matters.
func (a *App) sweepOutbox(ctx context.Context, jobs []outboxJob, settle time.Duration) {
	for _, job := range jobs {
		if _, err := a.runOutbox(ctx, job, settle); err != nil {
			a.out.Warn("%s: %v", job.local, errLine(err))
		}
	}
}

// tryPending uploads one file that had an event, if it has stopped changing.
// It reports whether the file is finished with, either because it went or because
// it is no longer there.
func (a *App) tryPending(ctx context.Context, jobs []outboxJob, p string, settle time.Duration) (bool, error) {
	dir := filepath.Dir(p)
	var job *outboxJob
	for i := range jobs {
		if jobs[i].local == dir {
			job = &jobs[i]
			break
		}
	}
	if job == nil {
		// An event for something outside the folders being watched, which happens
		// for the .uploaded directory "--after move" writes into.
		return true, nil
	}

	info, err := os.Lstat(p)
	switch {
	case os.IsNotExist(err):
		return true, nil // moved or removed before it settled
	case err != nil:
		return true, err
	case info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return true, nil
	case time.Since(info.ModTime()) < settle:
		return false, nil // still being written; look again next tick
	}

	item := outboxItem{
		path: p, name: filepath.Base(p), size: info.Size(),
		modified: info.ModTime(), settled: true,
	}
	_, _, err = a.sendOutboxItem(ctx, *job, item)
	return err == nil, err
}

// checkUnattended refuses to start a long-running upload that cannot sign in.
//
// The device-code flow prints a URL and waits for somebody to visit it, which
// never happens in a service. Failing now, with the reason, beats starting
// cheerfully and failing on the first upload an hour later.
func (a *App) checkUnattended() error {
	if a.hasTerminal() {
		return nil
	}
	if a.chain == nil {
		return nil
	}
	// A provider that is available and needs nobody present. The device flow
	// prints a URL and waits for somebody to visit it, so it is never one of these.
	//
	// Basic is the awkward case, and Available() cannot answer it: that method
	// reports true when a password *could* be obtained, including by prompting for
	// one. So it counts here only when the password is already in the environment,
	// which is how a service would be given one.
	unattended := map[string]bool{
		auth.MethodKerberos: true,
		auth.MethodAppToken: true,
		auth.MethodToken:    true,
		auth.MethodBasic:    os.Getenv("CERNBOX_PASSWORD") != "",
	}
	for _, p := range a.chain.Providers() {
		if unattended[p.Name()] && p.Available(context.Background()) {
			return nil
		}
	}
	return cberr.Usagef("watching needs a credential that works without a terminal: " +
		"a Kerberos ticket, or an app token in CERNBOX_APP_TOKEN")
}

// ── status ───────────────────────────────────────────────────────────────────

func newOutboxStatusCmd(app *App) *cobra.Command {
	var flags outboxFlags

	cmd := &cobra.Command{
		Use:   "status [DIR]",
		Short: "Show what is waiting to be uploaded",
		Long: "List what the outbox would upload, without uploading anything.\n\n" +
			"This is how you check that an outbox is working, and what it is waiting on:\n" +
			"a file still being written is listed as such rather than left unexplained.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			jobs, err := app.outboxJobs(args, flags)
			if err != nil {
				return err
			}

			type row struct {
				Local  string `json:"local"`
				Remote string `json:"remote"`
				Size   int64  `json:"size"`
				State  string `json:"state"`
			}
			var rows []row
			var dirs int
			for _, job := range jobs {
				scan, err := scanOutbox(job, flags.settle, time.Now())
				if err != nil {
					return err
				}
				dirs += scan.dirs
				for _, it := range scan.items {
					state := "waiting"
					if !it.settled {
						state = "still being written"
					} else if done, err := app.alreadyThere(ctx, job, it); err == nil && done {
						state = "already up"
					}
					rows = append(rows, row{
						Local: it.path, Remote: job.remoteFor(it), Size: it.size, State: state,
					})
				}
			}

			table := output.Table{Headers: []string{"STATE", "SIZE", "LOCAL", "GOES TO"}, Items: rows}
			for _, r := range rows {
				table.Rows = append(table.Rows, []string{
					r.State, output.HumanSize(r.Size), r.Local, r.Remote,
				})
			}
			if err := app.out.Render(table); err != nil {
				return err
			}
			if len(rows) == 0 {
				app.out.Msg("Nothing is waiting.")
			}
			if dirs > 0 {
				app.out.Warn("%s skipped: an outbox uploads files, not folders.",
					itemsPlural(dirs, "folder"))
			}
			return nil
		},
	}

	flags.register(cmd)
	return cmd
}

// ── the work ─────────────────────────────────────────────────────────────────

// outboxJob is one folder, resolved and checked.
type outboxJob struct {
	local  string
	remote string
	layout string
	after  string
	link   bool
}

func newOutboxJob(f OutboxFolder) (outboxJob, error) {
	if f.Local == "" || f.Remote == "" {
		return outboxJob{}, cberr.Usagef("an outbox folder needs both a local path and a CERNBox folder")
	}
	local, err := filepath.Abs(expandHome(f.Local))
	if err != nil {
		return outboxJob{}, cberr.Usagef("%v", err)
	}

	job := outboxJob{local: local, remote: f.Remote, layout: f.Layout, after: f.After, link: f.Link}
	if job.layout == "" {
		job.layout = "flat"
	}
	if job.after == "" {
		// Keeping the file is the only choice that cannot lose anything, so it is
		// what you get unless the configuration says otherwise in writing.
		job.after = "keep"
	}
	switch job.layout {
	case "flat", "date":
	default:
		return outboxJob{}, cberr.Usagef("unknown layout %q: want flat or date", job.layout)
	}
	switch job.after {
	case "keep", "move", "delete":
	default:
		return outboxJob{}, cberr.Usagef("unknown --after %q: want keep, move or delete", job.after)
	}
	return job, nil
}

// outboxScan is what one folder holds.
type outboxScan struct {
	items []outboxItem
	// dirs counts subdirectories, which an outbox does not upload. Counted rather
	// than ignored: somebody who drops a folder in and is told nothing would
	// reasonably believe it had been sent.
	dirs int
}

// outboxItem is one local file that might need uploading.
type outboxItem struct {
	path     string
	name     string
	size     int64
	modified time.Time
	settled  bool
}

// remoteFor is where a file goes, which for the date layout is taken from the
// file's own timestamp rather than from today: a screenshot belongs to the day it
// was taken, whenever it happens to be uploaded.
func (j outboxJob) remoteFor(it outboxItem) string {
	if j.layout == "date" {
		d := it.modified
		return path.Join(j.remote, fmt.Sprintf("%04d/%02d/%02d", d.Year(), int(d.Month()), d.Day()), it.name)
	}
	return path.Join(j.remote, it.name)
}

// scanOutbox lists what is in the folder, marking which files have stopped
// changing.
//
// The settle test is a comparison against the clock rather than a second look at
// the file: a file still being written keeps its modification time current, so
// one stat is enough to tell. That also means nothing has to be remembered
// between runs.
// An outbox is one folder deep on purpose. Deciding that a whole tree has
// finished being written is a much harder question than deciding it about one
// file, and the folders people drop things into are flat.
func scanOutbox(job outboxJob, settle time.Duration, now time.Time) (outboxScan, error) {
	entries, err := os.ReadDir(job.local)
	if err != nil {
		return outboxScan{}, cberr.Wrap(cberr.KindNotFound, "read the outbox folder", job.local, err)
	}

	var scan outboxScan
	for _, e := range entries {
		if skipOutboxName(e.Name()) {
			continue
		}
		if e.IsDir() {
			scan.dirs++
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Symbolic links are not followed: one pointing at / would mean uploading
		// a home directory, and an outbox should only ever send what was put in it.
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		scan.items = append(scan.items, outboxItem{
			path:     filepath.Join(job.local, e.Name()),
			name:     e.Name(),
			size:     info.Size(),
			modified: info.ModTime(),
			settled:  now.Sub(info.ModTime()) >= settle,
		})
	}
	sort.Slice(scan.items, func(i, j int) bool { return scan.items[i].name < scan.items[j].name })
	return scan, nil
}

// skipOutboxName leaves alone the names that are not finished files.
func skipOutboxName(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
		return true
	}
	for _, ext := range []string{".part", ".crdownload", ".tmp", ".partial", ".download", ".swp"} {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			return true
		}
	}
	return false
}

// outboxResult counts a pass, so that a run says what it did.
type outboxResult struct {
	uploaded int
	skipped  int
	pending  int
	failed   int
	dirs     int
	bytes    int64
}

func (r *outboxResult) add(o outboxResult) {
	r.uploaded += o.uploaded
	r.skipped += o.skipped
	r.pending += o.pending
	r.failed += o.failed
	r.dirs += o.dirs
	r.bytes += o.bytes
}

func (r outboxResult) report(out *output.Writer) {
	switch {
	case r.uploaded == 1:
		out.Msg("Uploaded 1 file (%s).", output.HumanSize(r.bytes))
	case r.uploaded > 1:
		out.Msg("Uploaded %d files (%s).", r.uploaded, output.HumanSize(r.bytes))
	case r.pending == 0 && r.skipped == 0:
		out.Msg("Nothing to upload.")
	}
	if r.skipped > 0 {
		out.Msg("%d were already there.", r.skipped)
	}
	if r.pending > 0 {
		out.Msg("%d are still being written and were left for next time.", r.pending)
	}
	if r.dirs > 0 {
		// Said out loud rather than passed over: an outbox sends files, and
		// somebody who dropped a folder in needs to know it stayed put.
		out.Warn("%s skipped: an outbox uploads files, not folders. "+
			"'cernbox put -r' or 'cernbox sync' will send a directory.",
			itemsPlural(r.dirs, "folder"))
	}
}

// runOutbox makes one pass over one folder.
func (a *App) runOutbox(ctx context.Context, job outboxJob, settle time.Duration) (outboxResult, error) {
	var res outboxResult

	scan, err := scanOutbox(job, settle, time.Now())
	if err != nil {
		return res, err
	}
	res.dirs = scan.dirs

	for _, it := range scan.items {
		if !it.settled {
			res.pending++
			continue
		}
		uploaded, size, err := a.sendOutboxItem(ctx, job, it)
		if err != nil {
			res.failed++
			a.out.Warn("%s: %v", it.name, errLine(err))
			continue
		}
		if uploaded {
			res.uploaded++
			res.bytes += size
		} else {
			res.skipped++
		}
	}
	return res, nil
}

// alreadyThere reports whether the destination already holds this file.
//
// Size rather than content, which is the trade sync makes for the same reason:
// reading every byte on both sides to decide whether to read every byte is no
// saving at all.
func (a *App) alreadyThere(ctx context.Context, job outboxJob, it outboxItem) (bool, error) {
	info, err := a.client.Stat(ctx, job.remoteFor(it))
	if err != nil {
		if cberr.KindOf(err) == cberr.KindNotFound {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir && info.Size == it.size, nil
}

// sendOutboxItem uploads one file and then does whatever was asked with the
// local copy. It reports whether anything was actually sent.
func (a *App) sendOutboxItem(ctx context.Context, job outboxJob, it outboxItem) (bool, int64, error) {
	remote, err := a.resolve(ctx, job.remoteFor(it))
	if err != nil {
		return false, 0, err
	}

	switch info, statErr := a.client.Stat(ctx, remote); {
	case statErr != nil && cberr.KindOf(statErr) != cberr.KindNotFound:
		return false, 0, statErr
	case statErr == nil && !info.IsDir && info.Size == it.size:
		// Already safely up. Still apply the policy, so that a run which uploaded
		// and then failed to tidy up finishes the job rather than leaving the file
		// to be considered for ever.
		return false, 0, a.afterOutbox(job, it)
	case statErr == nil:
		// Taken by something else. An outbox adds to CERNBox and never writes over
		// what is already there, so this goes alongside under another name.
		if remote, err = a.freeRemoteName(ctx, remote); err != nil {
			return false, 0, err
		}
	}

	if err := a.client.Mkdir(ctx, path.Dir(remote), true); err != nil &&
		cberr.KindOf(err) != cberr.KindConflict {
		return false, 0, err
	}

	// Verified when the local copy is about to be deleted. A checksum the server
	// agrees with is the difference between knowing the file arrived and assuming
	// it did, and assuming is not good enough to delete somebody's only copy.
	engine, err := a.transferEngine(transferFlags{force: true, verify: job.after == "delete"})
	if err != nil {
		return false, 0, err
	}
	size, err := engine.UploadFile(ctx, it.path, remote)
	if err != nil {
		return false, 0, err
	}

	if job.link {
		if url, err := a.linkFor(ctx, remote); err != nil {
			a.out.Warn("uploaded %s but could not make a link: %v", it.name, errLine(err))
		} else {
			// Last on stdout, so that piping into a clipboard tool works.
			a.out.Line("%s", url)
		}
	}

	return true, size, a.afterOutbox(job, it)
}

// linkFor makes a public link for something just uploaded.
func (a *App) linkFor(ctx context.Context, remote string) (string, error) {
	info, err := a.client.Stat(ctx, remote)
	if err != nil {
		return "", err
	}
	perm, err := a.client.CreateLink(ctx, info.ID, client.LinkOptions{Type: "view"})
	if err != nil {
		return "", err
	}
	if perm == nil || perm.Link == nil || perm.Link.URL == "" {
		return "", cberr.New(cberr.KindOther, "create a link", remote, "the server returned no link")
	}
	return perm.Link.URL, nil
}

// freeRemoteName finds a name next to remote that nothing is using.
func (a *App) freeRemoteName(ctx context.Context, remote string) (string, error) {
	ext := path.Ext(remote)
	stem := strings.TrimSuffix(remote, ext)
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if _, err := a.client.Stat(ctx, candidate); err != nil {
			if cberr.KindOf(err) == cberr.KindNotFound {
				return candidate, nil
			}
			return "", err
		}
	}
	return "", cberr.New(cberr.KindConflict, "upload", remote,
		"a thousand files already have this name")
}

// afterOutbox does whatever the folder asked for once the file is safely up.
func (a *App) afterOutbox(job outboxJob, it outboxItem) error {
	switch job.after {
	case "delete":
		return os.Remove(it.path)
	case "move":
		dir := filepath.Join(job.local, uploadedDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		dst := filepath.Join(dir, it.name)
		for n := 2; pathTaken(dst) && n < 1000; n++ {
			ext := filepath.Ext(it.name)
			dst = filepath.Join(dir, fmt.Sprintf("%s (%d)%s",
				strings.TrimSuffix(it.name, ext), n, ext))
		}
		return os.Rename(it.path, dst)
	}
	return nil
}

func pathTaken(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
