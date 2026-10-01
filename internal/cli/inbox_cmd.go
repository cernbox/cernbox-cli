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

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

// The inbox is the outbox the other way round: a CERNBox folder whose arrivals
// come down to a local one. It pairs with an upload link — somebody drops a
// file in without an account and it turns up on your laptop — and with a
// project folder collaborators write into.
//
// Two things make it genuinely different from the outbox rather than a mirror
// image of it, and both are worth knowing before using it.
//
// It has to poll. The outbox is told when a local folder changes, by the
// filesystem; nothing on this surface will tell a client that a remote folder
// did, so watching means asking. One pass is one listing per folder.
//
// And the settle test compares against the server's clock, not this machine's.
// A file still being uploaded keeps its modification time current, so one
// listing is enough to tell it apart from a finished one — but that comparison
// is between a timestamp the server wrote and a now, and taking now from here
// would make the test wrong by however far the two clocks are apart. Which is
// not a hypothetical: it is what "cernbox doctor" checks.

const (
	// defaultInboxInterval is how often a watch looks. Longer than the outbox's
	// settle check, because every pass is a request rather than a cheap look at
	// a few files that had events.
	defaultInboxInterval = time.Minute

	// collectedDir is where "--after move" puts a file once it is safely down.
	collectedDir = ".collected"
)

func newInboxCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Download whatever appears in a CERNBox folder",
		Long: "Treat a CERNBox folder as an inbox: anything that turns up in it is\n" +
			"downloaded to a local folder.\n" +
			"\n" +
			"It is one way, always. Nothing local is changed except by adding to it, and\n" +
			"'cernbox sync' is still the command for keeping two sides matching.\n" +
			"\n" +
			"Folders are usually listed in the configuration file under 'inbox', each with\n" +
			"its own destination, so that 'cernbox inbox pull' needs no arguments. A folder\n" +
			"can also be given on the command line with --to.",
	}
	cmd.AddCommand(newInboxPullCmd(app), newInboxWatchCmd(app), newInboxStatusCmd(app))
	return cmd
}

// ── flags ────────────────────────────────────────────────────────────────────

type inboxFlags struct {
	to     string
	layout string
	after  string
	settle time.Duration
}

func (o *inboxFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.to, "to", "", "local folder to download into")
	cmd.Flags().StringVar(&o.layout, "layout", "",
		"flat, or date to file each arrival under YYYY/MM/DD")
	cmd.Flags().StringVar(&o.after, "after", "",
		"what to do with the CERNBox copy once it is down: keep, move, or delete")
	cmd.Flags().DurationVar(&o.settle, "settle", defaultSettle,
		"how long a file must sit unchanged before it counts as finished")
}

// inboxJobs works out which folders to collect from.
func (a *App) inboxJobs(args []string, flags inboxFlags) ([]inboxJob, error) {
	if len(args) == 1 {
		if flags.to == "" {
			return nil, cberr.Usagef("pass --to with the local folder to download %s into", args[0])
		}
		job, err := newInboxJob(InboxFolder{
			Remote: args[0],
			Local:  flags.to,
			Layout: flags.layout,
			After:  flags.after,
		})
		if err != nil {
			return nil, err
		}
		return []inboxJob{job}, nil
	}

	if flags.to != "" {
		return nil, cberr.Usagef("--to describes one folder, so name the folder it applies to")
	}
	if len(a.cfg.Inbox) == 0 {
		return nil, cberr.Usagef("no inbox folders are configured. Name one with " +
			"'cernbox inbox pull REMOTE --to DIR', or list them under 'inbox' in the configuration file")
	}

	jobs := make([]inboxJob, 0, len(a.cfg.Inbox))
	for _, f := range a.cfg.Inbox {
		// Flags override the configuration for every folder, which is what makes
		// "--after keep" usable as a safety net over a config that says delete.
		if flags.layout != "" {
			f.Layout = flags.layout
		}
		if flags.after != "" {
			f.After = flags.after
		}
		job, err := newInboxJob(f)
		if err != nil {
			return nil, err
		}
		job.configured = true
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// ── pull ─────────────────────────────────────────────────────────────────────

func newInboxPullCmd(app *App) *cobra.Command {
	var flags inboxFlags

	cmd := &cobra.Command{
		Use:   "pull [REMOTE]",
		Short: "Download what is waiting, once",
		Long: "Download whatever is in the inbox folders and stop.\n\n" +
			"This is the form for a timer: it does the one pass and exits.",
		Example: "  cernbox inbox pull\n" +
			"  cernbox inbox pull /eos/project/c/cernbox/incoming --to ~/from-cernbox",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			jobs, err := app.inboxJobs(args, flags)
			if err != nil {
				return err
			}

			var total inboxResult
			for _, job := range jobs {
				got, err := app.runInbox(ctx, job, flags.settle)
				if err != nil {
					// A configured folder that is not there is a warning, not the
					// end of the run: the others have nothing to do with it.
					if job.configured && cberr.KindOf(err) == cberr.KindNotFound {
						app.out.Warn("skipping %s: %v", job.remote, errLine(err))
						continue
					}
					return err
				}
				total.add(got)
			}
			total.report(app.out)
			return nil
		},
	}

	flags.register(cmd)
	return cmd
}

// ── watch ────────────────────────────────────────────────────────────────────

func newInboxWatchCmd(app *App) *cobra.Command {
	var flags inboxFlags
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "watch [REMOTE]",
		Short: "Keep downloading as things appear",
		Long: "Watch the inbox folders and download what turns up.\n" +
			"\n" +
			"This asks on a timer, unlike 'outbox watch', which the filesystem tells\n" +
			"about a change. Nothing on this surface announces one, so --interval is a\n" +
			"direct cost: one listing per folder per pass.\n" +
			"\n" +
			"For something that survives sleep, a lost network and a reboot, run\n" +
			"'inbox pull' from a timer instead, and give it an app token.",
		Example: "  cernbox inbox watch\n" +
			"  cernbox inbox watch --interval 5m",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if interval <= 0 {
				return cberr.Usagef("--interval wants a positive duration")
			}
			jobs, err := app.inboxJobs(args, flags)
			if err != nil {
				return err
			}
			if err := app.checkUnattended(); err != nil {
				return err
			}
			for _, job := range jobs {
				app.out.Msg("Watching %s → %s every %s", job.remote, job.local, interval)
			}
			return app.watchInbox(ctx, jobs, flags.settle, interval)
		},
	}

	flags.register(cmd)
	cmd.Flags().DurationVar(&interval, "interval", defaultInboxInterval,
		"how often to look in the folders")
	return cmd
}

// watchInbox collects on a timer until the context ends.
//
// A context that has ended is how this finishes — an interrupt, or --timeout —
// so it is not an error.
func (a *App) watchInbox(ctx context.Context, jobs []inboxJob, settle, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// Whatever is already there goes first, since nothing announced it.
		for _, job := range jobs {
			got, err := a.runInbox(ctx, job, settle)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				// Kept going rather than fatal: a watch that stops on one bad
				// pass is a watch that stops overnight for a reason nobody sees.
				a.out.Warn("%s: %v", job.remote, errLine(err))
				continue
			}
			got.report(a.out)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ── status ───────────────────────────────────────────────────────────────────

func newInboxStatusCmd(app *App) *cobra.Command {
	var flags inboxFlags

	cmd := &cobra.Command{
		Use:   "status [REMOTE]",
		Short: "Show what is waiting to be downloaded",
		Long: "List what the inbox would download, without downloading anything.\n\n" +
			"This is how you check that an inbox is working, and what it is waiting on:\n" +
			"a file still being uploaded is listed as such rather than left unexplained.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			jobs, err := app.inboxJobs(args, flags)
			if err != nil {
				return err
			}

			type row struct {
				Remote string `json:"remote"`
				Local  string `json:"local"`
				Size   int64  `json:"size"`
				State  string `json:"state"`
			}
			var rows []row
			table := output.Table{Headers: []string{"SIZE", "STATE", "REMOTE", "GOES TO"}}

			for _, job := range jobs {
				scan, err := app.scanInboxFor(ctx, job, flags.settle)
				if err != nil {
					if job.configured && cberr.KindOf(err) == cberr.KindNotFound {
						app.out.Warn("skipping %s: %v", job.remote, errLine(err))
						continue
					}
					return err
				}
				for _, it := range scan.items {
					state := "ready"
					if !it.settled {
						state = "still arriving"
					} else if there, _ := app.alreadyLocal(job, it); there {
						state = "already here"
					}
					local := job.localFor(it)
					rows = append(rows, row{Remote: it.path, Local: local, Size: it.size, State: state})
					table.Rows = append(table.Rows, []string{
						output.HumanSize(it.size), state, it.path, local,
					})
				}
				if scan.dirs > 0 {
					app.out.Msg("%s holds %s an inbox does not collect.",
						job.remote, plural(scan.dirs, "subdirectory", "subdirectories"))
				}
			}
			table.Items = rows
			return app.out.Render(table)
		},
	}

	flags.register(cmd)
	return cmd
}

// ── the job ──────────────────────────────────────────────────────────────────

type inboxJob struct {
	remote string
	local  string
	layout string
	after  string
	// configured marks a folder that came from the configuration file rather than
	// from the command line, which changes what a missing folder means.
	configured bool
}

func newInboxJob(f InboxFolder) (inboxJob, error) {
	if f.Local == "" || f.Remote == "" {
		return inboxJob{}, cberr.Usagef("an inbox folder needs both a CERNBox folder and a local path")
	}
	local, err := filepath.Abs(expandHome(f.Local))
	if err != nil {
		return inboxJob{}, cberr.Usagef("%v", err)
	}

	job := inboxJob{remote: f.Remote, local: local, layout: f.Layout, after: f.After}
	if job.layout == "" {
		job.layout = "flat"
	}
	if job.after == "" {
		// Keeping the CERNBox copy is the only choice that cannot lose anything,
		// so it is what you get unless the configuration says otherwise in
		// writing.
		job.after = "keep"
	}
	switch job.layout {
	case "flat", "date":
	default:
		return inboxJob{}, cberr.Usagef("unknown layout %q: want flat or date", job.layout)
	}
	switch job.after {
	case "keep", "move", "delete":
	default:
		return inboxJob{}, cberr.Usagef("unknown --after %q: want keep, move or delete", job.after)
	}
	return job, nil
}

// inboxScan is what one folder holds.
type inboxScan struct {
	items []inboxItem
	// dirs counts subdirectories, which an inbox does not collect. Counted
	// rather than ignored: somebody who put a folder there and is told nothing
	// would reasonably believe it had been collected.
	dirs int
}

// inboxItem is one CERNBox file that might need downloading.
type inboxItem struct {
	path     string
	name     string
	size     int64
	modified time.Time
	settled  bool
}

// localFor is where a file goes, which for the date layout is taken from the
// file's own timestamp rather than from today: a file belongs to the day it was
// made, whenever it happens to be collected.
func (j inboxJob) localFor(it inboxItem) string {
	if j.layout == "date" {
		d := it.modified
		return filepath.Join(j.local,
			fmt.Sprintf("%04d/%02d/%02d", d.Year(), int(d.Month()), d.Day()), it.name)
	}
	return filepath.Join(j.local, it.name)
}

// scanInboxFor lists what is in the folder, marking which files have stopped
// changing.
//
// An inbox is one folder deep on purpose, like the outbox: deciding that a whole
// tree has finished arriving is a much harder question than deciding it about
// one file.
func (a *App) scanInboxFor(ctx context.Context, job inboxJob, settle time.Duration) (inboxScan, error) {
	remote, err := a.resolve(ctx, job.remote)
	if err != nil {
		return inboxScan{}, err
	}
	entries, err := a.client.List(ctx, remote)
	if err != nil {
		return inboxScan{}, err
	}

	// The server's clock, not this machine's. The timestamps being compared were
	// written by the server, so a now taken from here would make the settle test
	// wrong by however far the two clocks are apart.
	now, err := a.client.ServerTime(ctx)
	if err != nil {
		// Not fatal, but worth saying: the comparison is then only as good as the
		// agreement between two clocks nobody has checked.
		a.out.Warn("could not read the server clock (%v), so using this machine's", errLine(err))
		now = time.Now()
	}

	var scan inboxScan
	for _, e := range entries {
		if e.IsDir {
			scan.dirs++
			continue
		}
		if skipInboxName(e.Name) {
			continue
		}
		scan.items = append(scan.items, inboxItem{
			path:     e.Path,
			name:     e.Name,
			size:     e.Size,
			modified: e.Modified,
			settled:  now.Sub(e.Modified) >= settle,
		})
	}
	sort.Slice(scan.items, func(i, j int) bool { return scan.items[i].name < scan.items[j].name })
	return scan, nil
}

// skipInboxName leaves alone the names that are not finished files, and the
// folder the inbox itself moves things into.
func skipInboxName(name string) bool {
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

// ── the run ──────────────────────────────────────────────────────────────────

type inboxResult struct {
	downloaded int
	skipped    int
	waiting    int
	bytes      int64
}

func (r *inboxResult) add(o inboxResult) {
	r.downloaded += o.downloaded
	r.skipped += o.skipped
	r.waiting += o.waiting
	r.bytes += o.bytes
}

func (r inboxResult) report(out *output.Writer) {
	if r.downloaded == 0 && r.skipped == 0 && r.waiting == 0 {
		out.Msg("Nothing to collect.")
		return
	}
	msg := fmt.Sprintf("Downloaded %s (%s)",
		plural(r.downloaded, "file", "files"), output.HumanSize(r.bytes))
	if r.skipped > 0 {
		msg += fmt.Sprintf(", %s already here", plural(r.skipped, "file", "files"))
	}
	if r.waiting > 0 {
		msg += fmt.Sprintf(", %s still arriving", plural(r.waiting, "file", "files"))
	}
	out.Msg("%s.", msg)
}

func (a *App) runInbox(ctx context.Context, job inboxJob, settle time.Duration) (inboxResult, error) {
	scan, err := a.scanInboxFor(ctx, job, settle)
	if err != nil {
		return inboxResult{}, err
	}

	var res inboxResult
	for _, it := range scan.items {
		if !it.settled {
			res.waiting++
			continue
		}
		got, size, err := a.collectInboxItem(ctx, job, it)
		if err != nil {
			return res, err
		}
		if got {
			res.downloaded++
			res.bytes += size
		} else {
			res.skipped++
		}
	}
	return res, nil
}

// alreadyLocal reports whether this file is already down, by the same rule the
// outbox uses in the other direction: a local file of the same size is taken to
// be the same file.
func (a *App) alreadyLocal(job inboxJob, it inboxItem) (bool, error) {
	info, err := os.Stat(job.localFor(it))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir() && info.Size() == it.size, nil
}

func (a *App) collectInboxItem(ctx context.Context, job inboxJob, it inboxItem) (bool, int64, error) {
	local := job.localFor(it)

	switch there, err := a.alreadyLocal(job, it); {
	case err != nil:
		return false, 0, err
	case there:
		// Already safely down. The policy still runs, so that a pass which
		// downloaded and then failed to tidy up finishes the job rather than
		// leaving the file to be considered for ever.
		return false, 0, a.afterInbox(ctx, job, it)
	}
	if pathTaken(local) {
		// Taken by something else of a different size. An inbox adds to the local
		// folder and never writes over what is already there, so this goes
		// alongside under another name.
		local = freeLocalName(local)
	}

	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return false, 0, cberr.Wrap(cberr.KindOther, "create", filepath.Dir(local), err)
	}

	// Verified when the CERNBox copy is about to be deleted. A checksum both
	// sides agree on is the difference between knowing the file arrived and
	// assuming it did, and assuming is not good enough to delete somebody's only
	// copy.
	engine, err := a.transferEngine(transferFlags{force: true, verify: job.after == "delete"})
	if err != nil {
		return false, 0, err
	}
	size, err := engine.DownloadFile(ctx, it.path, local)
	if err != nil {
		return false, 0, err
	}
	return true, size, a.afterInbox(ctx, job, it)
}

// freeLocalName finds a name next to local that nothing is using.
func freeLocalName(local string) string {
	ext := filepath.Ext(local)
	stem := strings.TrimSuffix(local, ext)
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if !pathTaken(candidate) {
			return candidate
		}
	}
	return local
}

// afterInbox does whatever the folder asked for once the file is safely down.
func (a *App) afterInbox(ctx context.Context, job inboxJob, it inboxItem) error {
	switch job.after {
	case "delete":
		return a.client.Remove(ctx, it.path)
	case "move":
		dir := path.Join(path.Dir(it.path), collectedDir)
		if err := a.client.Mkdir(ctx, dir, true); err != nil &&
			cberr.KindOf(err) != cberr.KindConflict {
			return err
		}
		dst := path.Join(dir, it.name)
		if _, err := a.client.Stat(ctx, dst); err == nil {
			moved, err := a.freeRemoteName(ctx, dst)
			if err != nil {
				return err
			}
			dst = moved
		}
		return a.client.Move(ctx, it.path, dst, false)
	}
	return nil
}
