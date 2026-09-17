package cli

import (
	"io"
	"os"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newTrashCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trash",
		Short: "List, restore and purge deleted files",
		Long: "Work with the trash bin.\n\n" +
			"Each space has its own bin. With no --space, commands act on your home\n" +
			"space; pass --space to reach a project's bin.",
	}
	cmd.AddCommand(newTrashListCmd(app), newTrashRestoreCmd(app), newTrashPurgeCmd(app))
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

func newTrashListCmd(app *App) *cobra.Command {
	var space string

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List deleted files",
		Example: "  cernbox trash list\n  cernbox trash list --space project/cernbox",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			base, err := app.trashBasePath(cmd, space)
			if err != nil {
				return err
			}
			items, err := app.client.ListTrash(ctx, base)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				app.out.Msg("The trash bin is empty.")
			}

			now := time.Now()
			table := output.Table{Headers: []string{"KEY", "TYPE", "SIZE", "DELETED", "ORIGINAL PATH"}, Items: items}
			for _, it := range items {
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
			return app.out.Render(table)
		},
	}

	spaceFlag(cmd, &space)
	return cmd
}

func newTrashRestoreCmd(app *App) *cobra.Command {
	var space, to string

	cmd := &cobra.Command{
		Use:   "restore KEY...",
		Short: "Restore a deleted file",
		Long: "Restore deleted files. Without --to they go back where they came from,\n" +
			"which is what the KEY's original path column shows.",
		Example: "  cernbox trash restore 1a2b3c\n" +
			"  cernbox trash restore 1a2b3c --to /eos/user/g/gdelmont/recovered.txt",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if to != "" && len(args) > 1 {
				return cberr.Usagef("--to takes a single destination, so it cannot be used with several keys")
			}

			base, err := app.trashBasePath(cmd, space)
			if err != nil {
				return err
			}

			dst := ""
			if to != "" {
				dst, err = app.resolve(ctx, to)
				if err != nil {
					return err
				}
			}

			for _, key := range args {
				if err := app.client.RestoreTrash(ctx, key, dst, base); err != nil {
					return err
				}
				app.out.Msg("Restored %s", key)
			}
			return nil
		},
	}

	spaceFlag(cmd, &space)
	cmd.Flags().StringVar(&to, "to", "", "restore to this path instead of the original location")
	return cmd
}

func newTrashPurgeCmd(app *App) *cobra.Command {
	var space string
	var all, yes bool

	cmd := &cobra.Command{
		Use:   "purge [KEY...]",
		Short: "Permanently delete items from the trash bin",
		Long: "Permanently delete trash items. This cannot be undone.\n\n" +
			"With --all the whole bin is emptied, which is why it asks for confirmation\n" +
			"unless you pass --yes.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			switch {
			case all && len(args) > 0:
				return cberr.Usagef("pass either --all or a list of keys, not both")
			case !all && len(args) == 0:
				return cberr.Usagef("pass the keys to purge, or --all to empty the bin")
			}

			base, err := app.trashBasePath(cmd, space)
			if err != nil {
				return err
			}

			if all {
				// Emptying the bin destroys everything a user might be about to
				// recover, so make them say so.
				if !yes && !confirm(app, "Permanently delete everything in the trash bin?") {
					return cberr.New(cberr.KindOther, "purge the trash bin", "", "cancelled")
				}
				if err := app.client.PurgeTrash(ctx, "", base); err != nil {
					return err
				}
				app.out.Msg("Emptied the trash bin.")
				return nil
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
	cmd.Flags().BoolVar(&all, "all", false, "empty the whole trash bin")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}

// confirm asks a yes/no question. With no terminal there is nobody to ask, so
// it answers no: a script that meant to purge should pass --yes.
func confirm(app *App, question string) bool {
	if !output.IsTerminal(os.Stdin) {
		app.out.Warn("not running interactively; pass --yes to confirm")
		return false
	}
	if _, err := io.WriteString(app.stderr, question+" [y/N] "); err != nil {
		return false
	}
	var answer string
	if _, err := fmtScan(&answer); err != nil {
		return false
	}
	return answer == "y" || answer == "Y" || answer == "yes"
}

// fmtScan is a seam so the confirmation prompt can be exercised in tests.
var fmtScan = func(a *string) (int, error) {
	return fscanln(os.Stdin, a)
}

func fscanln(r io.Reader, a *string) (int, error) {
	buf := make([]byte, 0, 16)
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				break
			}
			buf = append(buf, one[0])
		}
		if err != nil {
			break
		}
		if len(buf) > 64 {
			break
		}
	}
	*a = string(buf)
	return len(buf), nil
}
