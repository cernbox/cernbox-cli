//go:build integration

package integration_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// publicLinkRequest makes an unauthenticated WebDAV request against a public
// link, which is what somebody holding one actually is: no account, no
// credential, nothing but the token.
func publicLinkRequest(t *testing.T, method, linkURL, relative, body string) (int, string) {
	t.Helper()

	token := linkURL[strings.LastIndex(linkURL, "/")+1:]
	url := endpoint + "/remote.php/dav/public-files/" + token
	if relative != "" {
		url += "/" + relative
	}

	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if method == "PROPFIND" {
		req.Header.Set("Depth", "1")
	}

	// No credential is attached on purpose: a request that carried one would
	// prove nothing about what the link grants.
	resp, err := (&http.Client{Timeout: 20 * time.Second, Transport: devTransport()}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// TestUploadLinkTakesFilesFromAStranger is the whole point of the role: a
// folder somebody can put a file into without an account, and without being
// able to take anything out of it.
func TestUploadLinkTakesFilesFromAStranger(t *testing.T) {
	e := setup(t)

	drop := e.remotePath("drop")
	e.mustRun("mkdir", drop)
	e.mustRun("put", e.writeLocal("private.txt", []byte("not for strangers")),
		drop+"/private.txt")

	var created struct {
		ID   string `json:"id"`
		Link *struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"link"`
	}
	e.runJSONOne(&created, "link", "create", drop, "--role", "upload")
	if created.Link == nil || created.Link.URL == "" {
		t.Fatalf("link create returned no URL: %+v", created)
	}
	// The server has two names for this and answers with one of them.
	if created.Link.Type != "createOnly" {
		t.Errorf("link type = %q, want createOnly", created.Link.Type)
	}

	// Upload: the thing it exists for.
	if code, body := publicLinkRequest(t, http.MethodPut, created.Link.URL,
		"from-a-stranger.txt", "contributed"); code != http.StatusCreated && code != http.StatusOK {
		t.Errorf("anonymous upload returned %d, want it accepted: %s", code, body)
	}

	// Download: the thing it must not allow. A file that was already there is
	// the one worth checking, since that is what the folder's owner is risking.
	if code, _ := publicLinkRequest(t, http.MethodGet, created.Link.URL,
		"private.txt", ""); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("anonymous download returned %d, want it refused", code)
	}

	// The upload arrived. Its name is the server's business, not the sender's:
	// reva adds a suffix, so nothing already in the folder can be written over.
	var arrived bool
	for _, name := range e.names(drop) {
		if strings.HasPrefix(name, "from-a-stranger") {
			arrived = true
		}
	}
	if !arrived {
		t.Errorf("the upload is not in the folder: %v", e.names(drop))
	}

	e.mustRun("link", "remove", drop, created.ID)
}

// TestUploadLinkNeedsAFolder: the server refuses this with a message about
// matching permission sets that says nothing about the reason, so the CLI
// answers it before asking.
func TestUploadLinkNeedsAFolder(t *testing.T) {
	e := setup(t)
	file := e.remotePath("report.txt")
	e.mustRun("put", e.writeLocal("report.txt", []byte("x")), file)

	stdout, stderr, code := e.run("link", "create", file, "--role", "upload")
	if code == 0 {
		t.Fatalf("an upload link on a file should be refused:\n%s", stdout)
	}
	if !strings.Contains(stderr, "folder") {
		t.Errorf("the refusal does not say what is wrong:\n%s", stderr)
	}
}
