package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/spf13/cobra"
)

// tail follows a file that something else is writing, which on CERNBox is
// usually a batch job's log. Doing it by hand means downloading the whole file
// again to see the line that was added, or reloading a page.
//
// It costs almost nothing, because the server answers a ranged GET: measured,
// a request for bytes=9- came back 206 with the nine bytes after the offset and
// nothing else. So following a log is one Stat plus, when it has grown, a
// transfer of exactly what was appended.
//
// What it cannot do is wait for the server to say something. There are no
// notifications on this surface, so following is a poll — unlike the outbox,
// which watches a local folder and gets told.

const (
	// defaultTailLines is tail(1)'s own default.
	defaultTailLines = 10

	// defaultTailInterval is how often a followed file is asked about. Two
	// seconds is tail -f's own default, and the request it makes is a Stat.
	defaultTailInterval = 2 * time.Second

	// tailWindow is the first guess at how far back the last N lines start. It
	// doubles until enough line endings are in hand.
	tailWindow = 8 << 10

	// maxTailWindow bounds that search. A file with no line endings in it at all
	// would otherwise be downloaded whole to find a line that is not there.
	maxTailWindow = 8 << 20
)

func newTailCmd(app *App) *cobra.Command {
	var lines int
	var follow bool
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "tail PATH...",
		Short: "Show the end of a file, and optionally follow it",
		Long: "Show the last lines of a file, like tail.\n" +
			"\n" +
			"-f keeps watching and prints what gets appended, which is how you follow a\n" +
			"log a job is still writing without downloading it again each time: only the\n" +
			"new bytes are transferred.\n" +
			"\n" +
			"Following polls, because the server has nothing to announce a change with.\n" +
			"Use --timeout to stop after a while, or interrupt it.",
		Example: "  cernbox tail job.log\n" +
			"  cernbox tail -f -n 50 job.log\n" +
			"  cernbox tail -f --timeout 1h job.log",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if lines < 0 {
				return cberr.Usagef("-n wants a number of lines, not %d", lines)
			}
			if interval <= 0 {
				return cberr.Usagef("--interval wants a positive duration")
			}
			return app.runTail(ctx, args, lines, follow, interval)
		},
	}

	cmd.Flags().IntVarP(&lines, "lines", "n", defaultTailLines, "how many lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep watching and print what is appended")
	cmd.Flags().DurationVar(&interval, "interval", defaultTailInterval,
		"how often to look for new bytes while following")
	return cmd
}

// tailTarget is one file being followed, and where reading it got to.
type tailTarget struct {
	path   string
	offset int64
	// etag tells a file that was rewritten in place from one that was appended
	// to. A size alone cannot: a log rotated and replaced by one of the same
	// length would otherwise be followed from the middle of its first line.
	etag string
}

func (a *App) runTail(ctx context.Context, args []string, lines int, follow bool, interval time.Duration) error {
	targets := make([]*tailTarget, 0, len(args))
	for _, arg := range args {
		p, err := a.resolve(ctx, arg)
		if err != nil {
			return err
		}
		info, err := a.client.Stat(ctx, p)
		if err != nil {
			return err
		}
		// cat(1) says "Is a directory" rather than letting the server answer a
		// download of a collection with 501, and so does this.
		if info.IsDir {
			return cberr.New(cberr.KindUsage, "read", p, "is a directory")
		}
		targets = append(targets, &tailTarget{path: p, etag: info.ETag})
	}

	// Headers only when there is more than one file to tell apart, which is
	// tail's own rule, and never when quiet.
	headers := len(targets) > 1 && !a.out.IsQuiet()

	var last string
	for _, t := range targets {
		start, err := a.tailOffset(ctx, t.path, lines)
		if err != nil {
			return err
		}
		t.offset = start
		if err := a.tailPrint(ctx, t, headers, &last); err != nil {
			return err
		}
	}

	if !follow {
		return nil
	}
	return a.followTail(ctx, targets, headers, &last, interval)
}

// tailOffset finds the byte offset where the last n lines begin.
//
// It reads backwards in a widening window rather than reading the file, because
// the point of the command is not to transfer a log to find its end. A window
// that turns out to hold too few line endings is doubled, so the bytes fetched
// come to about twice the final window and never the whole file.
func (a *App) tailOffset(ctx context.Context, p string, n int) (int64, error) {
	info, err := a.client.Stat(ctx, p)
	if err != nil {
		return 0, err
	}
	if n == 0 || info.Size == 0 {
		return info.Size, nil
	}

	for window := int64(tailWindow); ; window *= 2 {
		start := max(info.Size-window, 0)
		buf, err := a.readTailRange(ctx, p, start)
		if err != nil {
			return 0, err
		}

		// Walk back through the line endings. A trailing one ends the last
		// line rather than starting another, so it does not count.
		end := len(buf)
		if end > 0 && buf[end-1] == '\n' {
			end--
		}
		found, at := 0, -1
		for i := end - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			found++
			if found == n {
				at = i
				break
			}
		}
		if at >= 0 {
			return start + int64(at) + 1, nil
		}
		if start == 0 {
			// The whole file holds fewer lines than were asked for.
			return 0, nil
		}
		if window >= maxTailWindow {
			// Far enough back. Begin at the first line ending in the window, so
			// a partial line is not printed as if it were whole.
			if i := bytes.IndexByte(buf, '\n'); i >= 0 {
				return start + int64(i) + 1, nil
			}
			return start, nil
		}
	}
}

func (a *App) readTailRange(ctx context.Context, p string, offset int64) ([]byte, error) {
	body, _, err := a.client.Download(ctx, p, offset)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

// tailPrint writes whatever is after the target's offset, and advances it.
//
// An offset past the end of the file is not an error here. The file can be
// replaced between working out where its last lines start and reading them —
// a log rotated in that window would otherwise kill the command with a 416
// about a range nobody typed — so the answer is to start the new file again
// from the beginning, which is what following it means.
func (a *App) tailPrint(ctx context.Context, t *tailTarget, headers bool, last *string) error {
	body, _, err := a.client.Download(ctx, t.path, t.offset)
	if isRangeBeyondEnd(err) && t.offset > 0 {
		a.out.Warn("%s is shorter than it was; following it from the start", t.path)
		t.offset = 0
		body, _, err = a.client.Download(ctx, t.path, 0)
	}
	if err != nil {
		return err
	}
	defer body.Close()

	// Buffered rather than copied straight through, so that a header is only
	// printed when there turn out to be bytes under it.
	buf, err := io.ReadAll(body)
	if err != nil {
		return cberr.Wrap(cberr.KindOther, "read", t.path, err)
	}
	if len(buf) == 0 {
		return nil
	}
	if headers && *last != t.path {
		prefix := ""
		if *last != "" {
			prefix = "\n"
		}
		fmt.Fprintf(a.stdout, "%s==> %s <==\n", prefix, t.path)
		*last = t.path
	}
	if _, err := a.stdout.Write(buf); err != nil {
		return cberr.Wrap(cberr.KindOther, "write", t.path, err)
	}
	t.offset += int64(len(buf))
	return nil
}

// isRangeBeyondEnd reports whether a download failed because the offset asked
// for is past the end of the file, which is how a file that shrank announces
// itself.
func isRangeBeyondEnd(err error) bool {
	var ce *cberr.Error
	return errors.As(err, &ce) && ce.Status == http.StatusRequestedRangeNotSatisfiable
}

// followTail polls until the context ends.
//
// A context that has ended is how this command finishes — an interrupt, or
// --timeout — so it is not an error. Anything else is.
func (a *App) followTail(ctx context.Context, targets []*tailTarget, headers bool, last *string, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		for _, t := range targets {
			if err := a.pollTail(ctx, t, headers, last); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

// pollTail looks at one file once.
func (a *App) pollTail(ctx context.Context, t *tailTarget, headers bool, last *string) error {
	info, err := a.client.Stat(ctx, t.path)
	if err != nil {
		// A file that has gone is worth saying once and then waiting for: a log
		// being rotated disappears for a moment, and giving up on it is not what
		// somebody watching it wants.
		if cberr.KindOf(err) == cberr.KindNotFound {
			if t.etag != "" {
				a.out.Warn("%s has gone; still watching", t.path)
				t.etag = ""
			}
			return nil
		}
		return err
	}

	switch {
	case info.Size < t.offset, info.Size == t.offset && info.ETag != t.etag:
		// Shorter than what has already been read, or the same length with
		// different content: either way it is not the file that was being
		// followed any more, and continuing from the old offset would print
		// from the middle of a line.
		a.out.Warn("%s was replaced; following the new one", t.path)
		t.offset = 0
	case info.Size == t.offset:
		t.etag = info.ETag
		return nil
	}
	t.etag = info.ETag
	return a.tailPrint(ctx, t, headers, last)
}
