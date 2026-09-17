// Package transfer moves data between the local filesystem and CERNBox.
//
// It exists to make three things routine rather than exceptional: an
// interrupted transfer resumes instead of restarting, many small files go in
// parallel instead of one round trip at a time, and what arrives is verified
// against what was sent.
package transfer

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/adler32"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
)

// defaultChunkSize is the upload chunk used when the server advertises no
// limit. Large enough that per-chunk overhead is negligible, small enough that
// an interrupted transfer loses little.
const defaultChunkSize = 8 << 20

// defaultPutThreshold is the size below which a plain PUT beats TUS: setting up
// a resumable upload costs two extra round trips, which dominates for a small
// file.
const defaultPutThreshold = 4 << 20

// Event reports transfer progress.
type Event struct {
	// Path is the resource being transferred, as the user named it.
	Path string
	// Transferred is how many bytes have moved so far.
	Transferred int64
	// Total is the size of the resource, or -1 when unknown.
	Total int64
	// Done reports whether this resource has finished.
	Done bool
	// Err is set when this resource failed.
	Err error
}

// ProgressFunc receives progress events. It may be called from several
// goroutines at once, so implementations must be safe for concurrent use.
type ProgressFunc func(Event)

// Options configures an Engine.
type Options struct {
	// Jobs is the number of resources transferred concurrently.
	Jobs int
	// ChunkSize is the preferred upload chunk, clamped to what the server
	// advertises.
	ChunkSize int64
	// PutThreshold is the size at or above which an upload becomes resumable.
	// Below it a plain PUT is faster, because setting up a TUS upload costs two
	// extra round trips.
	PutThreshold int64
	// DryRun reports what would happen without transferring anything.
	DryRun bool
	// Overwrite allows replacing an existing destination. Without it, an
	// existing destination is skipped rather than clobbered.
	Overwrite bool
	// Verify computes a checksum and asks the server to verify it.
	Verify bool
	// Archive allows a recursive download to be served by the archiver as a
	// single stream, instead of one request per file.
	Archive bool
	// StateDir holds resumable-upload state between invocations.
	StateDir string
	// Progress receives events.
	Progress ProgressFunc
}

func (o *Options) applyDefaults() {
	if o.Jobs <= 0 {
		o.Jobs = min(8, max(2, runtime.NumCPU()))
	}
	if o.ChunkSize <= 0 {
		o.ChunkSize = defaultChunkSize
	}
	if o.PutThreshold <= 0 {
		o.PutThreshold = defaultPutThreshold
	}
	if o.StateDir == "" {
		o.StateDir = DefaultStateDir()
	}
}

// DefaultStateDir returns where resumable-upload state is kept.
func DefaultStateDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "cernbox", "uploads")
	}
	return filepath.Join(os.TempDir(), "cernbox-uploads")
}

// Stats summarises a transfer.
type Stats struct {
	Files    int           `json:"files"`
	Dirs     int           `json:"dirs"`
	Bytes    int64         `json:"bytes"`
	Skipped  int           `json:"skipped"`
	Duration time.Duration `json:"duration_ns"`
}

// Engine performs transfers.
type Engine struct {
	c    *client.Client
	opts Options
}

// New returns an Engine using the given client.
func New(c *client.Client, opts Options) *Engine {
	opts.applyDefaults()
	return &Engine{c: c, opts: opts}
}

// Options returns the effective options, after defaults.
func (e *Engine) Options() Options { return e.opts }

func (e *Engine) emit(ev Event) {
	if e.opts.Progress != nil {
		e.opts.Progress(ev)
	}
}

// ── upload ───────────────────────────────────────────────────────────────────

// UploadFile sends one local file to a CERNBox path.
func (e *Engine) UploadFile(ctx context.Context, localPath, remotePath string) (int64, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return 0, cberr.Wrap(cberr.KindNotFound, "read", localPath, err)
	}
	if info.IsDir() {
		return 0, cberr.Usagef("%s is a directory: pass -r to copy it recursively", localPath)
	}

	if !e.opts.Overwrite {
		if _, statErr := e.c.Stat(ctx, remotePath); statErr == nil {
			e.emit(Event{Path: remotePath, Total: info.Size(), Done: true})
			return 0, errSkipped
		} else if cberr.KindOf(statErr) != cberr.KindNotFound {
			return 0, statErr
		}
	}

	if e.opts.DryRun {
		e.emit(Event{Path: remotePath, Transferred: info.Size(), Total: info.Size(), Done: true})
		return info.Size(), nil
	}

	checksum := ""
	if e.opts.Verify {
		checksum, err = e.fileChecksum(ctx, localPath)
		if err != nil {
			return 0, err
		}
	}

	caps, err := e.c.Capabilities(ctx)
	if err != nil {
		return 0, err
	}

	if !caps.TusSupported || info.Size() < e.opts.PutThreshold {
		if err := e.simpleUpload(ctx, localPath, remotePath, info.Size(), checksum); err != nil {
			return 0, err
		}
		e.emit(Event{Path: remotePath, Transferred: info.Size(), Total: info.Size(), Done: true})
		return info.Size(), nil
	}

	return e.resumableUpload(ctx, localPath, remotePath, info, checksum, caps)
}

// errSkipped signals that a resource was intentionally not transferred. It is
// handled inside the engine and never reaches a caller.
var errSkipped = errors.New("skipped")

func (e *Engine) simpleUpload(ctx context.Context, localPath, remotePath string, size int64, checksum string) error {
	body := func() (io.ReadCloser, error) { return os.Open(localPath) }
	return e.c.Upload(ctx, remotePath, body, size, checksum)
}

// resumableUpload transfers a large file in chunks, continuing a previous
// attempt when one is recorded.
func (e *Engine) resumableUpload(ctx context.Context, localPath, remotePath string, info os.FileInfo, checksum string, caps *client.Capabilities) (int64, error) {
	chunk := caps.ChunkSize(e.opts.ChunkSize)

	up, resumed, err := e.resumeOrCreate(ctx, localPath, remotePath, info, checksum)
	if err != nil {
		return 0, err
	}

	offset := up.Offset
	if resumed {
		// The server's offset is the only trustworthy one: our recorded
		// progress may be ahead of what it actually committed.
		offset, err = e.c.UploadOffset(ctx, up)
		if err != nil {
			// The upload the server knew about is gone, so start a new one
			// rather than failing a command the user can only fix by deleting
			// a state file they do not know exists.
			e.clearState(localPath, remotePath)
			up, _, err = e.resumeOrCreate(ctx, localPath, remotePath, info, checksum)
			if err != nil {
				return 0, err
			}
			offset = 0
		}
	}

	f, err := os.Open(localPath)
	if err != nil {
		return 0, cberr.Wrap(cberr.KindOther, "read", localPath, err)
	}
	defer f.Close()

	start := offset
	for offset < info.Size() {
		if err := ctx.Err(); err != nil {
			e.saveState(localPath, remotePath, up, offset)
			return offset - start, err
		}

		length := min(chunk, info.Size()-offset)
		at := offset
		body := func() (io.ReadCloser, error) {
			return io.NopCloser(io.NewSectionReader(f, at, length)), nil
		}

		newOffset, err := e.c.UploadChunk(ctx, up, offset, body, length)
		if err != nil {
			e.saveState(localPath, remotePath, up, offset)
			return offset - start, err
		}
		if newOffset <= offset {
			e.saveState(localPath, remotePath, up, offset)
			return offset - start, cberr.New(cberr.KindOther, "upload", remotePath,
				"the server stopped accepting data")
		}
		offset = newOffset
		e.emit(Event{Path: remotePath, Transferred: offset, Total: info.Size()})
	}

	e.clearState(localPath, remotePath)
	e.emit(Event{Path: remotePath, Transferred: offset, Total: info.Size(), Done: true})
	return offset - start, nil
}

func (e *Engine) resumeOrCreate(ctx context.Context, localPath, remotePath string, info os.FileInfo, checksum string) (*client.Upload, bool, error) {
	if up, ok := e.loadState(localPath, remotePath, info); ok {
		return up, true, nil
	}
	up, err := e.c.CreateUpload(ctx, remotePath, info.Size(), checksum)
	if err != nil {
		return nil, false, err
	}
	return up, false, nil
}

// UploadTree copies a local directory tree to CERNBox.
func (e *Engine) UploadTree(ctx context.Context, localRoot, remoteRoot string) (*Stats, error) {
	start := time.Now()
	stats := &Stats{}

	info, err := os.Stat(localRoot)
	if err != nil {
		return nil, cberr.Wrap(cberr.KindNotFound, "read", localRoot, err)
	}
	if !info.IsDir() {
		n, err := e.UploadFile(ctx, localRoot, remoteRoot)
		if errors.Is(err, errSkipped) {
			stats.Skipped++
			stats.Duration = time.Since(start)
			return stats, nil
		}
		if err != nil {
			return nil, err
		}
		stats.Files, stats.Bytes, stats.Duration = 1, n, time.Since(start)
		return stats, nil
	}

	dirs, files, err := walkLocal(localRoot)
	if err != nil {
		return nil, err
	}

	// Directories first, shallowest to deepest, so every file has a parent by
	// the time it is uploaded.
	for _, rel := range dirs {
		remote := path.Join(remoteRoot, filepath.ToSlash(rel))
		if e.opts.DryRun {
			stats.Dirs++
			continue
		}
		if err := e.c.Mkdir(ctx, remote, true); err != nil {
			return nil, err
		}
		stats.Dirs++
	}
	if e.opts.DryRun {
		// A dry run still needs the root itself counted.
		stats.Dirs++
	} else if err := e.c.Mkdir(ctx, remoteRoot, true); err != nil {
		return nil, err
	}

	var mu sync.Mutex
	err = e.eachParallel(ctx, len(files), func(ctx context.Context, i int) error {
		rel := files[i]
		local := filepath.Join(localRoot, rel)
		remote := path.Join(remoteRoot, filepath.ToSlash(rel))

		n, err := e.UploadFile(ctx, local, remote)
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
	if err != nil {
		return stats, err
	}
	return stats, nil
}

// ── download ─────────────────────────────────────────────────────────────────

// DownloadFile fetches one CERNBox file to a local path, resuming a partial
// transfer when one is present.
func (e *Engine) DownloadFile(ctx context.Context, remotePath, localPath string) (int64, error) {
	info, err := e.c.Stat(ctx, remotePath)
	if err != nil {
		return 0, err
	}
	if info.IsDir {
		return 0, cberr.Usagef("%s is a directory: pass -r to copy it recursively", remotePath)
	}

	if !e.opts.Overwrite {
		if _, err := os.Stat(localPath); err == nil {
			e.emit(Event{Path: localPath, Total: info.Size, Done: true})
			return 0, errSkipped
		}
	}
	if e.opts.DryRun {
		e.emit(Event{Path: localPath, Transferred: info.Size, Total: info.Size, Done: true})
		return info.Size, nil
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return 0, cberr.Wrap(cberr.KindOther, "create", filepath.Dir(localPath), err)
	}

	// Download into a sibling .part file. A consumer watching the destination
	// never sees a truncated file, and an interrupted transfer leaves something
	// to resume from.
	partPath := localPath + ".part"
	offset := int64(0)
	if st, err := os.Stat(partPath); err == nil && st.Size() < info.Size {
		offset = st.Size()
	}

	body, _, err := e.c.Download(ctx, remotePath, offset)
	if err != nil {
		return 0, err
	}
	defer body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return 0, cberr.Wrap(cberr.KindOther, "create", partPath, err)
	}

	written, err := e.copyWithProgress(ctx, f, body, localPath, offset, info.Size)
	closeErr := f.Close()
	if err != nil {
		return written, err
	}
	if closeErr != nil {
		return written, cberr.Wrap(cberr.KindOther, "write", partPath, closeErr)
	}

	if err := os.Rename(partPath, localPath); err != nil {
		return written, cberr.Wrap(cberr.KindOther, "write", localPath, err)
	}

	if e.opts.Verify && len(info.Checksums) > 0 {
		if err := verifyLocalChecksum(localPath, info.Checksums); err != nil {
			return written, err
		}
	}

	e.emit(Event{Path: localPath, Transferred: offset + written, Total: info.Size, Done: true})
	return written, nil
}

// DownloadTree fetches a CERNBox directory tree.
//
// When the server has an archiver and the tree fits its limits, the whole tree
// comes down as one stream. The alternative — PROPFIND the tree, then one GET
// per file — costs a round trip per file, which is what dominates the time for
// a directory of many small files.
func (e *Engine) DownloadTree(ctx context.Context, remoteRoot, localRoot string) (*Stats, error) {
	start := time.Now()

	info, err := e.c.Stat(ctx, remoteRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir {
		stats := &Stats{}
		n, err := e.DownloadFile(ctx, remoteRoot, localRoot)
		if errors.Is(err, errSkipped) {
			stats.Skipped++
			stats.Duration = time.Since(start)
			return stats, nil
		}
		if err != nil {
			return nil, err
		}
		stats.Files, stats.Bytes, stats.Duration = 1, n, time.Since(start)
		return stats, nil
	}

	if e.opts.Archive && !e.opts.DryRun {
		stats, err := e.archiveDownload(ctx, remoteRoot, localRoot)
		if err == nil {
			stats.Duration = time.Since(start)
			return stats, nil
		}
		// Falling back is the right move: the archiver may be absent, or the
		// tree may exceed its limits, and neither should fail the command.
		e.emit(Event{Path: remoteRoot, Err: err})
	}

	return e.walkDownload(ctx, remoteRoot, localRoot, start)
}

func (e *Engine) walkDownload(ctx context.Context, remoteRoot, localRoot string, start time.Time) (*Stats, error) {
	stats := &Stats{}

	type job struct{ remote, local string }
	var jobs []job

	err := e.c.Walk(ctx, remoteRoot, func(info client.ResourceInfo) error {
		rel := strings.TrimPrefix(info.Path, remoteRoot)
		rel = strings.TrimPrefix(rel, "/")
		local := localRoot
		if rel != "" {
			local = filepath.Join(localRoot, filepath.FromSlash(rel))
		}
		if info.IsDir {
			stats.Dirs++
			if e.opts.DryRun {
				return nil
			}
			return os.MkdirAll(local, 0o755)
		}
		jobs = append(jobs, job{remote: info.Path, local: local})
		return nil
	})
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	err = e.eachParallel(ctx, len(jobs), func(ctx context.Context, i int) error {
		n, err := e.DownloadFile(ctx, jobs[i].remote, jobs[i].local)
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

// ── helpers ──────────────────────────────────────────────────────────────────

// eachParallel runs fn for each index with at most Jobs in flight, stopping
// early on the first error.
func (e *Engine) eachParallel(ctx context.Context, n int, fn func(context.Context, int) error) error {
	if n == 0 {
		return nil
	}
	workers := min(e.opts.Jobs, n)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	indices := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indices {
				if err := fn(ctx, i); err != nil {
					once.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
			}
		}()
	}

send:
	for i := range n {
		select {
		case indices <- i:
		case <-ctx.Done():
			break send
		}
	}
	close(indices)
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func (e *Engine) copyWithProgress(ctx context.Context, dst io.Writer, src io.Reader, name string, offset, total int64) (int64, error) {
	buf := make([]byte, 256<<10)
	var written int64
	lastEmit := time.Now()

	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return written, cberr.Wrap(cberr.KindOther, "write", name, err)
			}
			written += int64(n)
			// Rate-limit events: a progress bar redrawn per 256 KiB read is
			// wasted work on a fast link.
			if time.Since(lastEmit) > 100*time.Millisecond {
				e.emit(Event{Path: name, Transferred: offset + written, Total: total})
				lastEmit = time.Now()
			}
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, cberr.Wrap(cberr.KindOther, "download", name, readErr)
		}
	}
}

// walkLocal returns directories shallowest-first and files, both relative to
// root.
func walkLocal(root string) (dirs, files []string, err error) {
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		// Symlinks are not followed: copying the target under the link's name
		// silently changes the shape of what the user asked to transfer.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, nil, cberr.Wrap(cberr.KindOther, "read", root, err)
	}
	sort.Slice(dirs, func(i, j int) bool {
		di, dj := strings.Count(dirs[i], string(os.PathSeparator)), strings.Count(dirs[j], string(os.PathSeparator))
		if di != dj {
			return di < dj
		}
		return dirs[i] < dirs[j]
	})
	sort.Strings(files)
	return dirs, files, nil
}

// fileChecksum computes a checksum in the algorithm the server prefers,
// formatted the way OC-Checksum expects.
func (e *Engine) fileChecksum(ctx context.Context, localPath string) (string, error) {
	caps, err := e.c.Capabilities(ctx)
	if err != nil {
		return "", err
	}
	algo := caps.BestChecksum()
	if algo == "" {
		return "", nil
	}
	sum, err := checksumFile(localPath, algo)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(algo) + ":" + sum, nil
}

func newHash(algo string) (hash.Hash, error) {
	switch strings.ToLower(algo) {
	case "sha1":
		return sha1.New(), nil
	case "md5":
		return md5.New(), nil
	case "adler32":
		return adler32.New(), nil
	default:
		return nil, fmt.Errorf("unsupported checksum algorithm %q", algo)
	}
}

func checksumFile(localPath, algo string) (string, error) {
	h, err := newHash(algo)
	if err != nil {
		return "", err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return "", cberr.Wrap(cberr.KindOther, "read", localPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return "", cberr.Wrap(cberr.KindOther, "read", localPath, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyLocalChecksum checks a downloaded file against the server's checksums.
// Only algorithms both sides know are compared; a server that reports none
// leaves the file unverified rather than failing.
func verifyLocalChecksum(localPath string, serverSums map[string]string) error {
	for _, algo := range []string{"sha1", "md5", "adler32"} {
		want, ok := serverSums[algo]
		if !ok || want == "" {
			continue
		}
		got, err := checksumFile(localPath, algo)
		if err != nil {
			return err
		}
		if !strings.EqualFold(got, want) {
			return cberr.New(cberr.KindOther, "verify", localPath,
				fmt.Sprintf("checksum mismatch: the server reported %s %s but the downloaded file is %s", algo, want, got))
		}
		return nil
	}
	return nil
}
