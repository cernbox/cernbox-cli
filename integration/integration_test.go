//go:build integration

package integration_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// ── identity and discovery ───────────────────────────────────────────────────

func TestWhoami(t *testing.T) {
	e := setup(t)

	var me struct {
		Username string `json:"username"`
	}
	e.runJSON(&me, "whoami")
	if me.Username != username {
		t.Errorf("whoami = %q, want %q", me.Username, username)
	}
}

func TestStatusReportsServerAndCredential(t *testing.T) {
	e := setup(t)
	out := e.mustRun("status")

	for _, want := range []string{"Endpoint", username, "basic"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
}

func TestSpaceListIncludesHome(t *testing.T) {
	e := setup(t)

	var spaces []struct {
		Type string `json:"type"`
		Path string `json:"path"`
	}
	e.runJSON(&spaces, "space", "list")

	found := false
	for _, s := range spaces {
		if s.Type == "personal" && s.Path == homeRoot {
			found = true
		}
	}
	if !found {
		t.Errorf("no personal space at %s in %+v", homeRoot, spaces)
	}
}

// TestHomeAliasResolves checks the alias path model against a real spaces
// listing rather than a fixture.
func TestHomeAliasResolves(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("alias.txt", []byte("via alias")), e.remotePath("alias.txt"))

	rel := strings.TrimPrefix(e.remote, homeRoot+"/")
	info := e.stat("home:" + rel + "/alias.txt")
	if info.Size != 9 {
		t.Errorf("stat through the home alias returned %+v", info)
	}
}

// ── namespace ────────────────────────────────────────────────────────────────

func TestMkdirListAndRemove(t *testing.T) {
	e := setup(t)

	e.mustRun("mkdir", e.remotePath("sub"))
	e.mustRun("touch", e.remotePath("sub/file.txt"))
	e.requireNames(e.remote, "sub")
	e.requireNames(e.remotePath("sub"), "file.txt")

	// A directory must not be removable without -r.
	_, _, code := e.run("rm", e.remotePath("sub"))
	if code != cberr.ExitUsage {
		t.Errorf("rm on a directory exited %d, want %d", code, cberr.ExitUsage)
	}
	e.requireNames(e.remote, "sub")

	e.mustRun("rm", "-r", e.remotePath("sub"))
	if got := e.names(e.remote); len(got) != 0 {
		t.Errorf("after rm -r the directory still contains %v", got)
	}
}

func TestMkdirParents(t *testing.T) {
	e := setup(t)
	e.mustRun("mkdir", "-p", e.remotePath("a/b/c"))

	info := e.stat(e.remotePath("a/b/c"))
	if !info.IsDir {
		t.Errorf("mkdir -p did not create a directory: %+v", info)
	}
}

func TestStatOnMissingPathExitsFive(t *testing.T) {
	e := setup(t)
	_, _, code := e.run("stat", e.remotePath("does-not-exist"))
	if code != cberr.ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, cberr.ExitNotFound)
	}
}

func TestMoveAndCat(t *testing.T) {
	e := setup(t)
	local := e.writeLocal("a.txt", []byte("move me"))
	e.mustRun("put", local, e.remotePath("a.txt"))

	e.mustRun("mv", e.remotePath("a.txt"), e.remotePath("b.txt"))
	e.requireNames(e.remote, "b.txt")

	if out := e.mustRun("cat", e.remotePath("b.txt")); out != "move me" {
		t.Errorf("cat = %q", out)
	}
}

func TestServerSideCopy(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("a.txt", []byte("copy me")), e.remotePath("a.txt"))

	e.mustRun("cp", "cb:"+e.remotePath("a.txt"), "cb:"+e.remotePath("c.txt"))
	if out := e.mustRun("cat", e.remotePath("c.txt")); out != "copy me" {
		t.Errorf("the server-side copy produced %q", out)
	}
}

// ── transfers ────────────────────────────────────────────────────────────────

func TestPutAndGetRoundTrip(t *testing.T) {
	e := setup(t)
	body := []byte("round trip contents")
	local := e.writeLocal("a.txt", body)

	e.mustRun("put", local, e.remotePath("a.txt"))
	e.mustRun("get", e.remotePath("a.txt"), e.localPath("back.txt"))

	if got := e.readLocal("back.txt"); !bytes.Equal(got, body) {
		t.Errorf("round trip changed the content: %q vs %q", got, body)
	}
}

// TestLargeFileUsesResumableUpload exercises the TUS path against a real
// server, where chunk handling and offsets actually matter.
func TestLargeFileUsesResumableUpload(t *testing.T) {
	e := setup(t)
	body := payload(12 << 20)
	local := e.writeLocal("big.bin", body)

	e.mustRun("put", local, e.remotePath("big.bin"))

	info := e.stat(e.remotePath("big.bin"))
	if info.Size != int64(len(body)) {
		t.Fatalf("uploaded size = %d, want %d", info.Size, len(body))
	}

	e.mustRun("get", e.remotePath("big.bin"), e.localPath("big-back.bin"))
	got := e.readLocal("big-back.bin")
	if sha256hex(got) != sha256hex(body) {
		t.Errorf("the large file round trip changed the content (%d bytes back, %d sent)", len(got), len(body))
	}
}

func TestPutWithVerify(t *testing.T) {
	e := setup(t)
	body := payload(1 << 20)
	local := e.writeLocal("verified.bin", body)

	e.mustRun("put", "--verify", local, e.remotePath("verified.bin"))
	e.mustRun("get", "--verify", e.remotePath("verified.bin"), e.localPath("verified-back.bin"))

	if got := e.readLocal("verified-back.bin"); sha256hex(got) != sha256hex(body) {
		t.Error("the verified round trip changed the content")
	}
}

func TestRecursiveUploadAndDownload(t *testing.T) {
	e := setup(t)

	e.writeLocal("tree/a.txt", []byte("alpha"))
	e.writeLocal("tree/sub/b.txt", []byte("beta"))
	e.writeLocal("tree/sub/deep/c.txt", []byte("gamma"))

	e.mustRun("put", "-r", e.localPath("tree"), e.remotePath("tree"))

	e.requireNames(e.remotePath("tree"), "a.txt", "sub")
	e.requireNames(e.remotePath("tree/sub"), "b.txt", "deep")

	dest := e.localPath("downloaded")
	e.mustRun("get", "-r", e.remotePath("tree"), dest)

	for rel, want := range map[string]string{
		"a.txt":          "alpha",
		"sub/b.txt":      "beta",
		"sub/deep/c.txt": "gamma",
	} {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("reading %s: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

// TestRecursiveDownloadWithoutArchiver checks the fallback path, which is what
// runs when the archiver is unavailable or the tree is too large for it.
func TestRecursiveDownloadWithoutArchiver(t *testing.T) {
	e := setup(t)
	e.writeLocal("tree/a.txt", []byte("alpha"))
	e.writeLocal("tree/sub/b.txt", []byte("beta"))
	e.mustRun("put", "-r", e.localPath("tree"), e.remotePath("tree"))

	dest := e.localPath("walked")
	e.mustRun("get", "-r", "--no-archive", e.remotePath("tree"), dest)

	got, err := os.ReadFile(filepath.Join(dest, "sub", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "beta" {
		t.Errorf("content = %q", got)
	}
}

func TestPutSkipsExistingWithoutForce(t *testing.T) {
	e := setup(t)
	local := e.writeLocal("a.txt", []byte("first"))
	e.mustRun("put", local, e.remotePath("a.txt"))

	e.writeLocal("a.txt", []byte("second"))
	e.mustRun("put", local, e.remotePath("a.txt"))
	if out := e.mustRun("cat", e.remotePath("a.txt")); out != "first" {
		t.Errorf("the existing file was overwritten without --force: %q", out)
	}

	e.mustRun("put", "--force", local, e.remotePath("a.txt"))
	if out := e.mustRun("cat", e.remotePath("a.txt")); out != "second" {
		t.Errorf("--force did not overwrite: %q", out)
	}
}

func TestPutDryRunWritesNothing(t *testing.T) {
	e := setup(t)
	local := e.writeLocal("a.txt", []byte("dry"))

	e.mustRun("put", "--dry-run", local, e.remotePath("a.txt"))
	if _, _, code := e.run("stat", e.remotePath("a.txt")); code != cberr.ExitNotFound {
		t.Errorf("a dry run created the file (stat exited %d)", code)
	}
}

// TestCpRefusesAmbiguousPaths is the lxplus trap, checked against the real
// binary: two unmarked paths must be refused rather than guessed at.
func TestCpRefusesAmbiguousPaths(t *testing.T) {
	e := setup(t)
	_, stderr, code := e.run("cp", e.remotePath("a.txt"), e.remotePath("b.txt"))

	if code != cberr.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, cberr.ExitUsage)
	}
	if !strings.Contains(stderr, "cb:") {
		t.Errorf("the error should suggest the cb: form:\n%s", stderr)
	}
}

func TestUnicodeAndSpacesInNames(t *testing.T) {
	e := setup(t)
	name := "my notes #1 – ümlaut.txt"
	local := e.writeLocal("weird.txt", []byte("odd name"))

	e.mustRun("put", local, e.remotePath(name))
	if out := e.mustRun("cat", e.remotePath(name)); out != "odd name" {
		t.Errorf("cat = %q", out)
	}

	names := e.names(e.remote)
	found := false
	for _, n := range names {
		if n == name {
			found = true
		}
	}
	if !found {
		t.Errorf("listing shows %v, want a file named %q", names, name)
	}
}

// ── sharing ──────────────────────────────────────────────────────────────────

func TestShareLifecycle(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("shared.txt", []byte("share me")), e.remotePath("shared.txt"))
	target := e.remotePath("shared.txt")

	var created []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	e.runJSON(&created, "share", "create", target, "--with", "marie", "--role", "editor")
	if len(created) == 0 {
		t.Fatal("share create returned nothing")
	}
	shareID := created[0].ID
	if created[0].Role != "editor" {
		t.Errorf("role = %q, want editor", created[0].Role)
	}

	var listed []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	e.runJSON(&listed, "share", "list", target)
	found := false
	for _, s := range listed {
		if s.ID == shareID {
			found = true
		}
	}
	if !found {
		t.Errorf("the new share is not in the listing: %+v", listed)
	}

	e.mustRun("share", "update", target, shareID, "--role", "viewer")
	e.mustRun("share", "remove", target, shareID)

	e.runJSON(&listed, "share", "list", target)
	for _, s := range listed {
		if s.ID == shareID {
			t.Errorf("the share survived removal: %+v", listed)
		}
	}
}

func TestPublicLinkLifecycle(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("linked.txt", []byte("link me")), e.remotePath("linked.txt"))
	target := e.remotePath("linked.txt")

	var created struct {
		ID   string `json:"id"`
		Link *struct {
			URL string `json:"url"`
		} `json:"link"`
	}
	e.runJSONOne(&created, "link", "create", target, "--role", "viewer")
	if created.ID == "" {
		t.Fatal("link create returned no id")
	}
	if created.Link == nil || created.Link.URL == "" {
		t.Errorf("link create returned no URL: %+v", created)
	}

	var links []struct {
		ID string `json:"id"`
	}
	e.runJSON(&links, "link", "list", target)
	found := false
	for _, l := range links {
		if l.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("the new link is not in the listing: %+v", links)
	}

	e.mustRun("link", "remove", target, created.ID)
}

// ── output contract ──────────────────────────────────────────────────────────

// TestJSONOutputIsCleanOnStdout is the scripting contract: everything
// informational goes to stderr, so a pipe sees only parseable data.
func TestJSONOutputIsCleanOnStdout(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("a.txt", []byte("x")), e.remotePath("a.txt"))

	stdout, _, code := e.run("--output", "json", "ls", e.remote)
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	trimmed := strings.TrimSpace(stdout)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		t.Errorf("stdout is not a bare JSON array:\n%s", stdout)
	}
}

func TestQuietSuppressesChatter(t *testing.T) {
	e := setup(t)
	local := e.writeLocal("a.txt", []byte("x"))

	_, stderr, code := e.run("-q", "put", local, e.remotePath("a.txt"))
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr)
	}
	if strings.Contains(stderr, "Uploaded") {
		t.Errorf("--quiet should suppress the summary:\n%s", stderr)
	}
}

func TestVersionWorksWithoutCredentials(t *testing.T) {
	e := setup(t)

	c := e.cmd("version")
	c.Env = append(os.Environ(), "CERNBOX_USERNAME=", "CERNBOX_PASSWORD=")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("version failed without credentials: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Version") {
		t.Errorf("version output:\n%s", out)
	}
}

// TestBadCredentialsExitThree distinguishes the retryable case from the
// permanent one, which is what scripts branch on.
func TestBadCredentialsExitThree(t *testing.T) {
	e := setup(t)

	c := e.cmd("ls", homeRoot)
	c.Env = append(os.Environ(),
		"CERNBOX_USERNAME="+username,
		"CERNBOX_PASSWORD=definitely-not-the-password",
		"CERNBOX_TOKEN_CACHE="+filepath.Join(e.cacheDir, "bad-tokens"),
		"CERNBOX_CONFIG="+filepath.Join(e.cacheDir, "absent.yaml"),
		// Without the dev CA the run fails on the certificate instead, which
		// is a different error and a different exit code — and the point here
		// is precisely which code a rejected password produces.
		"SSL_CERT_FILE="+devCACert(),
	)
	out, err := c.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a failure, got:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !asExitError(err, &exitErr) {
		t.Fatalf("unexpected error type: %v", err)
	}
	if exitErr.ExitCode() != cberr.ExitAuth {
		t.Errorf("exit code = %d, want %d for bad credentials\n%s", exitErr.ExitCode(), cberr.ExitAuth, out)
	}
}
