package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TrashItem is one deleted resource.
type TrashItem struct {
	// Key identifies the item for restore and purge. It is opaque: the server
	// encodes the storage and the original location into it.
	Key string `json:"key"`
	// Name is the file name the resource had when it was deleted.
	Name string `json:"name"`
	// OriginalPath is where it lived, relative to the space root.
	OriginalPath string `json:"original_path"`
	// DeletedAt is when it was deleted.
	DeletedAt time.Time `json:"deleted_at"`
	// IsDir reports whether the item is a directory.
	IsDir bool `json:"is_dir"`
	// Size is the byte count, for files.
	Size int64 `json:"size,omitempty"`
}

// trashPropfindBody asks for the trash-specific properties alongside the usual
// ones. The original location is what makes a listing actionable: without it a
// user sees a pile of names with no way to tell which "report.pdf" is which.
const trashPropfindBody = `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop>
    <d:displayname/>
    <d:resourcetype/>
    <d:getcontentlength/>
    <oc:size/>
    <oc:trashbin-original-filename/>
    <oc:trashbin-original-location/>
    <oc:trashbin-delete-timestamp/>
    <oc:trashbin-delete-datetime/>
  </d:prop>
</d:propfind>`

// trashURL builds a trash-bin URL. basePath selects which recycle bin to use;
// empty means the caller's home, which is what the server defaults to.
func (c *Client) trashURL(ctx context.Context, key, basePath string) (string, error) {
	me, err := c.Me(ctx)
	if err != nil {
		return "", err
	}
	u := c.URL(davTrashPrefix + "/" + escapePath(me.Username))
	if key != "" {
		u += "/" + url.PathEscape(key)
	}
	if basePath != "" {
		u += "?base_path=" + url.QueryEscape(basePath)
	}
	return u, nil
}

// ListTrash returns the deleted items in a recycle bin. basePath selects the
// bin; empty means the caller's home.
func (c *Client) ListTrash(ctx context.Context, basePath string) ([]TrashItem, error) {
	u, err := c.trashURL(ctx, "", basePath)
	if err != nil {
		return nil, err
	}

	resp, err := c.do(ctx, request{
		method: MethodPropfind,
		url:    u,
		header: http.Header{
			"Depth":        []string{"1"},
			"Content-Type": []string{"application/xml"},
		},
		body:    stringBody(trashPropfindBody),
		op:      "list the trash bin",
		path:    basePath,
		expects: []int{http.StatusMultiStatus, http.StatusOK},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var ms multistatus
	if err := decodeXML(resp.Body, &ms, "list the trash bin", basePath); err != nil {
		return nil, err
	}

	items := make([]TrashItem, 0, len(ms.Responses))
	for _, entry := range ms.Responses {
		item, ok := trashEntryToItem(entry)
		if !ok {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func trashEntryToItem(entry multistatusEntry) (TrashItem, bool) {
	props, ok := okProps(entry)
	if !ok {
		return TrashItem{}, false
	}

	href, err := url.PathUnescape(entry.Href)
	if err != nil {
		href = entry.Href
	}
	key := trashKeyFromHref(href)
	if key == "" {
		// The bin itself appears in a Depth:1 listing and has no key.
		return TrashItem{}, false
	}

	item := TrashItem{
		Key:          key,
		Name:         firstNonEmptyString(props.TrashFilename, props.DisplayName),
		OriginalPath: props.TrashLocation,
		IsDir:        props.ResourceType != nil && props.ResourceType.Collection != nil,
	}
	if n, err := strconv.ParseInt(props.OCSize, 10, 64); err == nil {
		item.Size = n
	} else if n, err := strconv.ParseInt(props.GetLength, 10, 64); err == nil {
		item.Size = n
	}
	if n, err := strconv.ParseInt(props.TrashTimestamp, 10, 64); err == nil && n > 0 {
		item.DeletedAt = epochToTime(n)
	} else if t, err := http.ParseTime(props.TrashDatetime); err == nil {
		item.DeletedAt = t
	}
	return item, true
}

// trashKeyFromHref extracts the item key, which the server places after the
// username in the href.
func trashKeyFromHref(href string) string {
	i := strings.Index(href, davTrashPrefix)
	if i < 0 {
		return ""
	}
	rest := strings.Trim(href[i+len(davTrashPrefix):], "/")
	_, after, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	return strings.Trim(after, "/")
}

// RestoreTrash puts a deleted item back. An empty dst restores it to where it
// came from.
func (c *Client) RestoreTrash(ctx context.Context, key, dst, basePath string) error {
	src, err := c.trashURL(ctx, key, basePath)
	if err != nil {
		return err
	}

	header := http.Header{}
	if dst != "" {
		dstURL, err := c.davURL(ctx, dst)
		if err != nil {
			return err
		}
		header.Set("Destination", dstURL)
		header.Set("Overwrite", "F")
	}

	resp, err := c.do(ctx, request{
		method:  MethodMove,
		url:     src,
		header:  header,
		op:      "restore from the trash bin",
		path:    key,
		expects: []int{http.StatusCreated, http.StatusNoContent, http.StatusOK},
	})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// PurgeTrash permanently deletes one item, or the whole bin when key is empty.
func (c *Client) PurgeTrash(ctx context.Context, key, basePath string) error {
	u, err := c.trashURL(ctx, key, basePath)
	if err != nil {
		return err
	}

	op := "purge the trash bin"
	if key != "" {
		op = "purge a trash item"
	}

	resp, err := c.do(ctx, request{
		method:  http.MethodDelete,
		url:     u,
		op:      op,
		path:    key,
		expects: []int{http.StatusNoContent, http.StatusOK, http.StatusAccepted},
	})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// okProps returns the properties from the 200 propstat block.
func okProps(entry multistatusEntry) (propsRaw, bool) {
	for _, ps := range entry.Propstat {
		if strings.Contains(ps.Status, " 200 ") {
			return ps.Prop, true
		}
	}
	return propsRaw{}, false
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
