package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newTrashCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trash",
		Short: "Work with deleted files",
		Long: "Work with deleted files.\n\n" +
			"Each space has its own trash. Commands use your home space unless you pass\n" +
			"--space.\n\n" +
			"'cernbox trash browse' opens the bin as a directory tree you can walk\n" +
			"through, which is easier than copying keys out of a listing.",
	}
	cmd.AddCommand(newTrashListCmd(app), newTrashBrowseCmd(app),
		newTrashRestoreCmd(app), newTrashPurgeCmd(app))
	return cmd
}

// spaceFlag adds --space, which selects which recycle bin to act on.
func spaceFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "space", "",
		"space whose trash bin to use, e.g. project/cernbox (default: your home space)")
}

// trashBasePath resolves --space to the base path the server expects. An empty
// result means the caller's home, which is the server's own default.
func (a *App) trashBasePath(cmd *cobra.Command, space string) (string, error) {
	if space == "" {
		return "", nil
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	return a.client.ResolveSpace(ctx, space)
}

// trashWindowOptions are the flags that choose which slice of a bin to look at.
// Registered on every command that reads one, so the query language cannot drift
// between them.
type trashWindowOptions struct {
	space string
	since string
	from  string
	to    string
}

func (o *trashWindowOptions) register(cmd *cobra.Command) {
	spaceFlag(cmd, &o.space)
	cmd.Flags().StringVar(&o.since, "since", "",
		"how far back to look, e.g. 7d, 2w, 36h (default 2d)")
	cmd.Flags().StringVar(&o.from, "from", "", "list deletions from this date, e.g. 2026-09-01")
	cmd.Flags().StringVar(&o.to, "to", "", "list deletions up to this date (default now)")
}

func (o *trashWindowOptions) window() (client.TrashWindow, error) {
	return trashWindow(o.since, o.from, o.to)
}

func newTrashListCmd(app *App) *cobra.Command {
	var opts trashWindowOptions

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List deleted files",
		Long: "List deleted files.\n\n" +
			"A listing covers a span of time, not the whole bin: the server reaches two\n" +
			"days back unless it is told otherwise. Use --since to look further, or\n" +
			"--from and --to for a particular stretch.",
		Example: "  cernbox trash list\n" +
			"  cernbox trash list --since 30d\n" +
			"  cernbox trash list --from 2026-09-01 --to 2026-09-15\n" +
			"  cernbox trash list --space project/cernbox",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			window, err := opts.window()
			if err != nil {
				return err
			}
			base, err := app.trashBasePath(cmd, opts.space)
			if err != nil {
				return err
			}
			listing, err := app.client.ListTrash(ctx, base, window)
			if err != nil {
				return err
			}
			return app.renderTrashListing(listing)
		},
	}

	opts.register(cmd)
	return cmd
}

// trashWindow turns the flags into a window.
func trashWindow(since, from, to string) (client.TrashWindow, error) {
	if since != "" && (from != "" || to != "") {
		return client.TrashWindow{}, cberr.Usagef(
			"--since says how far back to look and --from/--to say exactly when, so pass one or the other")
	}

	var w client.TrashWindow
	if to != "" {
		t, err := time.Parse(expiryLayout, to)
		if err != nil {
			return w, cberr.Usagef("invalid --to %q: want a date like 2026-09-15", to)
		}
		// The end of that day rather than its midnight, so --to 2026-09-15
		// includes what was deleted during the 15th.
		w.To = t.Add(24*time.Hour - time.Second)
	}
	if from != "" {
		t, err := time.Parse(expiryLayout, from)
		if err != nil {
			return w, cberr.Usagef("invalid --from %q: want a date like 2026-09-01", from)
		}
		w.From = t
	}
	if since != "" {
		d, err := parseLookback(since)
		if err != nil {
			return w, err
		}
		w.To = time.Now()
		w.From = w.To.Add(-d)
	}
	if !w.From.IsZero() && !w.To.IsZero() && !w.To.After(w.From) {
		return w, cberr.Usagef("--from must come before --to")
	}
	return w, nil
}

// parseLookback reads a span like 30d, 2w, 36h, or a bare number of days.
//
// time.ParseDuration has no unit longer than an hour, and nobody asks for their
// deleted files in hours: "--since 720h" is how you write a month only if you
// have already done the arithmetic.
func parseLookback(s string) (time.Duration, error) {
	invalid := cberr.Usagef("invalid --since %q: want something like 7d, 2w, 36h", s)

	unit := time.Hour * 24
	num := s
	switch {
	case strings.HasSuffix(s, "d"):
		num = strings.TrimSuffix(s, "d")
	case strings.HasSuffix(s, "w"):
		num, unit = strings.TrimSuffix(s, "w"), 7*24*time.Hour
	case strings.HasSuffix(s, "h") || strings.HasSuffix(s, "m") || strings.HasSuffix(s, "s"):
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return 0, invalid
		}
		return d, nil
	}

	n, err := strconv.ParseFloat(num, 64)
	if err != nil || n <= 0 {
		return 0, invalid
	}
	return time.Duration(n * float64(unit)), nil
}

// describeTrashWindow says what was searched, and what was refused.
func describeTrashWindow(app *App, listing *client.TrashListing) {
	where := trashWindowPhrase(listing.Window)
	if len(listing.Items) == 0 {
		app.out.Msg("Nothing was deleted %s. Look further back with 'trash list --since 30d'.", where)
	} else {
		app.out.Msg("%s %s. Look further back with 'trash list --since 30d'.",
			filesPlural(len(listing.Items)), where)
	}

	// A refused day is a hole in what was just printed. Staying quiet about it
	// would turn "the server would not tell me" into "it is not there", which is
	// the one mistake a trash listing must never make.
	for _, gap := range listing.Gaps {
		app.out.Warn("the server would not list %s — more was deleted that day than it will "+
			"return at once, so anything from it is missing above",
			gap.From.Format(expiryLayout))
	}
}

// trashWindowPhrase describes a window the way it was asked for: a lookback
// reads as "in the last week", an explicit range as the dates themselves.
func trashWindowPhrase(w client.TrashWindow) string {
	if time.Since(w.To) > time.Hour {
		return fmt.Sprintf("between %s and %s",
			w.From.Format(expiryLayout), w.To.Format(expiryLayout))
	}
	span := w.To.Sub(w.From)
	if span < 24*time.Hour {
		// Saying "the last day" for --since 2h would be the same overstatement
		// this whole window business exists to stop making.
		return fmt.Sprintf("in the last %d hours", max(int(span.Hours()), 1))
	}
	days := int(span.Hours() / 24)
	switch {
	case days == 1:
		return "in the last day"
	case days == 7:
		return "in the last week"
	case days%7 == 0:
		return fmt.Sprintf("in the last %d weeks", days/7)
	default:
		return fmt.Sprintf("in the last %d days", days)
	}
}

func filesPlural(n int) string {
	if n == 1 {
		return "1 file deleted"
	}
	return fmt.Sprintf("%d files deleted", n)
}

// renderTrashListing prints a listing as a table, and says which window it
// covered. Shared with the browser's --plain fallback so there is one renderer.
func (a *App) renderTrashListing(listing *client.TrashListing) error {
	now := time.Now()
	table := output.Table{
		Headers: []string{"KEY", "TYPE", "SIZE", "DELETED", "ORIGINAL PATH"},
		Items:   listing.Items,
	}
	for _, it := range listing.Items {
		kind := "file"
		if it.IsDir {
			kind = "dir"
		}
		table.Rows = append(table.Rows, []string{
			it.Key, kind, output.HumanSize(it.Size),
			output.HumanTime(it.DeletedAt, now),
			orDash(it.OriginalPath),
		})
	}
	if err := a.out.Render(table); err != nil {
		return err
	}

	// After the table, and on stderr, so that it informs a person without
	// reaching a pipe. Saying which window was searched is the point: "nothing
	// there" and "nothing there lately" are different answers, and only one of
	// them is ever true.
	describeTrashWindow(a, listing)
	return nil
}

func newTrashBrowseCmd(app *App) *cobra.Command {
	var opts trashWindowOptions
	var plain bool

	cmd := &cobra.Command{
		Use:     "browse",
		Aliases: []string{"ui"},
		Short:   "Walk through deleted files and restore them",
		Long: "Open the trash bin as a directory tree.\n\n" +
			"Deleted files remember where they lived, so the bin can be walked like the\n" +
			"folders it came from. Pick what you want back and restore it in one go,\n" +
			"without copying any keys.\n\n" +
			"A listing covers a span of time rather than the whole bin, starting at two\n" +
			"days; --since widens it before opening, and 't' widens it from inside.",
		Example: "  cernbox trash browse\n" +
			"  cernbox trash browse --since 30d\n" +
			"  cernbox trash browse --space project/cernbox",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			window, err := opts.window()
			if err != nil {
				return err
			}
			base, err := app.trashBasePath(cmd, opts.space)
			if err != nil {
				return err
			}

			// Asked not to take the terminal, so none of the terminal's
			// requirements apply: this is the escape hatch for the places where
			// the browser cannot run, and refusing it there would be absurd.
			if plain || os.Getenv("CERNBOX_NO_TUI") != "" {
				listing, err := app.client.ListTrash(ctx, base, window)
				if err != nil {
					return err
				}
				return app.renderTrashListing(listing)
			}

			if reason := app.browseRefusal(); reason != "" {
				return cberr.Usagef("%s", reason)
			}
			return runTrashBrowser(ctx, app, opts.space, base, window)
		},
	}

	opts.register(cmd)
	cmd.Flags().BoolVar(&plain, "plain", false,
		"print the listing instead of taking over the terminal")
	return cmd
}

// browseRefusal explains why the browser must not start, or "" when it may.
//
// Every one of these is a case where starting it would be worse than refusing.
// A frame drawn into a pipe is gibberish; a cron job that meets a full-screen UI
// hangs until somebody kills it; and a caller who asked for JSON asked for
// something this cannot produce, so ignoring the flag would be the wrong kind of
// helpful.
func (a *App) browseRefusal() string {
	switch {
	case a.out.Format() != output.FormatTable:
		return "browsing produces no machine-readable output; use 'cernbox trash list --output json'"
	case a.out.IsQuiet():
		return "--quiet and a full-screen browser ask for opposite things; use 'cernbox trash list'"
	case a.stdinFile() == nil || a.stdoutFile() == nil,
		!output.IsTerminal(a.stdinFile()) || !output.IsTerminal(a.stdoutFile()):
		return "browsing needs a terminal on both input and output; 'cernbox trash list' prints the same thing"
	case os.Getenv("TERM") == "" || os.Getenv("TERM") == "dumb":
		return "this terminal cannot be drawn on (TERM is " + orDash(os.Getenv("TERM")) +
			"); use 'cernbox trash list', or --plain"
	}
	return ""
}

func newTrashRestoreCmd(app *App) *cobra.Command {
	var space string

	cmd := &cobra.Command{
		Use:   "restore KEY...",
		Short: "Restore a deleted file",
		Long: "Restore deleted files. They go back where they were, which is the path\n" +
			"shown by 'cernbox trash list'.\n\n" +
			"With no keys, and on a terminal, this opens the browser so you can pick\n" +
			"what to bring back.",
		Example: "  cernbox trash restore 1a2b3c\n  cernbox trash restore",
		// Checked in the body rather than by cobra, because no arguments is not
		// a mistake here: it is how somebody who does not have a key to hand
		// asks to be shown the bin.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			base, err := app.trashBasePath(cmd, space)
			if err != nil {
				return err
			}

			if len(args) == 0 {
				if reason := app.browseRefusal(); reason != "" {
					return cberr.Usagef("pass the keys to restore, which 'cernbox trash list' shows (%s)", reason)
				}
				return runTrashBrowser(ctx, app, space, base, client.TrashWindow{})
			}

			for _, key := range args {
				if err := app.client.RestoreTrash(ctx, key, base); err != nil {
					return err
				}
				app.out.Msg("Restored %s", key)
			}
			return nil
		},
	}

	spaceFlag(cmd, &space)
	return cmd
}

func newTrashPurgeCmd(app *App) *cobra.Command {
	var space string

	cmd := &cobra.Command{
		Use:   "purge KEY...",
		Short: "Delete items from the trash for good",
		Long: "Delete trash items for good. This cannot be undone.\n\n" +
			"There is no way to empty the whole bin: the storage does not offer one, and\n" +
			"the keys to purge come from 'cernbox trash list'.",
		// Checked here rather than with cobra.MinimumNArgs so that a missing key
		// exits 2, the code this CLI documents for a mistake in the command.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if len(args) == 0 {
				return cberr.Usagef("pass the keys to purge, which 'cernbox trash list' shows")
			}

			base, err := app.trashBasePath(cmd, space)
			if err != nil {
				return err
			}

			for _, key := range args {
				if err := app.client.PurgeTrash(ctx, key, base); err != nil {
					return err
				}
				app.out.Msg("Purged %s", key)
			}
			return nil
		},
	}

	spaceFlag(cmd, &space)
	return cmd
}
