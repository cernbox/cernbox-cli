package cli

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/cernbox/cernbox-cli/pkg/pathspec"
	"github.com/cernbox/cernbox-cli/pkg/transfer"
	"github.com/spf13/cobra"
)

// transferFlags are the options of cp.
type transferFlags struct {
	recursive bool
	force     bool
	dryRun    bool
	verify    bool
	noArchive bool
	jobs      int
}

func (t *transferFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVarP(&t.recursive, "recursive", "r", false, "copy directories and their contents")
	// coreutils cp accepts -R too.
	f.BoolVarP(&t.recursive, "recursive-upper", "R", false, "same as --recursive")
	_ = f.MarkHidden("recursive-upper")
	f.BoolVarP(&t.force, "force", "f", false, "overwrite existing destinations")
	f.BoolVar(&t.dryRun, "dry-run", false, "report what would be transferred without doing it")
	f.BoolVar(&t.verify, "verify", false, "compute and check checksums")
	f.BoolVar(&t.noArchive, "no-archive", false, "download recursively file by file instead of as one archive")
	f.IntVarP(&t.jobs, "jobs", "j", 0, "number of files to transfer at once")
}

func newCpCmd(app *App) *cobra.Command {
	flags := &transferFlags{}

	cmd := &cobra.Command{
		Use:   "cp SOURCE DEST",
		Short: "Copy between your computer and CERNBox",
		Long: "Copy files between your computer and CERNBox, or within CERNBox.\n" +
			"\n" +
			"Mark the CERNBox side with cb:, because a path like /eos/... can exist on your\n" +
			"computer too.\n" +
			"\n" +
			"Large uploads go up in chunks and an interrupted transfer carries on where it\n" +
			"stopped. A whole directory comes down as one archive when the server can\n" +
			"build one, which is much faster for many small files.\n" +
			"\n" +
			"An admin can write USER@cb: to reach a path as another user sees it. A copy\n" +
			"between two users streams through this computer, since the server cannot\n" +
			"copy across them itself.",
		Example: "  cernbox cp ./report.pdf cb:~/Documents/\n" +
			"  cernbox cp -r cb:/eos/project/c/cernbox/data ./data\n" +
			"  cernbox cp cb:~/a.txt cb:~/b.txt\n" +
			"  cernbox cp cb:~/report.pdf marie@cb:~/Documents/",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			src, dst, err := pathspec.ParseTransferPair(args[0], args[1])
			if err != nil {
				return cberr.Usagef("%v", err)
			}

			switch {
			case src.IsRemote() && dst.IsRemote():
				return app.remoteCopy(ctx, src, dst, flags)
			case src.IsLocal():
				return app.runUpload(ctx, cmd, src.Path, dst, flags)
			default:
				return app.runDownload(ctx, cmd, src, dst.Path, flags)
			}
		},
	}

	flags.register(cmd)
	return cmd
}

// runUpload copies a local path to a remote spec.
func (a *App) runUpload(ctx context.Context, cmd *cobra.Command, local string, dst pathspec.Spec, flags *transferFlags) error {
	c, err := a.clientFor(ctx, dst)
	if err != nil {
		return err
	}
	engine, err := a.transferEngineFor(c, *flags)
	if err != nil {
		return err
	}

	remote, err := a.resolveSpec(ctx, dst)
	if err != nil {
		return err
	}

	info, err := os.Stat(local)
	if err != nil {
		return cberr.Wrap(cberr.KindNotFound, "read", local, err)
	}
	if info.IsDir() && !flags.recursive {
		return cberr.Usagef("%s is a directory: pass -r to upload it", local)
	}

	// A destination that is an existing directory, or was written with a
	// trailing slash, means "into it" — the same rule cp(1) uses.
	if destIsDirectory(ctx, c, remote, dst) {
		remote = path.Join(remote, filepath.Base(strings.TrimRight(local, string(os.PathSeparator))))
	}

	stats, err := engine.UploadTree(ctx, local, remote)
	if err != nil {
		return err
	}
	return a.reportTransfer(stats, "Uploaded")
}

// runDownload copies a remote spec to a local path.
func (a *App) runDownload(ctx context.Context, cmd *cobra.Command, src pathspec.Spec, local string, flags *transferFlags) error {
	c, err := a.clientFor(ctx, src)
	if err != nil {
		return err
	}
	engine, err := a.transferEngineFor(c, *flags)
	if err != nil {
		return err
	}

	remote, err := a.resolveSpec(ctx, src)
	if err != nil {
		return err
	}

	info, err := c.Stat(ctx, remote)
	if err != nil {
		return err
	}
	if info.IsDir && !flags.recursive {
		return cberr.Usagef("%s is a directory: pass -r to download it", remote)
	}

	// A local destination that is an existing directory means "into it".
	if st, statErr := os.Stat(local); statErr == nil && st.IsDir() {
		local = filepath.Join(local, path.Base(remote))
	}

	stats, err := engine.DownloadTree(ctx, remote, local)
	if err != nil {
		return err
	}
	return a.reportTransfer(stats, "Downloaded")
}

// remoteCopy duplicates within CERNBox. When both sides are acted on as the same
// user the server copies it without the bytes moving through the client. When
// they are not, it cannot — a COPY carries one credential — so the bytes are
// relayed: read as one user, written as the other.
func (a *App) remoteCopy(ctx context.Context, src, dst pathspec.Spec, flags *transferFlags) error {
	from, err := a.resolveSpec(ctx, src)
	if err != nil {
		return err
	}
	to, err := a.resolveSpec(ctx, dst)
	if err != nil {
		return err
	}
	srcClient, err := a.clientFor(ctx, src)
	if err != nil {
		return err
	}
	dstClient, err := a.clientFor(ctx, dst)
	if err != nil {
		return err
	}

	if destIsDirectory(ctx, dstClient, to, dst) {
		to = path.Join(to, path.Base(from))
	}

	if srcClient != dstClient {
		engine, err := a.transferEngineFor(srcClient, *flags)
		if err != nil {
			return err
		}
		stats, err := engine.Relay(ctx, from, dstClient, to)
		if err != nil {
			return err
		}
		return a.reportTransfer(stats, "Copied")
	}

	if flags.dryRun {
		a.out.Msg("Would copy %s to %s", from, to)
		return nil
	}
	if err := srcClient.Copy(ctx, from, to, flags.force); err != nil {
		return err
	}
	a.out.Msg("Copied %s to %s", from, to)
	return nil
}

// destIsDirectory reports whether a remote destination should be treated as a
// container rather than the target name.
func destIsDirectory(ctx context.Context, c *client.Client, remote string, spec pathspec.Spec) bool {
	if spec.TrailingSlash {
		return true
	}
	info, err := c.Stat(ctx, remote)
	return err == nil && info.IsDir
}

func (a *App) reportTransfer(stats *transfer.Stats, verb string) error {
	a.out.Msg("%s %d files (%s) in %s, %d skipped",
		verb, stats.Files, output.HumanSize(stats.Bytes),
		stats.Duration.Round(1e6), stats.Skipped)

	if a.out.Format() == output.FormatJSON {
		return a.out.Object(stats)
	}
	return nil
}
