package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

func TestMe(t *testing.T) {
	f := newFakeServer(t)
	me, err := f.client().Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.Username != "einstein" {
		t.Errorf("Username = %q", me.Username)
	}
	if me.DisplayName != "Albert Einstein" {
		t.Errorf("DisplayName = %q", me.DisplayName)
	}
	if me.Mail != "einstein@cern.ch" {
		t.Errorf("Mail = %q", me.Mail)
	}
}

func TestMeFetchedOnce(t *testing.T) {
	f := newFakeServer(t)
	var calls atomic.Int32
	f.on(http.MethodGet, graphV1+"/me", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, defaultMe)
	})

	c := f.client()
	for range 3 {
		if _, err := c.Me(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("identity fetched %d times, want 1", got)
	}
}

// TestMeWithoutUsernameIsAnError: every path-addressed WebDAV URL embeds the
// username, so proceeding without one would produce silently wrong URLs.
func TestMeWithoutUsernameIsAnError(t *testing.T) {
	f := newFakeServer(t)
	f.meJSON = `{"id": "abc", "displayName": "No Name"}`

	_, err := f.client().Me(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not return a username") {
		t.Errorf("got %v, want a clear error about the missing username", err)
	}
}

func TestSpaces(t *testing.T) {
	f := newFakeServer(t)
	spaces, err := f.client().Spaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 2 {
		t.Fatalf("got %d spaces, want 2", len(spaces))
	}

	home := spaces[0]
	if home.Type != "personal" {
		t.Errorf("first space type = %q", home.Type)
	}
	if home.Path != "/eos/user/e/einstein" {
		t.Errorf("home path = %q, want the drive alias turned into an absolute path", home.Path)
	}
	if home.QuotaTotal != 1073741824 || home.QuotaUsed != 524288 {
		t.Errorf("quota = %+v", home)
	}

	project := spaces[1]
	if project.Type != "project" || project.Path != "/eos/project/c/cernbox" {
		t.Errorf("project space = %+v", project)
	}
}

func TestSpacesCached(t *testing.T) {
	f := newFakeServer(t)
	var calls atomic.Int32
	f.on(http.MethodGet, graphBeta+"/me/drives", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, defaultDrives)
	})

	c := f.client(WithSpacesTTL(time.Hour))
	for range 3 {
		if _, err := c.Spaces(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("spaces fetched %d times, want 1 within the TTL", got)
	}
}

func TestSpacesCacheExpires(t *testing.T) {
	f := newFakeServer(t)
	var calls atomic.Int32
	f.on(http.MethodGet, graphBeta+"/me/drives", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, defaultDrives)
	})

	c := f.client(WithSpacesTTL(time.Nanosecond))
	for range 2 {
		if _, err := c.Spaces(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got < 2 {
		t.Errorf("spaces fetched %d times, want a refetch after the TTL", got)
	}
}

// TestResolveSpace covers every spelling a user might reasonably type.
func TestResolveSpace(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	ctx := context.Background()

	tests := []struct {
		alias string
		want  string
	}{
		{"home", "/eos/user/e/einstein"},
		{"personal", "/eos/user/e/einstein"},
		{"einstein", "/eos/user/e/einstein"},
		{"eos/user/e/einstein", "/eos/user/e/einstein"},
		{"localhome$MFZWI...", "/eos/user/e/einstein"},
		{"project/cernbox", "/eos/project/c/cernbox"},
		{"cernbox", "/eos/project/c/cernbox"},
		{"eos/project/c/cernbox", "/eos/project/c/cernbox"},
	}
	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			got, err := c.ResolveSpace(ctx, tt.alias)
			if err != nil {
				t.Fatalf("ResolveSpace(%q): %v", tt.alias, err)
			}
			if got != tt.want {
				t.Errorf("ResolveSpace(%q) = %q, want %q", tt.alias, got, tt.want)
			}
		})
	}
}

func TestResolveSpaceUnknown(t *testing.T) {
	f := newFakeServer(t)
	_, err := f.client().ResolveSpace(context.Background(), "project/does-not-exist")
	if cberr.ExitCode(err) != cberr.ExitNotFound {
		t.Errorf("exit code = %d, want %d", cberr.ExitCode(err), cberr.ExitNotFound)
	}
	if !strings.Contains(err.Error(), "cernbox space list") {
		t.Errorf("error should tell the user how to see their spaces, got %q", err)
	}
}

func TestSpaceAlias(t *testing.T) {
	tests := []struct {
		space Space
		want  string
	}{
		{Space{Type: "personal", Name: "einstein"}, "home"},
		{Space{Type: "project", Name: "cernbox"}, "project/cernbox"},
		{Space{Type: "other", Name: "x", Alias: "winspaces/x"}, "winspaces/x"},
		{Space{Type: "other", Name: "x"}, "x"},
	}
	for _, tt := range tests {
		if got := SpaceAlias(tt.space); got != tt.want {
			t.Errorf("SpaceAlias(%+v) = %q, want %q", tt.space, got, tt.want)
		}
	}
}

func TestRoleIDAndName(t *testing.T) {
	for _, name := range []string{"viewer", "editor", "collab", "denied", "VIEWER"} {
		if _, err := RoleID(name); err != nil {
			t.Errorf("RoleID(%q): %v", name, err)
		}
	}
	if _, err := RoleID("superuser"); err == nil {
		t.Error("RoleID(\"superuser\") should fail")
	} else if cberr.KindOf(err) != cberr.KindUsage {
		t.Errorf("unknown role should be a usage error, got %v", cberr.KindOf(err))
	}

	if got := RoleName(RoleEditor); got != "editor" {
		t.Errorf("RoleName(RoleEditor) = %q", got)
	}
	if got := RoleName(RoleSpaceViewer); got != "viewer" {
		t.Errorf("RoleName(RoleSpaceViewer) = %q, want the space variant folded into viewer", got)
	}
	// An unknown id is shown rather than hidden, so a server that grows a new
	// role does not display a blank column.
	if got := RoleName("some-new-role"); got != "some-new-role" {
		t.Errorf("RoleName(unknown) = %q, want it passed through", got)
	}
}

func TestSplitResourceID(t *testing.T) {
	spaceID, fullID, ok := splitResourceID("localhome$MFZWI...!fileid-1")
	if !ok {
		t.Fatal("splitResourceID failed on a well-formed id")
	}
	if spaceID != "localhome$MFZWI..." {
		t.Errorf("spaceID = %q", spaceID)
	}
	if fullID != "localhome$MFZWI...!fileid-1" {
		t.Errorf("fullID = %q, want the whole id", fullID)
	}

	for _, bad := range []string{"", "no-separator", "!", "abc!"} {
		if _, _, ok := splitResourceID(bad); ok {
			t.Errorf("splitResourceID(%q) should fail", bad)
		}
	}
}

// TestShareRequestShape matters because ocgraph decodes the invite body with
// DisallowUnknownFields: a stray or misspelled field is a 400, not a warning.
func TestShareRequestShape(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, graphBeta, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":[{"id":"share-1","roles":["`+RoleEditor+`"],
		  "grantedToV2":{"user":{"id":"marie","displayName":"Marie Curie"}}}]}`)
	})

	perms, err := f.client().Share(context.Background(), "localhome$ABC!item-1",
		[]Recipient{{ID: "marie", Type: "user"}}, RoleEditor, nil)
	if err != nil {
		t.Fatal(err)
	}

	var sent map[string]any
	body := f.lastRequest(http.MethodPost).Body
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, body)
	}
	if _, present := sent["expirationDateTime"]; present {
		t.Error("expirationDateTime must be omitted when unset, not sent as null")
	}
	recipients, _ := sent["recipients"].([]any)
	if len(recipients) != 1 {
		t.Fatalf("recipients = %v", sent["recipients"])
	}
	first, _ := recipients[0].(map[string]any)
	if first["objectId"] != "marie" {
		t.Errorf("objectId = %v", first["objectId"])
	}
	if first["@libre.graph.recipient.type"] != "user" {
		t.Errorf("recipient type key or value is wrong: %v", first)
	}

	if len(perms) != 1 {
		t.Fatalf("got %d permissions, want 1", len(perms))
	}
	if perms[0].ID != "share-1" || perms[0].Role != "editor" {
		t.Errorf("permission = %+v", perms[0])
	}
	if perms[0].GrantedTo == nil || perms[0].GrantedTo.ID != "marie" || perms[0].GrantedTo.Type != "user" {
		t.Errorf("grantedTo = %+v", perms[0].GrantedTo)
	}
}

func TestShareIncludesExpiryWhenSet(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, graphBeta, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"value":[]}`)
	})

	expiry := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	_, err := f.client().Share(context.Background(), "localhome$ABC!item-1",
		[]Recipient{{ID: "marie", Type: "user"}}, RoleViewer, &expiry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastRequest(http.MethodPost).Body, "2026-12-31") {
		t.Errorf("expiry missing from body: %s", f.lastRequest(http.MethodPost).Body)
	}
}

func TestShareRejectsUnparseableResourceID(t *testing.T) {
	f := newFakeServer(t)
	_, err := f.client().Share(context.Background(), "not-a-resource-id",
		[]Recipient{{ID: "marie", Type: "user"}}, RoleViewer, nil)
	if err == nil {
		t.Fatal("expected an error for a malformed resource id")
	}
	if n := f.countRequests(http.MethodPost); n != 0 {
		t.Errorf("a malformed id produced %d requests, want 0", n)
	}
}

func TestListPermissions(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodGet, graphBeta+"/drives", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"value":[
		  {"id":"share-1","roles":["`+RoleViewer+`"],"grantedToV2":{"group":{"id":"physics-lovers","displayName":"Physics"}}},
		  {"id":"link-1","link":{"type":"view","webUrl":"https://cernbox.test/s/abc","@libre.graph.permissions.link.hasPassword":true}}
		]}`)
	})

	perms, err := f.client().ListPermissions(context.Background(), "localhome$ABC!item-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(perms) != 2 {
		t.Fatalf("got %d permissions, want 2", len(perms))
	}
	if perms[0].GrantedTo == nil || perms[0].GrantedTo.Type != "group" {
		t.Errorf("first permission should be a group share: %+v", perms[0])
	}
	if perms[1].Link == nil {
		t.Fatalf("second permission should be a link: %+v", perms[1])
	}
	if perms[1].Link.URL != "https://cernbox.test/s/abc" || !perms[1].Link.HasPassword {
		t.Errorf("link = %+v", perms[1].Link)
	}
}

func TestUpdatePermissionRequiresSomethingToChange(t *testing.T) {
	f := newFakeServer(t)
	_, err := f.client().UpdatePermission(context.Background(), "localhome$ABC!item-1", "share-1", "", nil)
	if cberr.KindOf(err) != cberr.KindUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

func TestUpdatePermission(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPatch, graphBeta+"/drives", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"share-1","roles":["`+RoleViewer+`"]}`)
	})

	p, err := f.client().UpdatePermission(context.Background(), "localhome$ABC!item-1", "share-1", RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Role != "viewer" {
		t.Errorf("role = %q", p.Role)
	}
	if !strings.Contains(f.lastRequest(http.MethodPatch).Path, "/permissions/share-1") {
		t.Errorf("PATCH path = %q", f.lastRequest(http.MethodPatch).Path)
	}
}

func TestRemovePermission(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodDelete, graphBeta+"/drives", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := f.client().RemovePermission(context.Background(), "localhome$ABC!item-1", "share-1"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateLink(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, graphBeta+"/drives", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"link-1","link":{"type":"edit","webUrl":"https://cernbox.test/s/xyz"}}`)
	})

	expiry := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p, err := f.client().CreateLink(context.Background(), "localhome$ABC!item-1", LinkOptions{
		Type: "edit", DisplayName: "review", Password: "hunter2", Expiry: &expiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Link == nil || p.Link.URL != "https://cernbox.test/s/xyz" {
		t.Fatalf("link = %+v", p.Link)
	}

	body := f.lastRequest(http.MethodPost).Body
	for _, want := range []string{`"type":"edit"`, `"displayName":"review"`, `"password":"hunter2"`, "2026-10-01"} {
		if !strings.Contains(body, want) {
			t.Errorf("createLink body is missing %s:\n%s", want, body)
		}
	}
	if !strings.HasSuffix(f.lastRequest(http.MethodPost).Path, "/createLink") {
		t.Errorf("createLink path = %q", f.lastRequest(http.MethodPost).Path)
	}
}

func TestCreateLinkOmitsUnsetFields(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, graphBeta+"/drives", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"link-1"}`)
	})

	_, err := f.client().CreateLink(context.Background(), "localhome$ABC!item-1", LinkOptions{Type: "view"})
	if err != nil {
		t.Fatal(err)
	}
	body := f.lastRequest(http.MethodPost).Body
	for _, unwanted := range []string{"password", "displayName", "expirationDateTime"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body should omit %s when unset:\n%s", unwanted, body)
		}
	}
}

func TestSharedWithMe(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodGet, graphBeta+"/me/drive/sharedWithMe", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"value":[
		  {"id":"item-1","name":"Shared Folder","size":4096,
		   "@client.synchronize": true,
		   "parentReference":{"path":"/eos/project/c/cernbox"},
		   "createdBy":{"user":{"id":"marie","displayName":"Marie Curie"}},
		   "permissions":[{"id":"p1","roles":["`+RoleViewer+`"]}]}
		]}`)
	})

	items, err := f.client().SharedWithMe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.Name != "Shared Folder" || it.Size != 4096 {
		t.Errorf("item = %+v", it)
	}
	if it.SharedBy == nil || it.SharedBy.DisplayName != "Marie Curie" {
		t.Errorf("sharedBy = %+v", it.SharedBy)
	}
	if it.Role != "viewer" {
		t.Errorf("role = %q", it.Role)
	}
	if !it.Accepted {
		t.Error("an item marked @client.synchronize should read as accepted")
	}
}

func TestSetReceivedShareState(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPatch, graphBeta+"/drives", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})

	c := f.client()
	if err := c.SetReceivedShareState(context.Background(), "localhome$ABC!item-1", true); err != nil {
		t.Fatal(err)
	}
	body := f.lastRequest(http.MethodPatch).Body
	if !strings.Contains(body, `"@client.synchronize":true`) || !strings.Contains(body, `"@UI.Hidden":false`) {
		t.Errorf("accept body = %s", body)
	}

	if err := c.SetReceivedShareState(context.Background(), "localhome$ABC!item-1", false); err != nil {
		t.Fatal(err)
	}
	body = f.lastRequest(http.MethodPatch).Body
	if !strings.Contains(body, `"@client.synchronize":false`) || !strings.Contains(body, `"@UI.Hidden":true`) {
		t.Errorf("decline body = %s", body)
	}
}

func TestMalformedJSONIsReported(t *testing.T) {
	f := newFakeServer(t)
	f.meJSON = "{not json"

	_, err := f.client().Me(context.Background())
	if err == nil || !strings.Contains(err.Error(), "malformed server response") {
		t.Errorf("got %v, want a clear parse error", err)
	}
}
