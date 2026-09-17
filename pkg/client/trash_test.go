package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// trashMultistatus renders a trash listing the way reva does: the bin itself
// first, then one entry per deleted item, each keyed in its href.
func trashMultistatus(user string, items ...trashFixture) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	sb.WriteString(`<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)

	// The bin itself, which a Depth:1 listing always includes.
	sb.WriteString(`<d:response><d:href>` + davTrashPrefix + "/" + user + `/</d:href>`)
	sb.WriteString(`<d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop>`)
	sb.WriteString(`<d:resourcetype><d:collection/></d:resourcetype>`)
	sb.WriteString(`</d:prop></d:propstat></d:response>`)

	for _, it := range items {
		href := davTrashPrefix + "/" + user + "/" + url.PathEscape(it.Key)
		if it.IsDir {
			href += "/"
		}
		sb.WriteString(`<d:response><d:href>` + href + `</d:href>`)
		sb.WriteString(`<d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop>`)
		sb.WriteString(`<d:displayname>` + escapeXML(it.Name) + `</d:displayname>`)
		if it.IsDir {
			sb.WriteString(`<d:resourcetype><d:collection/></d:resourcetype>`)
		} else {
			sb.WriteString(`<d:resourcetype></d:resourcetype>`)
			fmt.Fprintf(&sb, "<d:getcontentlength>%d</d:getcontentlength>", it.Size)
		}
		fmt.Fprintf(&sb, "<oc:size>%d</oc:size>", it.Size)
		sb.WriteString(`<oc:trashbin-original-filename>` + escapeXML(it.Name) + `</oc:trashbin-original-filename>`)
		sb.WriteString(`<oc:trashbin-original-location>` + escapeXML(it.Location) + `</oc:trashbin-original-location>`)
		fmt.Fprintf(&sb, "<oc:trashbin-delete-timestamp>%d</oc:trashbin-delete-timestamp>", it.Deleted)
		sb.WriteString(`</d:prop></d:propstat></d:response>`)
	}
	sb.WriteString(`</d:multistatus>`)
	return sb.String()
}

type trashFixture struct {
	Key      string
	Name     string
	Location string
	Size     int64
	Deleted  int64
	IsDir    bool
}

func TestListTrash(t *testing.T) {
	f := newFakeServer(t)
	f.on(MethodPropfind, davTrashPrefix, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, trashMultistatus("einstein",
			trashFixture{Key: "key-1", Name: "notes.txt", Location: "Documents/notes.txt", Size: 120, Deleted: 1767225600},
			trashFixture{Key: "key-2", Name: "old", Location: "Documents/old", Size: 4096, Deleted: 1767139200, IsDir: true},
		))
	})

	items, err := f.client().ListTrash(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	// The bin itself must not appear as a restorable item.
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (the bin itself must be excluded): %+v", len(items), items)
	}

	first := items[0]
	if first.Key != "key-1" {
		t.Errorf("Key = %q", first.Key)
	}
	if first.Name != "notes.txt" {
		t.Errorf("Name = %q", first.Name)
	}
	// The original location is what lets a user tell two deleted "notes.txt"
	// apart, so it must survive the round trip.
	if first.OriginalPath != "Documents/notes.txt" {
		t.Errorf("OriginalPath = %q", first.OriginalPath)
	}
	if first.Size != 120 {
		t.Errorf("Size = %d", first.Size)
	}
	if !first.DeletedAt.Equal(time.Unix(1767225600, 0)) {
		t.Errorf("DeletedAt = %v", first.DeletedAt)
	}
	if first.IsDir {
		t.Error("a file was reported as a directory")
	}
	if !items[1].IsDir {
		t.Error("a directory was reported as a file")
	}
}

func TestListTrashWithSpace(t *testing.T) {
	f := newFakeServer(t)
	var gotQuery string
	f.on(MethodPropfind, davTrashPrefix, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, trashMultistatus("einstein"))
	})

	if _, err := f.client().ListTrash(context.Background(), "/eos/project/c/cernbox"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "base_path=") {
		t.Errorf("query = %q, want the bin selected with base_path", gotQuery)
	}
	if decoded, _ := url.QueryUnescape(gotQuery); !strings.Contains(decoded, "/eos/project/c/cernbox") {
		t.Errorf("query = %q, want the space path", decoded)
	}
}

func TestTrashKeyFromHref(t *testing.T) {
	tests := []struct {
		href string
		want string
	}{
		{davTrashPrefix + "/einstein/key-1", "key-1"},
		{davTrashPrefix + "/einstein/key-2/", "key-2"},
		{davTrashPrefix + "/einstein/", ""},
		{davTrashPrefix + "/einstein", ""},
		{"/somewhere/else", ""},
	}
	for _, tt := range tests {
		if got := trashKeyFromHref(tt.href); got != tt.want {
			t.Errorf("trashKeyFromHref(%q) = %q, want %q", tt.href, got, tt.want)
		}
	}
}

func TestRestoreTrashToOriginalLocation(t *testing.T) {
	f := newFakeServer(t)
	f.on(MethodMove, davTrashPrefix, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	if err := f.client().RestoreTrash(context.Background(), "key-1", "", ""); err != nil {
		t.Fatal(err)
	}

	req := f.lastRequest(MethodMove)
	// With no destination the server restores to the original location, so
	// sending one would override the user's intent.
	if got := req.Header.Get("Destination"); got != "" {
		t.Errorf("Destination = %q, want none for a restore in place", got)
	}
	if !strings.HasSuffix(req.Path, "/key-1") {
		t.Errorf("request path = %q", req.Path)
	}
}

func TestRestoreTrashToExplicitDestination(t *testing.T) {
	f := newFakeServer(t)
	f.on(MethodMove, davTrashPrefix, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	err := f.client().RestoreTrash(context.Background(), "key-1", "/eos/user/e/einstein/recovered.txt", "")
	if err != nil {
		t.Fatal(err)
	}

	req := f.lastRequest(MethodMove)
	dst := req.Header.Get("Destination")
	if !strings.HasSuffix(dst, "/eos/user/e/einstein/recovered.txt") {
		t.Errorf("Destination = %q", dst)
	}
	// Restoring must not silently clobber a file that already exists there.
	if got := req.Header.Get("Overwrite"); got != "F" {
		t.Errorf("Overwrite = %q, want F", got)
	}
}

func TestPurgeTrashItem(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodDelete, davTrashPrefix, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := f.client().PurgeTrash(context.Background(), "key-1", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.lastRequest(http.MethodDelete).Path, "/key-1") {
		t.Errorf("purge path = %q", f.lastRequest(http.MethodDelete).Path)
	}
}

func TestPurgeWholeTrashBin(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodDelete, davTrashPrefix, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := f.client().PurgeTrash(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	// No key means the whole bin, so the path must stop at the username.
	if got := f.lastRequest(http.MethodDelete).Path; !strings.HasSuffix(got, "/einstein") {
		t.Errorf("purge path = %q, want the bin root", got)
	}
}

func TestTrashDeletedAtFromMilliseconds(t *testing.T) {
	f := newFakeServer(t)
	f.on(MethodPropfind, davTrashPrefix, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, trashMultistatus("einstein",
			trashFixture{Key: "key-1", Name: "notes.txt", Location: "Documents/notes.txt",
				Size: 10, Deleted: 1767225600000},
		))
	})

	items, err := f.client().ListTrash(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	if year := items[0].DeletedAt.Year(); year != 2026 {
		t.Errorf("deleted year = %d, want 2026: the millisecond timestamp was read as seconds", year)
	}
}
