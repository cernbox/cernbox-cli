package cli

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newLsCmd(app *App) *cobra.Command {
	var long, all, recursive, rawBytes bool

	cmd := &cobra.Command{
		Use:   "ls [PATH...]",
		Short: "List a directory",
		Long: "List a CERNBox directory.\n\n" +
			"With no argument, lists your home space. Paths are CERNBox paths:\n" +
			"/eos/user/g/gdelmont, home:Documents, or project/cernbox:data.",
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
				if err := app.listOne(ctx, arg, long, all, recursive, !rawBytes); err != nil {
					return err
				}
				if i < len(args)-1 && !app.out.Streaming() {
					app.out.Msg("")
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&long, "long", "l", false, "show size, modification time and permissions")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include entries whose name starts with a dot")
	cmd.Flags().BoolVarP(&recursive, "recursive", "R", false, "list subdirectories recursively")
	// There is no -h shorthand: cobra reserves it for --help, and claiming it
	// panics at startup rather than failing gracefully.
	cmd.Flags().BoolVar(&rawBytes, "bytes", false, "print exact byte counts instead of 1.2K")
	return cmd
}

func (a *App) listOne(ctx context.Context, arg string, long, all, recursive, humanSizes bool) error {
	p, err := a.resolve(ctx, arg)
	if err != nil {
		return err
	}

	var entries []client.ResourceInfo
	if recursive {
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

	if !all {
		filtered := entries[:0]
		for _, e := range entries {
			if !strings.HasPrefix(e.Name, ".") {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

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

	table := output.Table{Items: entries}
	now := time.Now()
	if long {
		table.Headers = []string{"TYPE", "SIZE", "MODIFIED", "NAME"}
		for _, e := range entries {
			table.Rows = append(table.Rows, []string{
				entryType(e),
				formatSize(e.Size, humanSizes),
				output.HumanTime(e.Modified, now),
				displayName(e, p, recursive),
			})
		}
	} else {
		table.Headers = []string{"NAME"}
		for _, e := range entries {
			table.Rows = append(table.Rows, []string{displayName(e, p, recursive)})
		}
	}
	return a.out.Render(table)
}

func entryType(e client.ResourceInfo) string {
	if e.IsDir {
		return "dir"
	}
	return "file"
}

func displayName(e client.ResourceInfo, root string, recursive bool) string {
	name := e.Name
	if recursive {
		name = strings.TrimPrefix(strings.TrimPrefix(e.Path, root), "/")
	}
	if e.IsDir {
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
		Long: "Search a CERNBox subtree by name. The search runs on the server, so it\n" +
			"does not walk the tree from the client.",
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

func newDuCmd(app *App) *cobra.Command {
	var depth int
	var humanSizes bool

	cmd := &cobra.Command{
		Use:   "du PATH",
		Short: "Show space used by a directory",
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

			type usage struct {
				Path string `json:"path"`
				Size int64  `json:"size"`
			}
			items := []usage{}

			if info.IsDir && depth > 0 {
				children, err := app.client.List(ctx, p)
				if err != nil {
					return err
				}
				for _, c := range children {
					items = append(items, usage{Path: c.Path, Size: c.Size})
				}
			}
			items = append(items, usage{Path: info.Path, Size: info.Size})

			table := output.Table{Headers: []string{"SIZE", "PATH"}, Items: items}
			for _, it := range items {
				table.Rows = append(table.Rows, []string{formatSize(it.Size, humanSizes), it.Path})
			}
			return app.out.Render(table)
		},
	}

	cmd.Flags().IntVar(&depth, "depth", 0, "also show entries this many levels below PATH")
	cmd.Flags().BoolVar(&humanSizes, "human-readable", true, "print sizes like 1.2K")
	return cmd
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
	return &cobra.Command{
		Use:   "touch PATH...",
		Short: "Create an empty file",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			for _, arg := range args {
				p, err := app.resolve(ctx, arg)
				if err != nil {
					return err
				}
				if err := app.client.Touch(ctx, p); err != nil {
					return err
				}
				app.out.Msg("Created %s", p)
			}
			return nil
		},
	}
}

func newRmCmd(app *App) *cobra.Command {
	var recursive, force bool

	cmd := &cobra.Command{
		Use:   "rm PATH...",
		Short: "Delete a file or directory",
		Long: "Delete files or directories.\n\n" +
			"Deleting a directory requires -r. WebDAV DELETE on a directory is always\n" +
			"recursive, so the flag is the only thing standing between a typo and the\n" +
			"whole subtree.",
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
	cmd.Flags().BoolVarP(&force, "force", "f", false, "ignore paths that do not exist")
	return cmd
}

func newMvCmd(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "mv SOURCE DEST",
		Short: "Move or rename a file or directory",
		Long: "Move or rename within CERNBox. The data never leaves the server, so this\n" +
			"is fast regardless of size.",
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
