package transfer

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
)

// archiveDownload fetches a whole tree as one tar stream and extracts it.
func (e *Engine) archiveDownload(ctx context.Context, remoteRoot, localRoot string) (*Stats, error) {
	caps, err := e.c.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	// zip needs random access to its central directory, so it cannot be
	// extracted from a stream without buffering the whole archive. tar can.
	if !caps.SupportsArchiveFormat(client.ArchiveTar) {
		return nil, cberr.New(cberr.KindOther, "download archive", remoteRoot,
			"the server's archiver cannot produce a tar stream")
	}

	body, err := e.c.Archive(ctx, []string{remoteRoot}, client.ArchiveTar)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	return e.extractTar(ctx, body, localRoot)
}

// extractTar unpacks a tar stream into localRoot.
func (e *Engine) extractTar(ctx context.Context, r io.Reader, localRoot string) (*Stats, error) {
	stats := &Stats{}

	absRoot, err := filepath.Abs(localRoot)
	if err != nil {
		return nil, cberr.Wrap(cberr.KindOther, "write", localRoot, err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, cberr.Wrap(cberr.KindOther, "create", localRoot, err)
	}

	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return stats, nil
		}
		if err != nil {
			return stats, cberr.Wrap(cberr.KindOther, "read archive", localRoot, err)
		}

		// The archive comes from the server, but a client that trusts entry
		// names is one malformed entry away from writing outside the directory
		// the user asked for. Resolve and check every one.
		target, err := safeJoin(absRoot, hdr.Name)
		if err != nil {
			return stats, err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return stats, cberr.Wrap(cberr.KindOther, "create", target, err)
			}
			stats.Dirs++

		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return stats, cberr.Wrap(cberr.KindOther, "create", filepath.Dir(target), err)
			}
			if !e.opts.Overwrite {
				if _, err := os.Stat(target); err == nil {
					stats.Skipped++
					if _, err := io.Copy(io.Discard, tr); err != nil {
						return stats, cberr.Wrap(cberr.KindOther, "read archive", target, err)
					}
					continue
				}
			}
			n, err := writeArchiveFile(target, tr)
			if err != nil {
				return stats, err
			}
			stats.Files++
			stats.Bytes += n
			e.emit(Event{Path: target, Transferred: n, Total: hdr.Size, Done: true})

		case tar.TypeSymlink, tar.TypeLink:
			// A link in the archive could point anywhere, including outside the
			// destination. Skipping is the conservative choice, and CERNBox
			// does not store symlinks anyway.
			stats.Skipped++

		default:
			stats.Skipped++
		}
	}
}

func writeArchiveFile(target string, r io.Reader) (int64, error) {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, cberr.Wrap(cberr.KindOther, "create", target, err)
	}
	defer f.Close()

	n, err := io.Copy(f, r)
	if err != nil {
		return n, cberr.Wrap(cberr.KindOther, "write", target, err)
	}
	return n, nil
}

// safeJoin resolves name against root and refuses anything that escapes it.
func safeJoin(root, name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) || cleaned == ".." {
		return "", cberr.New(cberr.KindOther, "extract archive", name,
			"the archive contains an entry that would be written outside the destination")
	}

	target := filepath.Join(root, cleaned)
	// Join cleans the result, so a relative escape is already gone; this check
	// catches the remaining cases, such as a name that resolves to the parent
	// through repeated separators.
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", cberr.Wrap(cberr.KindOther, "extract archive", name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", cberr.New(cberr.KindOther, "extract archive", name,
			fmt.Sprintf("the archive entry %q would be written outside the destination", name))
	}
	return target, nil
}
