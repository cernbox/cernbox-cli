package client

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// Archive formats the archiver can produce.
const (
	ArchiveTar = "tar"
	ArchiveZip = "zip"
)

// Archive streams a directory tree as a single archive.
//
// This exists because the obvious alternative — PROPFIND the tree, then issue
// one GET per file — costs a round trip per file, which dominates the transfer
// time for a directory of many small files. The archiver does the walk
// server-side and returns one stream. The caller is responsible for falling
// back to a walk when the server has no archiver or the tree exceeds the
// advertised limits.
func (c *Client) Archive(ctx context.Context, paths []string, format string) (io.ReadCloser, error) {
	caps, err := c.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !caps.ArchiverEnabled {
		return nil, cberr.New(cberr.KindOther, "download archive", strings.Join(paths, ", "),
			"this server has no archiver service")
	}
	if !caps.SupportsArchiveFormat(format) {
		return nil, cberr.Usagef("this server cannot produce %q archives, it supports: %s",
			format, strings.Join(caps.ArchiverFormats, ", "))
	}
	if len(paths) == 0 {
		return nil, cberr.Usagef("no paths to archive")
	}

	// The advertised archiver URL may already carry a query of its own, so
	// parse it rather than concatenating: appending to the path would bury the
	// "?" inside the path component and silently drop the server's parameters.
	ref, err := url.Parse(caps.ArchiverURL)
	if err != nil {
		return nil, cberr.Wrap(cberr.KindOther, "download archive", "", err)
	}
	q := ref.Query()
	for _, p := range paths {
		q.Add("path", p)
	}
	q.Set("arch_type", format)
	ref.RawQuery = q.Encode()

	u := c.URL(ref.EscapedPath())
	if ref.RawQuery != "" {
		u += "?" + ref.RawQuery
	}

	resp, err := c.do(ctx, request{
		method:  http.MethodGet,
		url:     u,
		op:      "download archive",
		path:    strings.Join(paths, ", "),
		expects: []int{http.StatusOK},
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ArchiveFits reports whether a tree of the given file count and total size is
// within the archiver's advertised limits. A server that advertises no limit
// always fits.
func (c *Capabilities) ArchiveFits(numFiles int64, totalSize int64) bool {
	if c == nil || !c.ArchiverEnabled {
		return false
	}
	if c.ArchiverMaxNumFiles > 0 && numFiles > c.ArchiverMaxNumFiles {
		return false
	}
	if c.ArchiverMaxSize > 0 && totalSize > c.ArchiverMaxSize {
		return false
	}
	return true
}
