package cli

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/cernbox/cernbox-cli/pkg/pathspec"
	"github.com/cernbox/cernbox-cli/pkg/transfer"
	"github.com/spf13/cobra"
)

// transferFlags are shared by cp, get and put.
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
		Short: "Copy between the local filesystem and CERNBox",
		Long: "Copy files between the local filesystem and CERNBox.\n\n" +
			"The CERNBox side must be marked with cb:. On lxplus /eos is both a\n" +
			"CERNBox path and a local FUSE mount, so an unmarked path is ambiguous and\n" +
			"guessing would silently do the wrong thing to your data.\n\n" +
			"If you find the prefix awkward, use get and put instead: they are\n" +
			"unambiguous by position.",
		Example: "  cernbox cp ./report.pdf cb:/eos/user/g/gdelmont/Documents/\n" +
			"  cernbox cp -r cb:/eos/project/c/cernbox/data ./data\n" +
			"  cernbox cp cb:/eos/user/g/gdelmont/a.txt cb:/eos/user/g/gdelmont/b.txt",
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
				return app.serverSideCopy(ctx, src, dst, flags)
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

func newPutCmd(app *App) *cobra.Command {
	flags := &transferFlags{}

	cmd := &cobra.Command{
		Use:   "put LOCAL REMOTE",
		Short: "Upload a local file or directory to CERNBox",
		Long: "Upload to CERNBox. Unlike cp, the sides are unambiguous by position, so\n" +
			"no cb: prefix is needed.\n\n" +
			"Large files are uploaded in chunks and resume where they left off if the\n" +
			"transfer is interrupted.",
		Example: "  cernbox put ./report.pdf /eos/user/g/gdelmont/Documents/\n" +
			"  cernbox put -r ./data /eos/project/c/cernbox/data",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			dst, err := pathspec.ParseRemote(args[1])
			if err != nil {
				return cberr.Usagef("%v", err)
			}
			return app.runUpload(ctx, cmd, args[0], dst, flags)
		},
	}

	flags.register(cmd)
	return cmd
}

func newGetCmd(app *App) *cobra.Command {
	flags := &transferFlags{}

	cmd := &cobra.Command{
		Use:   "get REMOTE [LOCAL]",
		Short: "Download from CERNBox to the local filesystem",
		Long: "Download from CERNBox. With no local path, the file lands in the current\n" +
			"directory under its own name.\n\n" +
			"An interrupted download resumes from where it stopped. A recursive\n" +
			"download is served as a single archive when the server supports it, which\n" +
			"is much faster for a directory of many small files.",
		Example: "  cernbox get /eos/user/g/gdelmont/Documents/report.pdf\n" +
			"  cernbox get -r /eos/project/c/cernbox/data ./data",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			src, err := pathspec.ParseRemote(args[0])
			if err != nil {
				return cberr.Usagef("%v", err)
			}
			local := "."
			if len(args) == 2 {
				local = args[1]
			}
			return app.runDownload(ctx, cmd, src, local, flags)
		},
	}

	flags.register(cmd)
	return cmd
}

// runUpload copies a local path to a remote spec.
func (a *App) runUpload(ctx context.Context, cmd *cobra.Command, local string, dst pathspec.Spec, flags *transferFlags) error {
	engine, err := a.transferEngine(*flags)
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
	if a.destIsDirectory(ctx, remote, dst) {
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
	engine, err := a.transferEngine(*flags)
	if err != nil {
		return err
	}

	remote, err := a.resolveSpec(ctx, src)
	if err != nil {
		return err
	}

	info, err := a.client.Stat(ctx, remote)
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

// serverSideCopy duplicates within CERNBox without moving the bytes through
// the client.
func (a *App) serverSideCopy(ctx context.Context, src, dst pathspec.Spec, flags *transferFlags) error {
	from, err := a.resolveSpec(ctx, src)
	if err != nil {
		return err
	}
	to, err := a.resolveSpec(ctx, dst)
	if err != nil {
		return err
	}

	if info, err := a.client.Stat(ctx, to); err == nil && info.IsDir {
		to = path.Join(to, path.Base(from))
	}
	if flags.dryRun {
		a.out.Msg("Would copy %s to %s", from, to)
		return nil
	}
	if err := a.client.Copy(ctx, from, to, flags.force); err != nil {
		return err
	}
	a.out.Msg("Copied %s to %s", from, to)
	return nil
}

// destIsDirectory reports whether a remote destination should be treated as a
// container rather than the target name.
func (a *App) destIsDirectory(ctx context.Context, remote string, spec pathspec.Spec) bool {
	if spec.TrailingSlash {
		return true
	}
	info, err := a.client.Stat(ctx, remote)
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
