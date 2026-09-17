package cli

import (
	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/cernbox/cernbox-cli/pkg/pathspec"
	"github.com/cernbox/cernbox-cli/pkg/transfer"
	"github.com/spf13/cobra"
)

func newSyncCmd(app *App) *cobra.Command {
	flags := &transferFlags{}
	var del, hidden bool

	cmd := &cobra.Command{
		Use:   "sync SOURCE DEST",
		Short: "Mirror a directory onto CERNBox, or CERNBox onto a directory",
		Long: "Make one directory look like another.\n\n" +
			"This is a one-way mirror, not two-way synchronisation. Real bidirectional\n" +
			"sync needs per-file state to tell \"changed here\" from \"deleted there\";\n" +
			"without it the two are indistinguishable, which is how a sync tool deletes\n" +
			"data it should have uploaded. If you want that, use the CERNBox desktop\n" +
			"client, which keeps the state this command deliberately does not.\n\n" +
			"The CERNBox side is marked with cb:, as it is for cp. Files are compared\n" +
			"by size and modification time, so nothing is re-sent that has not changed.\n" +
			"Without --delete, sync only adds and updates.",
		Example: "  cernbox sync ./data cb:/eos/project/c/cernbox/data\n" +
			"  cernbox sync cb:/eos/project/c/cernbox/data ./data --delete\n" +
			"  cernbox sync ./data cb:/eos/project/c/cernbox/data --delete --dry-run",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			src, dst, err := pathspec.ParseTransferPair(args[0], args[1])
			if err != nil {
				return cberr.Usagef("%v", err)
			}
			if src.IsRemote() && dst.IsRemote() {
				return cberr.Usagef("sync mirrors between the local filesystem and CERNBox, " +
					"so exactly one side must be a CERNBox path")
			}

			opts := transfer.SyncOptions{Delete: del, IncludeHidden: hidden}
			var localPath string
			var remoteSpec pathspec.Spec
			if src.IsLocal() {
				opts.Direction = transfer.Push
				localPath, remoteSpec = src.Path, dst
			} else {
				opts.Direction = transfer.Pull
				localPath, remoteSpec = dst.Path, src
			}

			remotePath, err := app.resolveSpec(ctx, remoteSpec)
			if err != nil {
				return err
			}

			engine, err := app.transferEngine(*flags)
			if err != nil {
				return err
			}

			// --delete can remove a lot of data on the strength of one
			// mistyped path, so say what is about to happen before doing it.
			if del && !flags.dryRun {
				app.out.Msg("Mirroring with --delete: entries missing from the source will be removed from the destination.")
			}

			stats, err := engine.Sync(ctx, localPath, remotePath, opts)
			if err != nil {
				return err
			}

			verb := "Mirrored"
			if flags.dryRun {
				verb = "Would mirror"
			}
			app.out.Msg("%s %s: %d created, %d updated, %d deleted, %d unchanged (%s) in %s",
				verb, opts.Direction, stats.Created, stats.Updated, stats.Deleted, stats.Skipped,
				output.HumanSize(stats.Bytes), stats.Duration.Round(1e6))

			if app.out.Format() == output.FormatJSON {
				return app.out.Object(stats)
			}
			return nil
		},
	}

	flags.register(cmd)
	cmd.Flags().BoolVar(&del, "delete", false, "remove destination entries the source does not have")
	cmd.Flags().BoolVar(&hidden, "hidden", false, "include entries whose name starts with a dot")
	return cmd
}
