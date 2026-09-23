package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newLsCmd(app *App) *cobra.Command {
	var opts lsOptions

	cmd := &cobra.Command{
		Use:   "ls [PATH...]",
		Short: "List a directory",
		Long: "List a directory. With no path, lists your home space.\n\n" +
			"Works like ls: no header, columns on a terminal, one name per line when\n" +
			"the output is piped.",
		Example: "  cernbox ls /eos/user/g/gdelmont\n" +
			"  cernbox ls -l home:Documents\n" +
			"  cernbox ls --output json /eos/project/c/cernbox | jq '.[].name'",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if len(args) == 0 {
				args = []string{"home:"}
			}
			for i, arg := range args {
				if len(args) > 1 && !app.out.Streaming() {
					app.out.Msg("%s:", arg)
				}
				if err := app.listOne(ctx, arg, opts); err != nil {
					return err
				}
				if i < len(args)-1 && !app.out.Streaming() {
					app.out.Msg("")
				}
			}
			return nil
		},
	}

	f := cmd.Flags()

	// -h means human-readable here, as it does in ls, so it cannot also mean
	// help. Declaring --help first stops cobra adding its own with the -h
	// shorthand; cobra still honours the flag, so "cernbox ls --help" works,
	// and every other command keeps -h for help.
	f.Bool("help", false, "show help for ls")

	f.BoolVarP(&opts.Long, "long", "l", false, "long listing: rights, size, modification time")
	f.BoolVarP(&opts.Human, "human-readable", "h", false, "print sizes as 1.2K, 34M")
	f.BoolVarP(&opts.All, "all", "a", false, "include entries whose name starts with a dot")
	f.BoolVarP(&opts.Recursive, "recursive", "R", false, "list subdirectories recursively")
	f.BoolVarP(&opts.OnePerLine, "one-per-line", "1", false, "one entry per line, even on a terminal")
	f.BoolVarP(&opts.Reverse, "reverse", "r", false, "reverse the sort order")
	f.BoolVarP(&opts.Classify, "classify", "F", false, "append / to directory names")
	f.StringVar(&opts.Sort, "sort", "name", "sort by: name, time, or size")
	f.BoolVarP(&opts.SortTime, "sort-time", "t", false, "sort by modification time, newest first")
	f.BoolVarP(&opts.SortSize, "sort-size", "S", false, "sort by size, largest first")
	_ = f.MarkHidden("sort-time")
	_ = f.MarkHidden("sort-size")
	return cmd
}

// lsOptions is what the ls flags select. It is a struct because ls has many
// independent switches and a positional argument list is unreadable at six.
type lsOptions struct {
	Long       bool
	All        bool
	Recursive  bool
	OnePerLine bool
	Reverse    bool
	Classify   bool
	// Human prints 1.2K rather than a byte count, as ls -h does. Bytes are the
	// default, again as in ls.
	Human bool
	// Sort is "name", "time" or "size"; -t and -S set it.
	Sort     string
	SortTime bool
	SortSize bool
}

// sortKey resolves the -t and -S shorthands against --sort, which they set.
func (o lsOptions) sortKey() string {
	switch {
	case o.SortTime:
		return "time"
	case o.SortSize:
		return "size"
	default:
		return o.Sort
	}
}

func (a *App) listOne(ctx context.Context, arg string, opts lsOptions) error {
	p, err := a.resolve(ctx, arg)
	if err != nil {
		return err
	}

	var entries []client.ResourceInfo
	if opts.Recursive {
		err = a.client.Walk(ctx, p, func(info client.ResourceInfo) error {
			if info.Path == p {
				return nil
			}
			entries = append(entries, info)
			return nil
		})
	} else {
		entries, err = a.client.List(ctx, p)
	}
	if err != nil {
		return err
	}

	if !opts.All {
		filtered := entries[:0]
		for _, e := range entries {
			if !strings.HasPrefix(e.Name, ".") {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	sortEntries(entries, opts)

	// Streaming mode writes each entry as it goes, which is what keeps a
	// listing of a very large directory from being held in memory by the
	// consumer's pipe.
	if a.out.Streaming() {
		for _, e := range entries {
			if err := a.out.Item(e); err != nil {
				return err
			}
		}
		return nil
	}

	// JSON and CSV keep the labelled, stable shape a script parses. Only the
	// human rendering is made to look like ls.
	if a.out.Format() != output.FormatTable {
		table := output.Table{Items: entries}
		now := time.Now()
		if opts.Long {
			table.Headers = []string{"TYPE", "SIZE", "MODIFIED", "NAME"}
			for _, e := range entries {
				table.Rows = append(table.Rows, []string{
					entryType(e),
					formatSize(e.Size, opts.Human),
					output.HumanTime(e.Modified, now),
					displayName(e, p, opts.Recursive, true),
				})
			}
		} else {
			table.Headers = []string{"NAME"}
			for _, e := range entries {
				table.Rows = append(table.Rows, []string{displayName(e, p, opts.Recursive, true)})
			}
		}
		return a.out.Render(table)
	}

	return a.renderListing(entries, p, opts)
}

// renderListing prints a listing the way ls does: no header, one entry per
// line when piped, columns when a terminal is watching, and a long form that
// leads with what may be done to the entry.
func (a *App) renderListing(entries []client.ResourceInfo, root string, opts lsOptions) error {
	// Two parallel slices: the plain names decide column widths, the decorated
	// ones are what gets printed. Measuring the decorated name would count the
	// escape sequences and shear the columns.
	names := make([]string, 0, len(entries))
	shown := make([]string, 0, len(entries))
	colors := a.listingColors()
	for _, e := range entries {
		name := displayName(e, root, opts.Recursive, opts.Classify)
		names = append(names, name)
		shown = append(shown, colors.Apply(name, e.IsDir))
	}

	if opts.Long {
		var total int64
		for _, e := range entries {
			total += e.Size
		}
		// ls counts disk blocks here. CERNBox does not report blocks, so this
		// is the summed apparent size, which is the useful number anyway.
		a.out.Line("total %s", formatSize(total, opts.Human))

		now := time.Now()
		widest := 0
		sizes := make([]string, len(entries))
		for i, e := range entries {
			sizes[i] = formatSize(e.Size, opts.Human)
			widest = max(widest, len(sizes[i]))
		}
		for i, e := range entries {
			a.out.Line("%s %*s %s %s",
				modeString(e), widest, sizes[i], output.HumanTime(e.Modified, now), shown[i])
		}
		return nil
	}

	for _, line := range columnise(names, shown, a.listingWidth(), opts.OnePerLine) {
		a.out.Line("%s", line)
	}
	return nil
}

// listingColors is the colour database for a listing, empty unless the output
// is a terminal — a pipe gets no escape sequences, which is what ls does and
// what keeps "cernbox ls | grep" working.
func (a *App) listingColors() output.LSColors {
	f, ok := a.stdout.(*os.File)
	if !ok || !output.IsTerminal(f) {
		return output.LSColors{}
	}
	return output.LSColorsFromEnv()
}

// listingWidth is the width to lay columns out in, and 0 when the output is
// not a terminal — piped output is one entry per line, as ls does, so that
// "cernbox ls | while read" works.
func (a *App) listingWidth() int {
	f, ok := a.stdout.(*os.File)
	if !ok || !output.IsTerminal(f) {
		return 0
	}
	return output.TerminalWidth(f)
}

// columnise lays names out down-then-across within width, the way ls does. A
// width of 0, or onePerLine, gives one name per line.
func columnise(names, shown []string, width int, onePerLine bool) []string {
	if len(names) == 0 {
		return nil
	}
	if onePerLine || width <= 0 {
		return shown
	}

	const gap = 2
	widest := 0
	for _, n := range names {
		widest = max(widest, len(n))
	}
	cols := max((width+gap)/(widest+gap), 1)
	if cols == 1 {
		return shown
	}
	rows := (len(names) + cols - 1) / cols

	out := make([]string, 0, rows)
	for r := range rows {
		var b strings.Builder
		for c := range cols {
			// Down-then-across: ls fills each column before the next.
			i := c*rows + r
			if i >= len(names) {
				break
			}
			if c > 0 {
				b.WriteString(strings.Repeat(" ", gap))
			}
			if i+rows < len(names) {
				// Pad by the plain width, print the decorated name.
				b.WriteString(shown[i])
				b.WriteString(strings.Repeat(" ", widest-len(names[i])))
			} else {
				b.WriteString(shown[i]) // last in its row: no trailing padding
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

// modeString renders what the caller may do with an entry, in the shape ls
// uses for a mode.
//
// It is one triad, not three: CERNBox reports the effective rights of whoever
// is asking, and has no owner/group/other split to show. Nor does it report a
// POSIX mode, an owner, or a link count, so those columns are absent rather
// than invented.
func modeString(e client.ResourceInfo) string {
	kind := "-"
	if e.IsDir {
		kind = "d"
	}
	if e.Permissions == "" {
		// The server said nothing about rights; saying "---" would claim it did.
		return kind + "???"
	}

	has := func(letters string) bool { return strings.ContainsAny(e.Permissions, letters) }
	rwx := []byte("---")
	if has("G") {
		rwx[0] = 'r'
		if e.IsDir {
			rwx[2] = 'x' // a readable collection is one you can descend into
		}
	}
	// W writes a file; C and K create a file or a collection inside one.
	if (e.IsDir && has("CK")) || (!e.IsDir && has("W")) {
		rwx[1] = 'w'
	}
	return kind + string(rwx)
}

// sortEntries orders a listing: by name, or by time or size when asked, with
// directories and files intermixed exactly as ls leaves them.
func sortEntries(entries []client.ResourceInfo, opts lsOptions) {
	switch opts.sortKey() {
	case "time":
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].Modified.Equal(entries[j].Modified) {
				return entries[i].Path < entries[j].Path
			}
			return entries[i].Modified.After(entries[j].Modified)
		})
	case "size":
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].Size == entries[j].Size {
				return entries[i].Path < entries[j].Path
			}
			return entries[i].Size > entries[j].Size
		})
	default:
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	}
	if opts.Reverse {
		slices.Reverse(entries)
	}
}

func entryType(e client.ResourceInfo) string {
	if e.IsDir {
		return "dir"
	}
	return "file"
}

// displayName is the name as listed. classify appends "/" to a directory, which
// ls does only under -F — except in the machine formats, where the trailing
// slash has always been part of the contract.
func displayName(e client.ResourceInfo, root string, recursive, classify bool) string {
	name := e.Name
	if recursive {
		name = strings.TrimPrefix(strings.TrimPrefix(e.Path, root), "/")
	}
	if e.IsDir && classify {
		name += "/"
	}
	return name
}

func formatSize(n int64, human bool) string {
	if human {
		return output.HumanSize(n)
	}
	return fmt.Sprintf("%d", n)
}

func newStatCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "stat PATH",
		Short: "Show metadata for a file or directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			p, err := app.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			info, err := app.client.Stat(ctx, p)
			if err != nil {
				return err
			}

			fields := []output.Field{
				{Name: "Path", Value: info.Path},
				{Name: "Type", Value: entryType(*info)},
				{Name: "Size", Value: fmt.Sprintf("%d (%s)", info.Size, output.HumanSize(info.Size))},
				{Name: "Modified", Value: info.Modified.Local().Format(time.RFC3339)},
				{Name: "ETag", Value: info.ETag},
				{Name: "ID", Value: info.ID},
			}
			if info.MimeType != "" {
				fields = append(fields, output.Field{Name: "Type", Value: info.MimeType})
			}
			if info.Permissions != "" {
				fields = append(fields, output.Field{Name: "Permissions", Value: info.Permissions})
			}
			for algo, sum := range info.Checksums {
				fields = append(fields, output.Field{Name: "Checksum " + algo, Value: sum})
			}
			return app.out.Object(info, fields...)
		},
	}
}

func newFindCmd(app *App) *cobra.Command {
	var pattern string
	var limit int

	cmd := &cobra.Command{
		Use:   "find PATH",
		Short: "Search for files by name",
		Long: "Search a directory and everything under it.\n\n" +
			"Pass --name with the text to look for. The server searches when it can,\n" +
			"otherwise the CLI walks the tree, which is slower but always works.",
		Example: "  cernbox find /eos/user/g/gdelmont --name report",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if pattern == "" {
				return cberr.Usagef("pass --name with the text to search for")
			}
			p, err := app.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			results, err := app.client.Search(ctx, p, client.SearchOptions{Pattern: pattern, Limit: limit})
			if errors.Is(err, client.ErrSearchUnsupported) {
				app.out.Msg("The server cannot search. Looking through the files instead...")
				results, err = app.walkSearch(ctx, p, pattern, limit)
			}
			if err != nil {
				return err
			}

			table := output.Table{Headers: []string{"TYPE", "SIZE", "PATH"}, Items: results}
			for _, r := range results {
				table.Rows = append(table.Rows, []string{entryType(r), output.HumanSize(r.Size), r.Path})
			}
			return app.out.Render(table)
		},
	}

	cmd.Flags().StringVar(&pattern, "name", "", "text to match against file names")
	cmd.Flags().IntVar(&limit, "limit", 200, "maximum number of results")
	return cmd
}

// walkSearch matches names by walking the tree, for servers whose search
// endpoint is not implemented.
func (a *App) walkSearch(ctx context.Context, root, pattern string, limit int) ([]client.ResourceInfo, error) {
	needle := strings.ToLower(pattern)
	var results []client.ResourceInfo

	err := a.client.Walk(ctx, root, func(info client.ResourceInfo) error {
		if info.Path == root {
			return nil
		}
		if strings.Contains(strings.ToLower(info.Name), needle) {
			results = append(results, info)
		}
		if limit > 0 && len(results) >= limit {
			return errSearchLimit
		}
		return nil
	})
	if err != nil && !errors.Is(err, errSearchLimit) {
		return nil, err
	}
	return results, nil
}

// errSearchLimit stops a walk once enough matches are in hand. It never reaches
// the caller.
var errSearchLimit = errors.New("search limit reached")

func newDuCmd(app *App) *cobra.Command {
	var opts duOptions

	cmd := &cobra.Command{
		Use:   "du [PATH...]",
		Short: "Show how much space a directory uses",
		Long: "Show space used: a size, a tab, and a path, like du.\n\n" +
			"Only the total for each path is shown. Pass -d to also list the\n" +
			"directories below it.",
		Example: "  cernbox du -h /eos/user/g/gdelmont\n" +
			"  cernbox du -h -d 1 /eos/user/g/gdelmont",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if len(args) == 0 {
				args = []string{"home:"}
			}
			if opts.Summarize {
				opts.MaxDepth = 0
			}

			var items []duEntry
			for _, arg := range args {
				got, err := app.usage(ctx, arg, opts)
				if err != nil {
					return err
				}
				items = append(items, got...)
			}

			// json and csv keep their labelled columns for scripts; the human
			// rendering is du's.
			if app.out.Format() != output.FormatTable {
				table := output.Table{Headers: []string{"SIZE", "PATH"}, Items: items}
				for _, it := range items {
					table.Rows = append(table.Rows, []string{formatSize(it.Size, opts.Human), it.Path})
				}
				return app.out.Render(table)
			}
			for _, it := range items {
				// A tab, as du does: it keeps "cut -f2" working whatever the
				// size column happens to be.
				app.out.Line("%s\t%s", formatSize(it.Size, opts.Human), it.Path)
			}
			return nil
		},
	}

	f := cmd.Flags()
	// -h means human-readable in du(1), so declaring --help first keeps cobra
	// from taking the shorthand for help.
	f.Bool("help", false, "show help for du")
	f.BoolVarP(&opts.Human, "human-readable", "h", false, "print sizes as 1.2K, 34M")
	f.IntVarP(&opts.MaxDepth, "max-depth", "d", 0, "also report directories this many levels down")
	f.BoolVarP(&opts.Summarize, "summarize", "s", false, "report only the total for each argument")
	f.BoolVarP(&opts.All, "all", "a", false, "report files as well as directories")
	// The old name for --max-depth, kept so existing invocations keep working.
	f.IntVar(&opts.MaxDepth, "depth", 0, "deprecated alias for --max-depth")
	_ = f.MarkHidden("depth")
	return cmd
}

// duOptions is what the du flags select.
type duOptions struct {
	Human     bool
	MaxDepth  int
	Summarize bool
	All       bool
}

// duEntry is one line of du output.
type duEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// usage reports the space used at arg, and below it when asked.
//
// Sizes come from what the server reports for each directory, which is already
// recursive, so a depth of N costs one listing per directory in the first N
// levels rather than a walk of everything underneath.
func (a *App) usage(ctx context.Context, arg string, opts duOptions) ([]duEntry, error) {
	root, err := a.resolve(ctx, arg)
	if err != nil {
		return nil, err
	}
	info, err := a.client.Stat(ctx, root)
	if err != nil {
		return nil, err
	}

	// Deepest first, then the argument last: du reports a directory after
	// everything it contains.
	levels := make([][]duEntry, 0, max(opts.MaxDepth, 0)+1)
	frontier := []client.ResourceInfo{*info}
	for depth := 1; depth <= opts.MaxDepth && info.IsDir; depth++ {
		var next []client.ResourceInfo
		var level []duEntry
		for _, dir := range frontier {
			if !dir.IsDir {
				continue
			}
			children, err := a.client.List(ctx, dir.Path)
			if err != nil {
				return nil, err
			}
			for _, c := range children {
				if c.IsDir || opts.All {
					level = append(level, duEntry{Path: c.Path, Size: c.Size})
				}
				if c.IsDir {
					next = append(next, c)
				}
			}
		}
		if len(level) == 0 {
			break
		}
		sort.Slice(level, func(i, j int) bool { return level[i].Path < level[j].Path })
		levels = append(levels, level)
		frontier = next
	}

	out := make([]duEntry, 0, len(levels)+1)
	for i := len(levels) - 1; i >= 0; i-- {
		out = append(out, levels[i]...)
	}
	return append(out, duEntry{Path: info.Path, Size: info.Size}), nil
}

func newCatCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "cat PATH...",
		Short: "Write file contents to standard output",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			for _, arg := range args {
				p, err := app.resolve(ctx, arg)
				if err != nil {
					return err
				}
				// Without this the server answers a download of a collection
				// with 501, and the user is told "not implemented" about
				// something that is simply the wrong kind of thing. cat(1)
				// says "Is a directory"; so does this.
				if info, statErr := app.client.Stat(ctx, p); statErr == nil && info.IsDir {
					return cberr.New(cberr.KindUsage, "read", p, "is a directory")
				}
				body, _, err := app.client.Download(ctx, p, 0)
				if err != nil {
					return err
				}
				_, copyErr := io.Copy(app.stdout, body)
				body.Close()
				if copyErr != nil {
					return cberr.Wrap(cberr.KindOther, "read", p, copyErr)
				}
			}
			return nil
		},
	}
}

func newMkdirCmd(app *App) *cobra.Command {
	var parents bool

	cmd := &cobra.Command{
		Use:   "mkdir PATH...",
		Short: "Create a directory",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			for _, arg := range args {
				p, err := app.resolve(ctx, arg)
				if err != nil {
					return err
				}
				if err := app.client.Mkdir(ctx, p, parents); err != nil {
					return err
				}
				app.out.Msg("Created %s", p)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&parents, "parents", "p", false, "create missing parent directories")
	return cmd
}

func newTouchCmd(app *App) *cobra.Command {
	var noCreate bool

	cmd := &cobra.Command{
		Use:   "touch PATH...",
		Short: "Create an empty file",
		Long: "Create an empty file. A file that already exists is left alone, never\n" +
			"emptied.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			for _, arg := range args {
				p, err := app.resolve(ctx, arg)
				if err != nil {
					return err
				}
				if noCreate {
					if _, err := app.client.Stat(ctx, p); err != nil {
						continue // -c: nothing to do for a path that is not there
					}
				}
				err = app.client.Touch(ctx, p)
				switch {
				case err == nil:
					app.out.Msg("Created %s", p)
				case cberr.KindOf(err) == cberr.KindConflict:
					// Already there. touch(1) succeeds in this case, so this
					// does too, but says plainly that nothing changed.
					app.out.Warn("%s already exists; left unchanged", p)
				default:
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&noCreate, "no-create", "c", false, "do not create a file that does not exist")
	return cmd
}

func newRmCmd(app *App) *cobra.Command {
	var recursive, force bool

	cmd := &cobra.Command{
		Use:   "rm PATH...",
		Short: "Delete a file or directory",
		Long: "Delete files or directories.\n\n" +
			"Deleting a directory needs -r, which also deletes everything inside it.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			for _, arg := range args {
				p, err := app.resolve(ctx, arg)
				if err != nil {
					return err
				}

				info, err := app.client.Stat(ctx, p)
				if err != nil {
					if force && cberr.KindOf(err) == cberr.KindNotFound {
						continue
					}
					return err
				}
				if info.IsDir && !recursive {
					return cberr.Usagef("%s is a directory: pass -r to delete it and everything in it", p)
				}
				if err := app.client.Remove(ctx, p); err != nil {
					return err
				}
				app.out.Msg("Removed %s", p)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "delete directories and their contents")
	// coreutils rm accepts -R too, and ls -R trains the habit.
	cmd.Flags().BoolVarP(&recursive, "recursive-upper", "R", false, "same as --recursive")
	_ = cmd.Flags().MarkHidden("recursive-upper")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "ignore paths that do not exist")
	return cmd
}

func newMvCmd(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "mv SOURCE DEST",
		Short: "Move or rename a file or directory",
		Long: "Move or rename inside CERNBox. The file does not travel through your\n" +
			"computer, so it is fast whatever the size.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			src, err := app.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			dst, err := app.resolve(ctx, args[1])
			if err != nil {
				return err
			}

			// "mv a.txt dir/" and "mv a.txt dir" both mean "into dir" when dir
			// is an existing directory, matching mv(1).
			if info, err := app.client.Stat(ctx, dst); err == nil && info.IsDir {
				dst = path.Join(dst, path.Base(src))
			}

			if err := app.client.Move(ctx, src, dst, force); err != nil {
				return err
			}
			app.out.Msg("Moved %s to %s", src, dst)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite the destination if it exists")
	return cmd
}
