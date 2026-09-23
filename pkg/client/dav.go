package client

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// WebDAV methods not in the net/http constant set.
const (
	MethodPropfind = "PROPFIND"
	MethodMkcol    = "MKCOL"
	MethodMove     = "MOVE"
	MethodCopy     = "COPY"
	MethodReport   = "REPORT"
	MethodLock     = "LOCK"
	MethodUnlock   = "UNLOCK"
)

// ResourceInfo describes one file or directory.
type ResourceInfo struct {
	// Path is the absolute CERNBox path.
	Path string `json:"path"`
	// Name is the final path element.
	Name string `json:"name"`
	// IsDir reports whether this is a collection.
	IsDir bool `json:"is_dir"`
	// Size is the byte count for a file, or the recursive size for a directory
	// when the server reports one.
	Size int64 `json:"size"`
	// Modified is the last modification time.
	Modified time.Time `json:"modified"`
	// ETag identifies this version of the resource.
	ETag string `json:"etag,omitempty"`
	// ID is the persistent resource id, stable across renames.
	ID string `json:"id,omitempty"`
	// MimeType is the content type reported for files.
	MimeType string `json:"mime_type,omitempty"`
	// Permissions is the ownCloud permission string ("RDNVCK" and friends).
	Permissions string `json:"permissions,omitempty"`
	// Checksums holds the server-side checksums, keyed by algorithm.
	Checksums map[string]string `json:"checksums,omitempty"`
	// WebURL is the link that opens this resource in the web interface, when
	// the server reports one.
	WebURL string `json:"web_url,omitempty"`
}

// multistatus is the PROPFIND response envelope.
type multistatus struct {
	XMLName   xml.Name           `xml:"DAV: multistatus"`
	Responses []multistatusEntry `xml:"DAV: response"`
}

type multistatusEntry struct {
	Href     string     `xml:"DAV: href"`
	Propstat []propstat `xml:"DAV: propstat"`
}

type propstat struct {
	Status string   `xml:"DAV: status"`
	Prop   propsRaw `xml:"DAV: prop"`
}

type propsRaw struct {
	DisplayName  string        `xml:"DAV: displayname"`
	ResourceType *resourceType `xml:"DAV: resourcetype"`
	GetLastMod   string        `xml:"DAV: getlastmodified"`
	GetLength    string        `xml:"DAV: getcontentlength"`
	GetType      string        `xml:"DAV: getcontenttype"`
	GetETag      string        `xml:"DAV: getetag"`
	OCID         string        `xml:"http://owncloud.org/ns fileid"`
	OCPerms      string        `xml:"http://owncloud.org/ns permissions"`
	OCSize       string        `xml:"http://owncloud.org/ns size"`
	OCLink       string        `xml:"http://owncloud.org/ns privatelink"`
	OCChecksums  *checksumsRaw `xml:"http://owncloud.org/ns checksums"`

	// Trash-bin properties, present only in a trash listing.
	TrashFilename  string `xml:"http://owncloud.org/ns trashbin-original-filename"`
	TrashLocation  string `xml:"http://owncloud.org/ns trashbin-original-location"`
	TrashTimestamp string `xml:"http://owncloud.org/ns trashbin-delete-timestamp"`
	TrashDatetime  string `xml:"http://owncloud.org/ns trashbin-delete-datetime"`
}

type resourceType struct {
	Collection *struct{} `xml:"DAV: collection"`
}

type checksumsRaw struct {
	Checksum []string `xml:"http://owncloud.org/ns checksum"`
}

// propfindBody requests exactly the properties the CLI uses. Asking for a
// named set rather than allprop keeps the response small on directories with
// many entries, which matters for a listing that may be piped.
const propfindBody = `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop>
    <d:displayname/>
    <d:resourcetype/>
    <d:getlastmodified/>
    <d:getcontentlength/>
    <d:getcontenttype/>
    <d:getetag/>
    <oc:fileid/>
    <oc:permissions/>
    <oc:size/>
    <oc:privatelink/>
    <oc:checksums/>
  </d:prop>
</d:propfind>`

// davURL builds the WebDAV URL for an absolute CERNBox path. Path-addressed
// operations go through /remote.php/dav/files/{user}, which accepts absolute
// CS3 paths directly at CERN.
func (c *Client) davURL(ctx context.Context, p string) (string, error) {
	me, err := c.Me(ctx)
	if err != nil {
		return "", err
	}
	return c.URL(davFilesPrefix + "/" + escapePath(me.Username) + escapePath(p)), nil
}

// escapePath percent-encodes each segment of a path, keeping the separators and
// preserving whether the path was absolute. Callers join the result onto a
// prefix, so adding a leading slash that was not there would produce a double
// separator.
func escapePath(p string) string {
	if p == "" {
		return ""
	}
	leading := strings.HasPrefix(p, "/")
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	out := strings.Join(segs, "/")
	if leading {
		out = "/" + out
	}
	return out
}

// Stat returns metadata for a single resource.
func (c *Client) Stat(ctx context.Context, p string) (*ResourceInfo, error) {
	infos, err := c.propfind(ctx, p, 0, "stat")
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, cberr.New(cberr.KindNotFound, "stat", p, "no such file or directory")
	}
	return &infos[0], nil
}

// List returns the direct children of a directory. The directory itself, which
// PROPFIND Depth:1 includes, is filtered out.
func (c *Client) List(ctx context.Context, p string) ([]ResourceInfo, error) {
	infos, err := c.propfind(ctx, p, 1, "list")
	if err != nil {
		return nil, err
	}
	self := strings.TrimSuffix(cleanPath(p), "/")
	out := make([]ResourceInfo, 0, len(infos))
	for _, info := range infos {
		if strings.TrimSuffix(info.Path, "/") == self {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

func (c *Client) propfind(ctx context.Context, p string, depth int, op string) ([]ResourceInfo, error) {
	u, err := c.davURL(ctx, p)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, request{
		method: MethodPropfind,
		url:    u,
		header: http.Header{
			"Depth":        []string{strconv.Itoa(depth)},
			"Content-Type": []string{"application/xml"},
		},
		body:    stringBody(propfindBody),
		op:      op,
		path:    p,
		expects: []int{http.StatusMultiStatus, http.StatusOK},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return parseMultistatus(resp.Body, op, p)
}

// decodeXML parses an XML response body, reporting a malformed one in terms a
// user can act on rather than as a raw parse error.
func decodeXML(r io.Reader, v any, op, p string) error {
	if err := xml.NewDecoder(r).Decode(v); err != nil {
		return cberr.Wrap(cberr.KindOther, op, p, fmt.Errorf("malformed PROPFIND response: %w", err))
	}
	return nil
}

func parseMultistatus(r io.Reader, op, p string) ([]ResourceInfo, error) {
	var ms multistatus
	if err := decodeXML(r, &ms, op, p); err != nil {
		return nil, err
	}

	out := make([]ResourceInfo, 0, len(ms.Responses))
	for _, entry := range ms.Responses {
		info, ok := entryToInfo(entry)
		if !ok {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

func entryToInfo(entry multistatusEntry) (ResourceInfo, bool) {
	href, err := url.PathUnescape(entry.Href)
	if err != nil {
		href = entry.Href
	}

	// A propstat block carries its own status; the 404 block lists properties
	// the server does not have and must not be merged in.
	props, found := okProps(entry)
	if !found {
		return ResourceInfo{}, false
	}

	// WebDAV hrefs for collections carry a trailing slash. Normalising it away
	// here means every path the rest of the CLI handles has one spelling, which
	// is what keeps the self-entry filter in List and the recursion in Walk
	// from comparing "/root/" against "/root".
	p := strings.TrimSuffix(davHrefToPath(href), "/")
	if p == "" {
		p = "/"
	}
	info := ResourceInfo{
		Path:        p,
		Name:        props.DisplayName,
		IsDir:       props.ResourceType != nil && props.ResourceType.Collection != nil,
		ETag:        strings.Trim(props.GetETag, `"`),
		ID:          props.OCID,
		MimeType:    props.GetType,
		Permissions: props.OCPerms,
		WebURL:      props.OCLink,
	}
	if info.Name == "" {
		info.Name = path.Base(p)
	}
	if t, err := http.ParseTime(props.GetLastMod); err == nil {
		info.Modified = t
	}
	// oc:size is the recursive size on a collection and the plain size on a
	// file; d:getcontentlength is absent on collections. Prefer oc:size so a
	// directory listing shows real sizes.
	if n, err := strconv.ParseInt(props.OCSize, 10, 64); err == nil {
		info.Size = n
	} else if n, err := strconv.ParseInt(props.GetLength, 10, 64); err == nil {
		info.Size = n
	}
	if props.OCChecksums != nil {
		info.Checksums = parseChecksums(props.OCChecksums.Checksum)
	}
	return info, true
}

// parseChecksums turns "SHA1:abc MD5:def" into a map. reva emits them
// space-separated inside a single element, sometimes one per element.
func parseChecksums(raw []string) map[string]string {
	out := map[string]string{}
	for _, chunk := range raw {
		for field := range strings.FieldsSeq(chunk) {
			name, value, ok := strings.Cut(field, ":")
			if !ok || value == "" {
				continue
			}
			out[strings.ToLower(name)] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// davHrefToPath strips the WebDAV prefix from an href, leaving the absolute
// CERNBox path. The href is server-absolute and shaped like
// /remote.php/dav/files/{user}/eos/user/g/gdelmont/x.
func davHrefToPath(href string) string {
	if i := strings.Index(href, davFilesPrefix); i >= 0 {
		rest := href[i+len(davFilesPrefix):]
		rest = strings.TrimPrefix(rest, "/")
		// Drop the {user} segment.
		if _, after, ok := strings.Cut(rest, "/"); ok {
			return cleanPath("/" + after)
		}
		return "/"
	}
	if i := strings.Index(href, davSpacesPrefix); i >= 0 {
		rest := strings.TrimPrefix(href[i+len(davSpacesPrefix):], "/")
		if _, after, ok := strings.Cut(rest, "/"); ok {
			return cleanPath("/" + after)
		}
		return "/"
	}
	return cleanPath(href)
}

func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	trailing := strings.HasSuffix(p, "/") && len(p) > 1
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	if trailing && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

// Download opens a file for reading. The caller must close the returned
// ReadCloser. offset skips the first offset bytes, which is how an interrupted
// download resumes.
func (c *Client) Download(ctx context.Context, p string, offset int64) (io.ReadCloser, int64, error) {
	u, err := c.davURL(ctx, p)
	if err != nil {
		return nil, 0, err
	}
	header := http.Header{}
	expects := []int{http.StatusOK}
	if offset > 0 {
		header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		expects = append(expects, http.StatusPartialContent)
	}

	resp, err := c.do(ctx, request{
		method:  http.MethodGet,
		url:     u,
		header:  header,
		op:      "download",
		path:    p,
		expects: expects,
	})
	if err != nil {
		return nil, 0, err
	}
	// A server that ignores Range and replies 200 would silently duplicate the
	// prefix the caller already has, so refuse rather than corrupt the file.
	if offset > 0 && resp.StatusCode == http.StatusOK {
		drain(resp)
		return nil, 0, cberr.New(cberr.KindOther, "resume download", p,
			"the server ignored the requested byte range")
	}
	return resp.Body, resp.ContentLength, nil
}

// Upload writes a file in a single PUT. Large files should go through
// UploadResumable instead.
func (c *Client) Upload(ctx context.Context, p string, open func() (io.ReadCloser, error), size int64, checksum string) error {
	return c.put(ctx, p, open, size, checksum, "")
}

// UploadIfUnchanged writes a file only while its current ETag is still etag,
// failing with KindConflict otherwise.
//
// This is how two clients writing the same small file detect that they raced,
// rather than one silently overwriting the other. reva does honour If-Match on
// PUT — unlike If-None-Match, which it ignores, so there is no equivalent way to
// make *creating* a file exclusive.
func (c *Client) UploadIfUnchanged(ctx context.Context, p string, open func() (io.ReadCloser, error), size int64, etag string) error {
	return c.put(ctx, p, open, size, "", etag)
}

func (c *Client) put(ctx context.Context, p string, open func() (io.ReadCloser, error), size int64, checksum, ifMatch string) error {
	u, err := c.davURL(ctx, p)
	if err != nil {
		return err
	}
	header := http.Header{"Content-Type": []string{"application/octet-stream"}}
	if checksum != "" {
		header.Set("OC-Checksum", checksum)
	}
	if ifMatch != "" {
		// The ETag travels quoted, as PROPFIND reports it and as RFC 9110 wants
		// it; the parsed form the rest of the CLI carries has the quotes stripped.
		header.Set("If-Match", `"`+strings.Trim(ifMatch, `"`)+`"`)
	}

	resp, err := c.do(ctx, request{
		method:  http.MethodPut,
		url:     u,
		header:  header,
		body:    readerBody(open, size),
		op:      "upload",
		path:    p,
		expects: []int{http.StatusOK, http.StatusCreated, http.StatusNoContent},
	})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// Mkdir creates a directory. With parents, missing ancestors are created too.
func (c *Client) Mkdir(ctx context.Context, p string, parents bool) error {
	if parents {
		return c.mkdirAll(ctx, p)
	}
	return c.mkcol(ctx, p)
}

func (c *Client) mkcol(ctx context.Context, p string) error {
	u, err := c.davURL(ctx, p)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, request{
		method:  MethodMkcol,
		url:     u,
		op:      "create directory",
		path:    p,
		expects: []int{http.StatusCreated, http.StatusOK},
	})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// mkdirAll creates a directory and any missing ancestor.
//
// It works downwards from the target rather than upwards from the root: it
// tries to create the directory, and only if that fails for want of a parent
// does it go up a level. Walking from the root instead would mean creating
// every prefix of the path, and the leading segments of a CERNBox path are not
// real directories — "/eos" is the namespace root, which cannot be created and
// cannot be stat'ed, so "mkdir -p /eos/user/g/gdelmont/x" failed at the very
// first step even though everything above the leaf already existed.
//
// "Already exists" is tolerated at every level, which also makes this safe to
// run concurrently with another client creating the same tree.
func (c *Client) mkdirAll(ctx context.Context, p string) error {
	p = cleanPath(p)
	if p == "/" {
		return nil
	}

	err := c.mkcol(ctx, p)
	if err == nil {
		return nil
	}
	if c.isExistingDir(ctx, p) {
		return nil
	}

	// The parent may simply not be there yet. Create it, then try once more;
	// any other failure is the caller's to see.
	parent := cleanPath(path.Dir(p))
	if parent == p || parent == "/" {
		return err
	}
	if parentErr := c.mkdirAll(ctx, parent); parentErr != nil {
		return parentErr
	}
	if err := c.mkcol(ctx, p); err != nil {
		if c.isExistingDir(ctx, p) {
			return nil
		}
		return err
	}
	return nil
}

// isExistingDir reports whether p is already a directory, which is the one case
// where failing to create it is not a failure at all.
func (c *Client) isExistingDir(ctx context.Context, p string) bool {
	info, err := c.Stat(ctx, p)
	return err == nil && info.IsDir
}

// Remove deletes a file or directory. WebDAV DELETE on a collection is always
// recursive, so the caller is responsible for refusing to delete a non-empty
// directory without -r.
func (c *Client) Remove(ctx context.Context, p string) error {
	u, err := c.davURL(ctx, p)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, request{
		method:  http.MethodDelete,
		url:     u,
		op:      "remove",
		path:    p,
		expects: []int{http.StatusNoContent, http.StatusOK, http.StatusAccepted},
	})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// Move renames or moves a resource server-side.
func (c *Client) Move(ctx context.Context, src, dst string, overwrite bool) error {
	return c.moveOrCopy(ctx, MethodMove, "move", src, dst, overwrite)
}

// Copy duplicates a resource server-side, without moving the bytes through the
// client.
func (c *Client) Copy(ctx context.Context, src, dst string, overwrite bool) error {
	return c.moveOrCopy(ctx, MethodCopy, "copy", src, dst, overwrite)
}

func (c *Client) moveOrCopy(ctx context.Context, method, op, src, dst string, overwrite bool) error {
	srcURL, err := c.davURL(ctx, src)
	if err != nil {
		return err
	}
	dstURL, err := c.davURL(ctx, dst)
	if err != nil {
		return err
	}
	header := http.Header{"Destination": []string{dstURL}}
	if overwrite {
		header.Set("Overwrite", "T")
	} else {
		header.Set("Overwrite", "F")
	}

	resp, err := c.do(ctx, request{
		method:  method,
		url:     srcURL,
		header:  header,
		op:      op,
		path:    src,
		expects: []int{http.StatusCreated, http.StatusNoContent, http.StatusOK},
	})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// Touch creates an empty file, failing if it already exists.
func (c *Client) Touch(ctx context.Context, p string) error {
	// Look before writing. The request below carries If-None-Match: *, which
	// should make the server refuse to overwrite, but reva's WebDAV does not
	// implement that precondition — it accepts the PUT and truncates the file.
	// Relying on it silently destroyed content, so existence is checked here
	// instead. The header stays, so this becomes atomic if reva ever honours it.
	if info, statErr := c.Stat(ctx, p); statErr == nil {
		if info.IsDir {
			return cberr.New(cberr.KindConflict, "create file", p, "is a directory")
		}
		return cberr.New(cberr.KindConflict, "create file", p, "already exists")
	}

	u, err := c.davURL(ctx, p)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, request{
		method:  http.MethodPut,
		url:     u,
		header:  http.Header{"If-None-Match": []string{"*"}},
		body:    stringBody(""),
		op:      "create file",
		path:    p,
		expects: []int{http.StatusCreated, http.StatusOK, http.StatusNoContent},
	})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// Walk visits p and everything beneath it, depth first, calling fn for each
// resource. A directory is visited before its children.
func (c *Client) Walk(ctx context.Context, p string, fn func(ResourceInfo) error) error {
	info, err := c.Stat(ctx, p)
	if err != nil {
		return err
	}
	return c.walk(ctx, *info, fn)
}

func (c *Client) walk(ctx context.Context, info ResourceInfo, fn func(ResourceInfo) error) error {
	if err := fn(info); err != nil {
		return err
	}
	if !info.IsDir {
		return nil
	}
	children, err := c.List(ctx, info.Path)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.walk(ctx, child, fn); err != nil {
			return err
		}
	}
	return nil
}

// SearchOptions constrains a REPORT search.
type SearchOptions struct {
	// Pattern is matched against file names. The server treats it as a
	// substring match.
	Pattern string
	// Limit caps the number of results; zero means the server default.
	Limit int
}

const searchReportBody = `<?xml version="1.0" encoding="UTF-8"?>
<oc:search-files xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop>
    <d:displayname/>
    <d:resourcetype/>
    <d:getlastmodified/>
    <d:getcontentlength/>
    <d:getcontenttype/>
    <d:getetag/>
    <oc:fileid/>
    <oc:permissions/>
    <oc:size/>
  </d:prop>
  <oc:search>
    <oc:pattern>%s</oc:pattern>
    <oc:limit>%d</oc:limit>
  </oc:search>
</oc:search-files>`

// ErrSearchUnsupported reports that the server cannot search, so the caller
// should walk the tree instead.
var ErrSearchUnsupported = errors.New("the server does not support search")

// Search runs a server-side search rooted at p.
//
// reva's search-files REPORT handler is a stub that answers 501, so this
// returns ErrSearchUnsupported there and the caller falls back to walking. The
// request is still worth making: a deployment that implements it answers in one
// round trip instead of one per directory.
func (c *Client) Search(ctx context.Context, p string, opts SearchOptions) ([]ResourceInfo, error) {
	u, err := c.davURL(ctx, p)
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	body := fmt.Sprintf(searchReportBody, xmlEscape(opts.Pattern), limit)

	resp, err := c.do(ctx, request{
		method:  MethodReport,
		url:     u,
		header:  http.Header{"Content-Type": []string{"application/xml"}},
		body:    stringBody(body),
		op:      "search",
		path:    p,
		expects: []int{http.StatusMultiStatus, http.StatusOK},
	})
	if err != nil {
		var ce *cberr.Error
		if errors.As(err, &ce) && ce.Status == http.StatusNotImplemented {
			return nil, ErrSearchUnsupported
		}
		return nil, err
	}
	defer resp.Body.Close()

	return parseMultistatus(resp.Body, "search", p)
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
