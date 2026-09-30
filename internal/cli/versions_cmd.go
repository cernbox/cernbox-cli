package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/cernbox/cernbox-cli/pkg/textdiff"
	"github.com/spf13/cobra"
)

func newVersionsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "versions",
		Aliases: []string{"version-history"},
		Short:   "List, restore and download earlier versions of a file",
		Long: "Work with a file's earlier versions.\n\n" +
			"Versions follow the file itself, so the history survives a rename.",
	}
	cmd.AddCommand(newVersionsListCmd(app), newVersionsDiffCmd(app),
		newVersionsRestoreCmd(app), newVersionsDownloadCmd(app))
	return cmd
}

func newVersionsListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "list PATH",
		Short:   "List the earlier versions of a file",
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
				app.out.Msg("%s has no earlier versions.", info.Path)
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
		Short: "Make an earlier version the current one",
		Long: "Make an earlier version the current one.\n\n" +
			"Nothing is lost. The version you replace becomes a version too, so you can\n" +
			"undo it.",
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
		Short: "Download an earlier version without restoring it",
		Long: "Download an earlier version and leave the current one alone.\n\n" +
			"With no --output the file is written to the current directory as\n" +
			"NAME.VERSION.",
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

// maxDiffSize is the largest version this will read. Diffing means holding both
// sides in memory as lines, and something this big is not a text file anybody is
// reading a diff of.
const maxDiffSize = 16 << 20

func newVersionsDiffCmd(app *App) *cobra.Command {
	var context int
	var plain bool

	cmd := &cobra.Command{
		Use:   "diff PATH [VERSION] [VERSION]",
		Short: "Show what changed between versions of a file",
		Long: "Show what changed, as a unified diff.\n\n" +
			"With one version, that version against the file as it is now. With two,\n" +
			"one against the other. With none, the most recent version against now —\n" +
			"which answers 'what did I just change'.",
		Example: "  cernbox versions diff report.md\n" +
			"  cernbox versions diff report.md 1790771950.00001662\n" +
			"  cernbox versions diff report.md OLDER NEWER",
		Args: cobra.RangeArgs(1, 3),
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

			left, right, err := app.diffSides(ctx, info, args[1:])
			if err != nil {
				return err
			}

			out := textdiff.Unified(left.text, right.text, textdiff.Options{
				Context:  context,
				OldLabel: left.label,
				NewLabel: right.label,
				Colour:   !plain && app.out.UsesColor(),
			})
			if out == "" {
				app.out.Msg("%s and %s are identical.", left.label, right.label)
				return nil
			}
			// Written straight out: a diff is the command's data, already laid out
			// in lines, so it goes to stdout as it is and pipes cleanly.
			_, err = fmt.Fprint(app.stdout, out)
			return err
		},
	}

	cmd.Flags().IntVarP(&context, "context", "U", 3, "unchanged lines to show around each change")
	cmd.Flags().BoolVar(&plain, "plain", false, "never colour the output")
	return cmd
}

// diffSide is one thing being compared.
type diffSide struct {
	label string
	text  string
}

// diffSides works out what to compare and fetches both.
//
// The default is the newest version against the file as it is, because that is
// the question somebody asking for a diff almost always has.
func (a *App) diffSides(ctx context.Context, info *client.ResourceInfo, keys []string) (diffSide, diffSide, error) {
	var none diffSide

	switch len(keys) {
	case 0:
		versions, err := a.client.ListVersions(ctx, info.ID)
		if err != nil {
			return none, none, err
		}
		if len(versions) == 0 {
			return none, none, cberr.Usagef("%s has no earlier versions to compare with", info.Path)
		}
		// The first is the most recent: ListVersions sorts newest first.
		left, err := a.readVersion(ctx, info, versions[0].Key)
		if err != nil {
			return none, none, err
		}
		right, err := a.readCurrent(ctx, info)
		return left, right, err

	case 1:
		left, err := a.readVersion(ctx, info, keys[0])
		if err != nil {
			return none, none, err
		}
		right, err := a.readCurrent(ctx, info)
		return left, right, err

	default:
		left, err := a.readVersion(ctx, info, keys[0])
		if err != nil {
			return none, none, err
		}
		right, err := a.readVersion(ctx, info, keys[1])
		return left, right, err
	}
}

func (a *App) readCurrent(ctx context.Context, info *client.ResourceInfo) (diffSide, error) {
	body, _, err := a.client.Download(ctx, info.Path, 0)
	if err != nil {
		return diffSide{}, err
	}
	return readDiffSide(body, path.Base(info.Path)+" (now)")
}

func (a *App) readVersion(ctx context.Context, info *client.ResourceInfo, key string) (diffSide, error) {
	body, _, err := a.client.DownloadVersion(ctx, info.ID, key)
	if err != nil {
		return diffSide{}, err
	}
	return readDiffSide(body, path.Base(info.Path)+" ("+key+")")
}

// readDiffSide reads one side, refusing what a line diff would only mangle.
func readDiffSide(body io.ReadCloser, label string) (diffSide, error) {
	defer body.Close()

	data, err := io.ReadAll(io.LimitReader(body, maxDiffSize+1))
	if err != nil {
		return diffSide{}, err
	}
	if len(data) > maxDiffSize {
		return diffSide{}, cberr.Usagef("%s is too big to diff; use 'versions download'", label)
	}
	if textdiff.IsBinary(data) {
		return diffSide{}, cberr.Usagef("%s is not text, so there is nothing to read in a diff; "+
			"use 'versions download' to compare it another way", label)
	}
	return diffSide{label: label, text: string(data)}, nil
}
