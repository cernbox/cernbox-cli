package cli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

// stdoutTarget is the destination that means "write to standard output", the
// same spelling the rest of the CLI uses for a stream instead of a file.
const stdoutTarget = "-"

func newArchiveCmd(app *App) *cobra.Command {
	var format, to string
	var force bool

	cmd := &cobra.Command{
		Use:   "archive PATH...",
		Short: "Download files and directories as one archive",
		Long: "Download from CERNBox as a single archive.\n\n" +
			"The server packs everything, so a directory of many small files costs one\n" +
			"request instead of one per file.\n\n" +
			"Without --to, the archive lands in the current directory named after what\n" +
			"you asked for. Use '--to -' to send it to another program.",
		Example: "  cernbox archive /eos/project/c/cernbox/data\n" +
			"  cernbox archive --format zip --to notes.zip Documents\n" +
			"  cernbox archive --to - Documents | tar -t",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			switch format {
			case client.ArchiveTar, client.ArchiveZip:
			default:
				return cberr.Usagef("unknown format %q: want tar or zip", format)
			}

			paths := make([]string, 0, len(args))
			for _, arg := range args {
				p, err := app.resolve(ctx, arg)
				if err != nil {
					return err
				}
				paths = append(paths, p)
			}

			return app.writeArchive(ctx, paths, format, archiveDest(to, paths, format), force)
		},
	}

	cmd.Flags().StringVar(&format, "format", client.ArchiveTar, "tar or zip")
	cmd.Flags().StringVar(&to, "to", "", "write the archive here, or - for standard output")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite an existing file")
	return cmd
}

// writeArchive asks the server for the archive and saves it.
func (a *App) writeArchive(ctx context.Context, paths []string, format, dest string, force bool) error {
	body, err := a.client.Archive(ctx, paths, format)
	if err != nil {
		return err
	}
	defer body.Close()

	if dest == stdoutTarget {
		// No meter here. The archive is going down a pipe, and the only thing a
		// bar could be drawn against is stderr, which the program on the other
		// end may well be reading too.
		if _, err := io.Copy(a.stdout, body); err != nil {
			return cberr.Wrap(cberr.KindOther, "write archive", stdoutTarget, err)
		}
		return nil
	}

	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if !force {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(dest, flags, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return cberr.New(cberr.KindOther, "write archive", dest,
			"this file already exists. Pass --force to overwrite it, or --to to write somewhere else")
	}
	if err != nil {
		return cberr.Wrap(cberr.KindOther, "create", dest, err)
	}
	defer f.Close()

	m := a.newMeter(filepath.Base(dest), -1)
	n, err := io.Copy(m.Writer(f), body)
	m.Stop()
	if err != nil {
		// Half an archive cannot be opened, so leaving it would only look like a
		// download that worked. The error says what happened.
		os.Remove(dest)
		return cberr.Wrap(cberr.KindOther, "write archive", dest, err)
	}

	a.out.Msg("Wrote %s (%s)", dest, output.HumanSize(n))
	if a.out.Format() == output.FormatJSON {
		return a.out.Object(archiveResult{Path: dest, Format: format, Bytes: n, Sources: paths})
	}
	return nil
}

type archiveResult struct {
	Path    string   `json:"path"`
	Format  string   `json:"format"`
	Bytes   int64    `json:"bytes"`
	Sources []string `json:"sources"`
}

// archiveDest decides where the archive is written. An empty --to means the
// current directory, and a --to that names an existing directory means inside
// it, which is the rule get and cp already follow.
func archiveDest(to string, paths []string, format string) string {
	name := archiveName(paths, format)
	if to == "" {
		return name
	}
	if to == stdoutTarget {
		return stdoutTarget
	}
	if st, err := os.Stat(to); err == nil && st.IsDir() {
		return filepath.Join(to, name)
	}
	return to
}

// archiveName is what the archive is called when the user did not say. One path
// gives the archive its own name; several have no name in common, so they land
// in one called "archive".
func archiveName(paths []string, format string) string {
	if len(paths) == 1 {
		switch base := path.Base(paths[0]); base {
		case "", ".", "/":
		default:
			return base + "." + format
		}
	}
	return "archive." + format
}
