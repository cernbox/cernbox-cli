package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/output"
)

// testBox is a minimal CERNBox for driving the command tree end to end.
type testBox struct {
	ts    *httptest.Server
	files map[string]string
	dirs  map[string]bool

	trash    map[string]trashEntry
	versions map[string][]versionEntry
	restored string

	// appMethod is the HTTP method the fake application session advertises.
	appMethod string

	requests []string
}

type trashEntry struct {
	name     string
	location string
	body     string
}

type versionEntry struct {
	key  string
	body string
}

const (
	testDavPrefix   = "/remote.php/dav/files/einstein"
	testTrashPrefix = "/remote.php/dav/trash-bin/einstein"
	testMetaPrefix  = "/remote.php/dav/meta"
)

func newTestBox(t *testing.T) *testBox {
	t.Helper()
	b := &testBox{
		files:    map[string]string{},
		dirs:     map[string]bool{"/": true},
		trash:    map[string]trashEntry{},
		versions: map[string][]versionEntry{},
	}
	b.ts = httptest.NewServer(http.HandlerFunc(b.route))
	t.Cleanup(b.ts.Close)
	return b
}

func (b *testBox) route(w http.ResponseWriter, r *http.Request) {
	b.requests = append(b.requests, r.Method+" "+r.URL.Path)

	switch {
	case strings.HasPrefix(r.URL.Path, "/ocs/v1.php/cloud/capabilities"):
		fmt.Fprint(w, `{"ocs":{"data":{"version":{"string":"10.0.11"},"capabilities":{
		  "core":{"webdav-root":"remote.php/webdav","status":{"productname":"reva","versionstring":"10.0.11"}},
		  "checksums":{"supportedTypes":["md5"],"preferredUploadType":"md5"},
		  "files":{"versioning":true,"undelete":"1"},
		  "files_sharing":{"api_enabled":1,"public":{"enabled":true}},
		  "spaces":{"enabled":true}}}}}`)

	case r.URL.Path == "/ocs/v1.php/cloud/user/clients":
		fmt.Fprint(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":[
		  {"id":"c1","name":"laptop","description":"cernbox-sync","created_at":"2026-01-02T10:00:00Z","last_seen_at":"2026-09-01T08:00:00Z"}
		]}}`)

	case r.URL.Path == "/app/open":
		method := b.appMethod
		if method == "" {
			method = "GET"
		}
		fmt.Fprintf(w, `{"app_url":"https://office.test/edit?wopi=abc","method":%q,`+
			`"form_parameters":{"access_token":"secret"}}`, method)

	case r.URL.Path == "/app/list":
		fmt.Fprint(w, `{"mime-types":[{"mime_type":"application/vnd.oasis.opendocument.text",`+
			`"ext":"odt","default_application":{"name":"Collabora"},`+
			`"app_providers":[{"name":"Collabora"}]}]}`)

	case r.URL.Path == "/graph/v1.0/me":
		fmt.Fprint(w, `{"id":"u1","displayName":"Albert Einstein","mail":"einstein@cern.ch","onPremisesSamAccountName":"einstein"}`)

	case r.URL.Path == "/graph/v1beta1/me/drives":
		fmt.Fprint(w, `{"value":[
		  {"id":"s1","name":"einstein","driveType":"personal","driveAlias":"eos/user/e/einstein",
		   "quota":{"total":1073741824,"used":524288,"remaining":1073217536}},
		  {"id":"s2","name":"cernbox","driveType":"project","driveAlias":"eos/project/c/cernbox"}
		]}`)

	case strings.HasPrefix(r.URL.Path, "/graph/v1beta1/me/drive/sharedWithMe"):
		fmt.Fprint(w, `{"value":[{"id":"item-1","name":"Shared","@client.synchronize":true,
		  "createdBy":{"user":{"id":"marie","displayName":"Marie Curie"}},
		  "permissions":[{"id":"p1","roles":["b1e2218d-eef8-4d4c-b82d-0f1a1b48f3b5"]}]}]}`)

	case strings.HasPrefix(r.URL.Path, "/graph/v1beta1/drives/"):
		b.serveGraphItem(w, r)

	case strings.HasPrefix(r.URL.Path, testTrashPrefix):
		b.serveTrash(w, r)

	case strings.HasPrefix(r.URL.Path, testMetaPrefix):
		b.serveVersions(w, r)

	case strings.HasPrefix(r.URL.Path, testDavPrefix):
		b.serveDav(w, r)

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (b *testBox) serveGraphItem(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/invite"):
		fmt.Fprint(w, `{"value":[{"id":"share-1","roles":["fb6c3e19-e378-47e5-b277-9732f9de6e21"],
		  "grantedToV2":{"user":{"id":"marie","displayName":"Marie Curie"}}}]}`)
	case strings.HasSuffix(r.URL.Path, "/createLink"):
		fmt.Fprint(w, `{"id":"link-1","link":{"type":"view","webUrl":"https://cernbox.test/s/abc"}}`)
	case strings.HasSuffix(r.URL.Path, "/permissions"):
		fmt.Fprint(w, `{"value":[
		  {"id":"share-1","roles":["b1e2218d-eef8-4d4c-b82d-0f1a1b48f3b5"],"grantedToV2":{"user":{"id":"marie"}}},
		  {"id":"link-1","link":{"type":"view","webUrl":"https://cernbox.test/s/abc"}}]}`)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		fmt.Fprint(w, `{}`)
	}
}

func (b *testBox) serveDav(w http.ResponseWriter, r *http.Request) {
	p := path.Clean(strings.TrimPrefix(r.URL.Path, testDavPrefix))
	if p == "" {
		p = "/"
	}

	switch r.Method {
	case "PROPFIND":
		var entries []string
		switch {
		case b.dirs[p]:
			entries = append(entries, davXML(p, true, 0))
			if r.Header.Get("Depth") == "1" {
				for f, body := range b.files {
					if path.Dir(f) == p {
						entries = append(entries, davXML(f, false, len(body)))
					}
				}
				for d := range b.dirs {
					if d != p && path.Dir(d) == p {
						entries = append(entries, davXML(d, true, 0))
					}
				}
			}
		case b.files[p] != "":
			entries = append(entries, davXML(p, false, len(b.files[p])))
		default:
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`+
			strings.Join(entries, "")+`</d:multistatus>`)

	case http.MethodGet:
		body, ok := b.files[p]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		fmt.Fprint(w, body)

	case http.MethodPut:
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		b.files[p] = sb.String()
		w.WriteHeader(http.StatusCreated)

	case "MKCOL":
		if b.dirs[p] {
			http.Error(w, "exists", http.StatusMethodNotAllowed)
			return
		}
		b.dirs[p] = true
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		delete(b.files, p)
		delete(b.dirs, p)
		w.WriteHeader(http.StatusNoContent)

	case "MOVE":
		if body, ok := b.files[p]; ok {
			dst := r.Header.Get("Destination")
			if i := strings.Index(dst, testDavPrefix); i >= 0 {
				b.files[path.Clean(dst[i+len(testDavPrefix):])] = body
				delete(b.files, p)
			}
		}
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func davXML(p string, isDir bool, size int) string {
	href := testDavPrefix + p
	rt := "<d:resourcetype></d:resourcetype>"
	if isDir {
		href += "/"
		rt = "<d:resourcetype><d:collection/></d:resourcetype>"
	}
	return fmt.Sprintf(`<d:response><d:href>%s</d:href><d:propstat>`+
		`<d:status>HTTP/1.1 200 OK</d:status><d:prop>`+
		`<d:displayname>%s</d:displayname>%s`+
		`<d:getcontentlength>%d</d:getcontentlength><oc:size>%d</oc:size>`+
		`<d:getetag>&quot;etag-1&quot;</d:getetag>`+
		`<oc:fileid>s1$ABC!%s</oc:fileid>`+
		`<oc:privatelink>https://cernbox.test/files/spaces/s1%s</oc:privatelink>`+
		`<d:getlastmodified>Mon, 02 Jan 2026 15:04:05 GMT</d:getlastmodified>`+
		`</d:prop></d:propstat></d:response>`,
		href, path.Base(p), rt, size, size, strings.ReplaceAll(p, "/", "_"), p)
}

// serveTrash implements the trash-bin endpoints: a PROPFIND listing, MOVE to
// restore, DELETE to purge.
func (b *testBox) serveTrash(w http.ResponseWriter, r *http.Request) {
	key := strings.Trim(strings.TrimPrefix(r.URL.Path, testTrashPrefix), "/")

	switch r.Method {
	case "PROPFIND":
		var entries []string
		entries = append(entries, fmt.Sprintf(
			`<d:response><d:href>%s/</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status>`+
				`<d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop>`+
				`</d:propstat></d:response>`, testTrashPrefix))
		for k, item := range b.trash {
			entries = append(entries, fmt.Sprintf(
				`<d:response><d:href>%s/%s</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop>`+
					`<d:displayname>%s</d:displayname><d:resourcetype></d:resourcetype>`+
					`<d:getcontentlength>%d</d:getcontentlength><oc:size>%d</oc:size>`+
					`<oc:trashbin-original-filename>%s</oc:trashbin-original-filename>`+
					`<oc:trashbin-original-location>%s</oc:trashbin-original-location>`+
					`<oc:trashbin-delete-timestamp>1767225600</oc:trashbin-delete-timestamp>`+
					`</d:prop></d:propstat></d:response>`,
				testTrashPrefix, k, item.name, len(item.body), len(item.body), item.name, item.location))
		}
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`+
			strings.Join(entries, "")+`</d:multistatus>`)

	case "MOVE":
		item, ok := b.trash[key]
		if !ok {
			http.Error(w, "no such trash item", http.StatusNotFound)
			return
		}
		dst := "/" + item.location
		if h := r.Header.Get("Destination"); h != "" {
			if i := strings.Index(h, testDavPrefix); i >= 0 {
				dst = path.Clean(h[i+len(testDavPrefix):])
			}
		}
		b.files[dst] = item.body
		delete(b.trash, key)
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		if key == "" {
			b.trash = map[string]trashEntry{}
		} else {
			delete(b.trash, key)
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveVersions implements the meta endpoints for version history.
func (b *testBox) serveVersions(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, testMetaPrefix), "/")
	resourceID, after, _ := strings.Cut(rest, "/")
	key := strings.TrimPrefix(after, "v/")
	if key == "v" {
		key = ""
	}

	switch r.Method {
	case "PROPFIND":
		entries := []string{fmt.Sprintf(
			`<d:response><d:href>%s/%s/v</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status>`+
				`<d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop>`+
				`</d:propstat></d:response>`, testMetaPrefix, resourceID)}
		for _, v := range b.versions[resourceID] {
			entries = append(entries, fmt.Sprintf(
				`<d:response><d:href>%s/%s/v/%s</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop>`+
					`<d:resourcetype></d:resourcetype><d:getcontentlength>%d</d:getcontentlength>`+
					`<d:getetag>&quot;etag-%s&quot;</d:getetag></d:prop></d:propstat></d:response>`,
				testMetaPrefix, resourceID, v.key, len(v.body), v.key))
		}
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`+
			strings.Join(entries, "")+`</d:multistatus>`)

	case http.MethodGet:
		for _, v := range b.versions[resourceID] {
			if v.key == key {
				fmt.Fprint(w, v.body)
				return
			}
		}
		http.Error(w, "no such version", http.StatusNotFound)

	case "COPY":
		b.restored = key
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *testBox) mkdir(p string) {
	for d := p; d != "/" && d != "."; d = path.Dir(d) {
		b.dirs[d] = true
	}
}

func (b *testBox) putFile(p, body string) {
	b.files[p] = body
	b.mkdir(path.Dir(p))
}

// run executes the command tree with the given arguments and returns stdout,
// stderr and the error.
func run(t *testing.T, box *testBox, args ...string) (string, string, error) {
	t.Helper()

	// Point the user config at a file that does not exist, so a developer's
	// real configuration cannot influence the test.
	t.Setenv("CERNBOX_CONFIG", filepath.Join(t.TempDir(), "none.yaml"))
	t.Setenv("CERNBOX_TOKEN_CACHE", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("CERNBOX_TOKEN", "")
	t.Setenv("CERNBOX_APP_TOKEN", "")

	var stdout, stderr bytes.Buffer
	app := &App{flags: &globalFlags{}, stdout: &stdout, stderr: &stderr}
	root := newRootCmd(app)
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	full := append([]string{"--endpoint", box.ts.URL, "--token", "test-token"}, args...)
	root.SetArgs(full)

	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestLsCommand(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents")
	box.putFile("/eos/user/e/einstein/notes.txt", "hello")

	stdout, _, err := run(t, box, "ls", "/eos/user/e/einstein")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "notes.txt") {
		t.Errorf("ls output is missing the file:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Documents/") {
		t.Errorf("ls should mark directories with a trailing slash:\n%s", stdout)
	}
}

func TestLsDefaultsToHome(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "hello")

	stdout, _, err := run(t, box, "ls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "notes.txt") {
		t.Errorf("bare ls should list the home space:\n%s", stdout)
	}
}

func TestLsWithSpaceAlias(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/project/c/cernbox/data.txt", "x")

	stdout, _, err := run(t, box, "ls", "project/cernbox:")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "data.txt") {
		t.Errorf("a project alias should resolve:\n%s", stdout)
	}
}

// TestLsJSONIsMachineReadable is the scripting contract: stdout under
// --output json must parse, with nothing else mixed in.
func TestLsJSONIsMachineReadable(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "hello")

	stdout, _, err := run(t, box, "--output", "json", "ls", "/eos/user/e/einstein")
	if err != nil {
		t.Fatal(err)
	}

	var entries []struct {
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		IsDir bool   `json:"is_dir"`
	}
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if len(entries) == 0 || entries[0].Name != "notes.txt" {
		t.Errorf("entries = %+v", entries)
	}
	if entries[0].Size != 5 {
		t.Errorf("size = %d, want the exact byte count", entries[0].Size)
	}
}

func TestStatCommand(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "hello")

	stdout, _, err := run(t, box, "stat", "/eos/user/e/einstein/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"notes.txt", "etag-1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stat output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestStatMissingPathExitsFive(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "stat", "/eos/user/e/einstein/ghost.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := cberr.ExitCode(err); got != cberr.ExitNotFound {
		t.Errorf("exit code = %d, want %d", got, cberr.ExitNotFound)
	}
}

func TestMkdirAndRm(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein")

	if _, _, err := run(t, box, "mkdir", "/eos/user/e/einstein/new"); err != nil {
		t.Fatal(err)
	}
	if !box.dirs["/eos/user/e/einstein/new"] {
		t.Fatal("mkdir did not create the directory")
	}

	// Deleting a directory without -r must be refused: WebDAV DELETE on a
	// collection is always recursive.
	_, _, err := run(t, box, "rm", "/eos/user/e/einstein/new")
	if err == nil {
		t.Fatal("rm on a directory without -r should fail")
	}
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("exit code = %d, want a usage error", cberr.ExitCode(err))
	}
	if !box.dirs["/eos/user/e/einstein/new"] {
		t.Error("the directory was deleted despite the refusal")
	}

	if _, _, err := run(t, box, "rm", "-r", "/eos/user/e/einstein/new"); err != nil {
		t.Fatal(err)
	}
	if box.dirs["/eos/user/e/einstein/new"] {
		t.Error("rm -r did not delete the directory")
	}
}

func TestRmForceIgnoresMissingPaths(t *testing.T) {
	box := newTestBox(t)
	if _, _, err := run(t, box, "rm", "-f", "/eos/user/e/einstein/ghost.txt"); err != nil {
		t.Errorf("rm -f on a missing path should succeed, got %v", err)
	}
}

func TestCatCommand(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "file contents here")

	stdout, _, err := run(t, box, "cat", "/eos/user/e/einstein/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "file contents here" {
		t.Errorf("cat wrote %q", stdout)
	}
}

func TestPutAndGet(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein")

	dir := t.TempDir()
	local := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(local, []byte("report body"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, box, "put", local, "/eos/user/e/einstein/report.txt"); err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/report.txt"]; got != "report body" {
		t.Fatalf("uploaded content = %q", got)
	}

	back := filepath.Join(dir, "downloaded.txt")
	if _, _, err := run(t, box, "get", "/eos/user/e/einstein/report.txt", back); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "report body" {
		t.Errorf("downloaded content = %q", got)
	}
}

// TestCpRefusesTwoUnmarkedPaths is the lxplus ambiguity, surfaced at the
// command level: both sides look like CERNBox paths and neither is marked.
func TestCpRefusesTwoUnmarkedPaths(t *testing.T) {
	box := newTestBox(t)

	_, _, err := run(t, box, "cp", "/eos/user/e/einstein/a.txt", "/eos/user/e/einstein/b.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("exit code = %d, want a usage error", cberr.ExitCode(err))
	}
	if !strings.Contains(err.Error(), "cb:") {
		t.Errorf("the error should show the cb: form, got %q", err)
	}
}

func TestCpUploadsWithCbPrefix(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein")

	local := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(local, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, box, "cp", local, "cb:/eos/user/e/einstein/a.txt"); err != nil {
		t.Fatal(err)
	}
	if box.files["/eos/user/e/einstein/a.txt"] != "alpha" {
		t.Errorf("uploaded = %q", box.files["/eos/user/e/einstein/a.txt"])
	}
}

func TestPutDryRunWritesNothing(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein")

	local := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(local, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, box, "put", "--dry-run", local, "/eos/user/e/einstein/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, ok := box.files["/eos/user/e/einstein/a.txt"]; ok {
		t.Error("a dry run uploaded the file")
	}
}

func TestMvCommand(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/a.txt", "content")

	if _, _, err := run(t, box, "mv", "/eos/user/e/einstein/a.txt", "/eos/user/e/einstein/b.txt"); err != nil {
		t.Fatal(err)
	}
	if box.files["/eos/user/e/einstein/b.txt"] != "content" {
		t.Errorf("mv did not move the file: %+v", box.files)
	}
}

func TestWhoamiCommand(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "einstein") {
		t.Errorf("whoami output:\n%s", stdout)
	}
}

func TestStatusCommand(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Endpoint", "einstein", "Token cache"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestSpaceListCommand(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "space", "list")
	if err != nil {
		t.Fatal(err)
	}
	// The alias column is what users type as a path prefix, so it must be there.
	for _, want := range []string{"home", "project/cernbox", "/eos/user/e/einstein"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("space list is missing %q:\n%s", want, stdout)
		}
	}
}

func TestShareCreateCommand(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "x")

	stdout, _, err := run(t, box, "share", "create", "/eos/user/e/einstein/notes.txt",
		"--with", "marie", "--role", "editor")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "share-1") || !strings.Contains(stdout, "editor") {
		t.Errorf("share output:\n%s", stdout)
	}
}

func TestShareCreateRequiresRecipient(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "x")

	_, _, err := run(t, box, "share", "create", "/eos/user/e/einstein/notes.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("exit code = %d, want a usage error", cberr.ExitCode(err))
	}
}

func TestShareCreateRejectsUnknownRole(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "x")

	_, _, err := run(t, box, "share", "create", "/eos/user/e/einstein/notes.txt",
		"--with", "marie", "--role", "overlord")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

func TestShareReceivedCommand(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "share", "received")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Marie Curie") {
		t.Errorf("received shares output:\n%s", stdout)
	}
}

func TestLinkCreateCommand(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "x")

	stdout, stderr, err := run(t, box, "link", "create", "/eos/user/e/einstein/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout+stderr, "https://cernbox.test/s/abc") {
		t.Errorf("the link URL should be shown:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

func TestTokenListCommand(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "token", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "cernbox-sync") {
		t.Errorf("token list output:\n%s", stdout)
	}
}

// TestTokenCreateExplainsItself: the endpoint does not exist server side, so
// the CLI must say what to do instead rather than surfacing a 404.
func TestTokenCreateExplainsItself(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "token", "create", "--all")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "web interface") {
		t.Errorf("the error should say where to create a token, got %q", err)
	}
}

func TestVersionCommandNeedsNoServer(t *testing.T) {
	// No endpoint, no credentials: version must still work, because it is what
	// a user runs when nothing else does.
	var stdout, stderr bytes.Buffer
	app := &App{flags: &globalFlags{}, stdout: &stdout, stderr: &stderr}
	root := newRootCmd(app)
	root.SilenceErrors = true
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Version") {
		t.Errorf("version output:\n%s", stdout.String())
	}
}

func TestCompletionNeedsNoServer(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &App{flags: &globalFlags{}, stdout: &stdout, stderr: &stderr}
	root := newRootCmd(app)
	root.SilenceErrors = true
	root.SetOut(&stdout)
	root.SetArgs([]string{"completion", "bash"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestBadOutputFormatIsAUsageError(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "--output", "yaml", "ls")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

// TestInsecureIsRefusedForCERNHosts: a flag meant for a laptop instance must
// not quietly become a habit that applies to production.
func TestInsecureIsRefusedForCERNHosts(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &App{
		flags:  &globalFlags{outputFormat: "table", skipVerify: true},
		stdout: &stdout,
		stderr: &stderr,
	}
	app.out = nil
	root := newRootCmd(app)
	root.SilenceErrors = true
	root.SetArgs([]string{"--skip-verify", "--endpoint", "https://cernbox.cern.ch", "version"})
	_ = root.Execute()

	// version short-circuits before the endpoint check, so exercise the helper
	// directly with a configured writer.
	app.out = mustWriter(&stdout, &stderr)
	app.warnInsecure("https://cernbox.cern.ch")
	if !strings.Contains(stderr.String(), "refusing") {
		t.Errorf("stderr = %q, want a refusal for a CERN host", stderr.String())
	}

	stderr.Reset()
	app.warnInsecure("https://localhost:8080")
	if !strings.Contains(stderr.String(), "disabled") {
		t.Errorf("stderr = %q, want a warning for a non-CERN host", stderr.String())
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "frobnicate")
	if err == nil {
		t.Fatal("expected an error for an unknown command")
	}
}

// mustWriter builds an output writer for tests that call App helpers directly.
func mustWriter(stdout, stderr *bytes.Buffer) *output.Writer {
	return output.New(stdout, output.FormatTable, output.Stderr(stderr))
}
