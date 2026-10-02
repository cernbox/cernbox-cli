package transfer

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
)

// Relay copies a CERNBox file or tree, read through this engine's client, to
// dst as another client sees it. The two clients act as different users, so
// the server cannot copy it itself: a COPY carries one credential. The bytes
// stream through this process instead, one file at a time per job, and never
// touch local disk.
func (e *Engine) Relay(ctx context.Context, src string, to *client.Client, dst string) (*Stats, error) {
	start := time.Now()
	stats := &Stats{}

	info, err := e.c.Stat(ctx, src)
	if err != nil {
		return nil, err
	}
	if !info.IsDir {
		n, err := e.relayFile(ctx, *info, to, dst)
		stats.Duration = time.Since(start)
		switch {
		case errors.Is(err, errSkipped):
			stats.Skipped++
			return stats, nil
		case err != nil:
			return nil, err
		}
		stats.Files, stats.Bytes = 1, n
		return stats, nil
	}

	// Walk visits a directory before its children, so creating directories in
	// the order seen gives every file a parent before it arrives.
	var files []client.ResourceInfo
	err = e.c.Walk(ctx, src, func(ri client.ResourceInfo) error {
		target := path.Join(dst, strings.TrimPrefix(ri.Path, src))
		if !ri.IsDir {
			files = append(files, ri)
			return nil
		}
		stats.Dirs++
		if e.opts.DryRun {
			return nil
		}
		return to.Mkdir(ctx, target, true)
	})
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	err = e.eachParallel(ctx, len(files), func(ctx context.Context, i int) error {
		ri := files[i]
		n, err := e.relayFile(ctx, ri, to, path.Join(dst, strings.TrimPrefix(ri.Path, src)))
		mu.Lock()
		defer mu.Unlock()
		switch {
		case errors.Is(err, errSkipped):
			stats.Skipped++
			return nil
		case err != nil:
			return err
		default:
			stats.Files++
			stats.Bytes += n
			return nil
		}
	})
	stats.Duration = time.Since(start)
	return stats, err
}

// relayFile streams one file from this engine's client to dst through to.
func (e *Engine) relayFile(ctx context.Context, src client.ResourceInfo, to *client.Client, dst string) (int64, error) {
	if !e.opts.Overwrite {
		if _, err := to.Stat(ctx, dst); err == nil {
			e.emit(Event{Path: dst, Total: src.Size, Done: true})
			return 0, errSkipped
		} else if cberr.KindOf(err) != cberr.KindNotFound {
			return 0, err
		}
	}
	if e.opts.DryRun {
		e.emit(Event{Path: dst, Transferred: src.Size, Total: src.Size, Done: true})
		return src.Size, nil
	}

	body, size, err := e.c.Download(ctx, src.Path, 0)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	if err := to.UploadStream(ctx, dst, body, size); err != nil {
		return 0, err
	}

	if e.opts.Verify {
		if err := verifyRelayed(ctx, src, to, dst); err != nil {
			return 0, err
		}
	}
	e.emit(Event{Path: dst, Transferred: size, Total: size, Done: true})
	return size, nil
}

// verifyRelayed compares what arrived with what was sent, using the checksums
// the server keeps for both. Nothing was hashed on the way through, so this is
// the server's word on each side, which is the word that matters.
func verifyRelayed(ctx context.Context, src client.ResourceInfo, to *client.Client, dst string) error {
	got, err := to.Stat(ctx, dst)
	if err != nil {
		return err
	}
	if got.Size != src.Size {
		return cberr.New(cberr.KindOther, "verify", dst,
			fmt.Sprintf("arrived with %d bytes, %d were sent", got.Size, src.Size))
	}
	for alg, want := range src.Checksums {
		if have := got.Checksums[alg]; have != "" && !strings.EqualFold(have, want) {
			return cberr.New(cberr.KindOther, "verify", dst,
				fmt.Sprintf("%s checksum %s does not match the source's %s", alg, have, want))
		}
	}
	return nil
}
