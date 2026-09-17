package cli

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newVersionsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "versions",
		Aliases: []string{"version-history"},
		Short:   "List, restore and download previous versions of a file",
		Long: "Work with a file's version history.\n\n" +
			"Versions are addressed by the file's identity rather than its path, so\n" +
			"history survives a rename.",
	}
	cmd.AddCommand(newVersionsListCmd(app), newVersionsRestoreCmd(app), newVersionsDownloadCmd(app))
	return cmd
}

func newVersionsListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "list PATH",
		Short:   "List the previous versions of a file",
		Example: "  cernbox versions list /eos/user/g/gdelmont/report.pdf",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			if info.IsDir {
				return cberr.Usagef("%s is a directory: only files have versions", info.Path)
			}

			versions, err := app.client.ListVersions(ctx, info.ID)
			if err != nil {
				return err
			}
			if len(versions) == 0 {
				app.out.Msg("%s has no previous versions.", info.Path)
			}

			now := time.Now()
			table := output.Table{Headers: []string{"VERSION", "SIZE", "MODIFIED"}, Items: versions}
			for _, v := range versions {
				table.Rows = append(table.Rows, []string{
					v.Key, output.HumanSize(v.Size), output.HumanTime(v.Modified, now),
				})
			}
			return app.out.Render(table)
		},
	}
}

func newVersionsRestoreCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "restore PATH VERSION",
		Short: "Make a previous version current",
		Long: "Restore a previous version.\n\n" +
			"This is not destructive: the version being replaced becomes a version in\n" +
			"its own right, so a restore can itself be undone.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			if err := app.client.RestoreVersion(ctx, info.ID, args[1]); err != nil {
				return err
			}
			app.out.Msg("Restored version %s of %s", args[1], info.Path)
			return nil
		},
	}
}

func newVersionsDownloadCmd(app *App) *cobra.Command {
	var out string

	cmd := &cobra.Command{
		Use:   "download PATH VERSION",
		Short: "Download a previous version without restoring it",
		Long: "Download a previous version to a local file, leaving the current version\n" +
			"untouched. With no --output the file is written to the current directory\n" +
			"as NAME.VERSION.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}

			dst := out
			if dst == "" {
				dst = info.Name + "." + args[1]
			}
			if st, statErr := os.Stat(dst); statErr == nil && st.IsDir() {
				dst = filepath.Join(dst, info.Name+"."+args[1])
			}

			body, _, err := app.client.DownloadVersion(ctx, info.ID, args[1])
			if err != nil {
				return err
			}
			defer body.Close()

			// Write through a temporary file and rename, so an interrupted
			// download never leaves a truncated file under the final name.
			tmp := dst + ".part"
			f, err := os.Create(tmp)
			if err != nil {
				return cberr.Wrap(cberr.KindOther, "create", tmp, err)
			}
			n, copyErr := io.Copy(f, body)
			closeErr := f.Close()
			if copyErr != nil {
				os.Remove(tmp)
				return cberr.Wrap(cberr.KindOther, "download a version", dst, copyErr)
			}
			if closeErr != nil {
				os.Remove(tmp)
				return cberr.Wrap(cberr.KindOther, "write", tmp, closeErr)
			}
			if err := os.Rename(tmp, dst); err != nil {
				return cberr.Wrap(cberr.KindOther, "write", dst, err)
			}

			app.out.Msg("Wrote %s (%s)", dst, output.HumanSize(n))
			return nil
		},
	}

	cmd.Flags().StringVarP(&out, "output-file", "F", "", "write to this local path")
	return cmd
}
