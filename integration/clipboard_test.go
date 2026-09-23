//go:build integration

package integration_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The cross-machine clipboard, against a real reva.
//
// A fake HTTP server cannot tell whether this actually works, because the whole
// mechanism is state kept on the server: a manifest written by one invocation and
// read back by the next, payloads in the user's own home space, and a server-side
// COPY for the case where no data should move at all. Each of those is a real
// WebDAV behaviour, so each is checked here.
//
// There is only one machine in these tests, but that is not the thing under test:
// what matters is that nothing but CERNBox carries state between one command and
// the next, which is exactly what makes two machines work.

// clipSlot gives each test its own slot, so tests can run in parallel against the
// same account without pasting each other's files.
func clipSlot(t *testing.T) string {
	t.Helper()
	return "it-" + randHex()
}

// clearSlot removes a slot at the end of a test. It has to happen even when the
// test fails: a staged payload left behind is quota that nothing will reclaim
// until it expires a week later.
func (e *env) clearSlot(slot string) {
	e.t.Helper()
	e.t.Cleanup(func() {
		e.run("clipboard", "clear", slot)
	})
}

// TestClipboardRoundTrip is the feature in one test: copy on one side, paste on
// the other, with nothing but CERNBox in between.
func TestClipboardRoundTrip(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	local := e.writeLocal("clip.txt", []byte("carried by cernbox"))
	e.mustRun("copy", "--slot", slot, local)

	// A separate directory stands in for the second machine: the CLI is given
	// nothing but the slot name to find the file again.
	dest := t.TempDir()
	e.mustRun("paste", "--slot", slot, dest)

	got, err := os.ReadFile(filepath.Join(dest, "clip.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "carried by cernbox" {
		t.Errorf("pasted %q", got)
	}
}

// TestClipboardKeepsTheCopyAfterPasting is the decision this was built around:
// paste is not a move, so the same copy reaches every machine.
func TestClipboardKeepsTheCopyAfterPasting(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	local := e.writeLocal("shared.txt", []byte("to several machines"))
	e.mustRun("copy", "--slot", slot, local)

	for _, dest := range []string{t.TempDir(), t.TempDir(), t.TempDir()} {
		e.mustRun("paste", "--slot", slot, dest)
		got, err := os.ReadFile(filepath.Join(dest, "shared.txt"))
		if err != nil {
			t.Fatalf("pasting again failed: %v", err)
		}
		if string(got) != "to several machines" {
			t.Errorf("pasted %q", got)
		}
	}
}

// TestClipboardReferencesARemotePathWithoutUploading: a file already in CERNBox
// is pointed at, not duplicated. This is what makes the lxplus case free.
func TestClipboardReferencesARemotePathWithoutUploading(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	local := e.writeLocal("origin.txt", []byte("already on the server"))
	remote := e.remotePath("origin.txt")
	e.mustRun("put", local, remote)

	var manifest clipManifest
	e.runJSON(&manifest, "copy", "--slot", slot, "cb:"+remote)

	if len(manifest.Entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(manifest.Entries), manifest.Entries)
	}
	entry := manifest.Entries[0]
	if entry.Staged {
		t.Error("a path already in CERNBox must not be uploaded again")
	}
	if entry.Path != remote {
		t.Errorf("the clipboard points at %q, want the original %q", entry.Path, remote)
	}

	// And it is still usable: the reference resolves on paste.
	dest := t.TempDir()
	e.mustRun("paste", "--slot", slot, dest)
	if got, err := os.ReadFile(filepath.Join(dest, "origin.txt")); err != nil || string(got) != "already on the server" {
		t.Errorf("pasting a reference gave %q, %v", got, err)
	}
}

// TestClipboardPasteInsideCERNBoxMovesNoData: with both ends on the server, the
// paste is a WebDAV COPY and the bytes never reach the client.
func TestClipboardPasteInsideCERNBoxMovesNoData(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	local := e.writeLocal("server-side.txt", payload(64<<10))
	source := e.remotePath("server-side.txt")
	e.mustRun("put", local, source)
	e.mustRun("copy", "--slot", slot, "cb:"+source)

	destDir := e.remotePath("pasted")
	e.mustRun("mkdir", destDir)
	stdout, stderr, code := e.run("paste", "--slot", slot, "cb:"+destDir+"/")
	if code != 0 {
		t.Fatalf("paste exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "no data crossed the wire") {
		t.Errorf("a paste inside CERNBox should report itself as server-side:\n%s", stderr)
	}

	// The copy really is there, with the right contents.
	pasted := e.mustRun("cat", destDir+"/server-side.txt")
	if pasted != string(payload(64<<10)) {
		t.Errorf("the server-side copy is %d bytes, want %d", len(pasted), 64<<10)
	}
}

// TestClipboardCarriesADirectory: -r on the way in, whole tree on the way out.
func TestClipboardCarriesADirectory(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	e.writeLocal("tree/one.txt", []byte("one"))
	e.writeLocal("tree/sub/two.txt", []byte("two"))
	e.mustRun("copy", "-r", "--slot", slot, e.localPath("tree"))

	dest := t.TempDir()
	e.mustRun("paste", "--slot", slot, dest)

	for rel, want := range map[string]string{
		"tree/one.txt":     "one",
		"tree/sub/two.txt": "two",
	} {
		got, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

func TestClipboardRefusesADirectoryWithoutRecursive(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	e.writeLocal("nodir/file.txt", []byte("x"))
	_, stderr, code := e.run("copy", "--slot", slot, e.localPath("nodir"))
	if code == 0 {
		t.Fatal("copying a directory without -r should fail, as cp does")
	}
	if !strings.Contains(stderr, "-r") {
		t.Errorf("the error should name the flag:\n%s", stderr)
	}
}

// TestClipboardPipe: the cross-machine pipe, with nothing touching either
// filesystem. This is the form that needs a real server most, because the upload
// has to carry a Content-Length that reva will accept.
func TestClipboardPipe(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	body := payload(4096)
	cmd := e.cmd("copy", "--slot", slot, "-", "--name", "piped.bin")
	cmd.Stdin = strings.NewReader(string(body))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("copy - failed: %v\n%s", err, out)
	}

	got := e.mustRun("paste", "--slot", slot, "-")
	if got != string(body) {
		t.Errorf("the pipe carried %d bytes, want %d", len(got), len(body))
	}
}

// TestClipboardRefusesToOverwriteWithoutForce: the destination is somebody's
// existing file, and this CLI does not clobber one silently.
func TestClipboardRefusesToOverwriteWithoutForce(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	local := e.writeLocal("collide.txt", []byte("new contents"))
	e.mustRun("copy", "--slot", slot, local)

	dest := t.TempDir()
	existing := filepath.Join(dest, "collide.txt")
	if err := os.WriteFile(existing, []byte("old contents"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := e.run("paste", "--slot", slot, dest)
	if code == 0 {
		t.Fatal("paste should refuse to overwrite without -f")
	}
	if !strings.Contains(stderr, "-f") {
		t.Errorf("the error should name the flag:\n%s", stderr)
	}
	if got, _ := os.ReadFile(existing); string(got) != "old contents" {
		t.Errorf("the existing file was replaced anyway: %q", got)
	}

	e.mustRun("paste", "-f", "--slot", slot, dest)
	if got, _ := os.ReadFile(existing); string(got) != "new contents" {
		t.Errorf("-f did not overwrite: %q", got)
	}
}

func TestClipboardListShowsTheSlot(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	local := e.writeLocal("listed.txt", []byte("on the clipboard"))
	e.mustRun("copy", "--slot", slot, local)

	out := e.mustRun("clipboard", "list")
	if !strings.Contains(out, slot) {
		t.Errorf("the listing does not mention the slot %q:\n%s", slot, out)
	}
	if !strings.Contains(out, "listed.txt") {
		t.Errorf("the listing does not say what is in it:\n%s", out)
	}

	var manifests []clipManifest
	e.runJSON(&manifests, "clipboard", "list")
	found := false
	for _, m := range manifests {
		if m.Slot == slot {
			found = true
			if len(m.Entries) != 1 || m.Entries[0].Name != "listed.txt" {
				t.Errorf("slot %q holds %+v", slot, m.Entries)
			}
			if m.Origin.Host == "" {
				t.Error("the copy should record which machine made it")
			}
		}
	}
	if !found {
		t.Errorf("the slot is missing from the JSON listing: %+v", manifests)
	}
}

// TestClipboardClearReleasesStagedBytes: what was uploaded for the clipboard goes
// away, and the manifest with it.
func TestClipboardClearReleasesStagedBytes(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)

	local := e.writeLocal("temporary.txt", []byte("staged for the clipboard"))
	e.mustRun("copy", "--slot", slot, local)

	stdout, stderr, code := e.run("clipboard", "clear", slot)
	if code != 0 {
		t.Fatalf("clear exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "Cleared") {
		t.Errorf("clear should say what it did:\n%s", stderr)
	}

	// Pasting from it now fails, because there is nothing there.
	if _, _, code := e.run("paste", "--slot", slot, t.TempDir()); code == 0 {
		t.Error("pasting from a cleared slot should fail")
	}
	if out := e.mustRun("clipboard", "list"); strings.Contains(out, slot) {
		t.Errorf("the cleared slot is still listed:\n%s", out)
	}
}

// TestClipboardClearLeavesReferencedFilesAlone: a referenced copy points at the
// user's real file, and clearing the clipboard must not be a way to delete it.
func TestClipboardClearLeavesTheOriginalAlone(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)

	local := e.writeLocal("precious.txt", []byte("do not delete me"))
	remote := e.remotePath("precious.txt")
	e.mustRun("put", local, remote)
	e.mustRun("copy", "--slot", slot, "cb:"+remote)
	e.mustRun("clipboard", "clear", slot)

	if got := e.mustRun("cat", remote); got != "do not delete me" {
		t.Errorf("clearing the clipboard damaged the referenced file: %q", got)
	}
}

func TestClipboardPasteWithNothingCopied(t *testing.T) {
	e := setup(t)

	_, stderr, code := e.run("paste", "--slot", clipSlot(t), t.TempDir())
	if code == 0 {
		t.Fatal("pasting from a slot that was never written should fail")
	}
	if !strings.Contains(stderr, "copied") {
		t.Errorf("the error should say nothing has been copied to it:\n%s", stderr)
	}
}

// TestClipboardSlotsAreIndependent: named slots are what make it safe to have
// more than one copy in flight, so they must not see each other.
func TestClipboardSlotsAreIndependent(t *testing.T) {
	e := setup(t)
	first, second := clipSlot(t), clipSlot(t)
	e.clearSlot(first)
	e.clearSlot(second)

	e.mustRun("copy", "--slot", first, e.writeLocal("first.txt", []byte("one")))
	e.mustRun("copy", "--slot", second, e.writeLocal("second.txt", []byte("two")))

	firstDest, secondDest := t.TempDir(), t.TempDir()
	e.mustRun("paste", "--slot", first, firstDest)
	e.mustRun("paste", "--slot", second, secondDest)

	if got, _ := os.ReadFile(filepath.Join(firstDest, "first.txt")); string(got) != "one" {
		t.Errorf("the first slot pasted %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(secondDest, "second.txt")); string(got) != "two" {
		t.Errorf("the second slot pasted %q", got)
	}
	if _, err := os.Stat(filepath.Join(firstDest, "second.txt")); err == nil {
		t.Error("the slots are not independent")
	}
}

// TestClipboardStaysOutOfOrdinaryListings: the clipboard lives in the user's home
// space, so a user who never touches the feature should never see it.
func TestClipboardStaysOutOfOrdinaryListings(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	e.mustRun("copy", "--slot", slot, e.writeLocal("hidden.txt", []byte("x")))

	for _, name := range e.names("home:") {
		if name == ".cernbox" {
			t.Error("the clipboard directory shows up in a plain listing; it should need -a")
		}
	}
	// With -a it is there, because hiding it from the user entirely would be
	// worse than keeping it out of the way.
	var withAll []entry
	e.runJSON(&withAll, "ls", "-a", "home:")
	found := false
	for _, it := range withAll {
		if it.Name == ".cernbox" {
			found = true
		}
	}
	if !found {
		t.Error("ls -a should show the clipboard directory")
	}
}

// TestClipboardReplacesASlotWithoutLeakingTheOldCopy: copying over a slot has to
// release what the previous copy staged, or a clipboard used daily becomes a slow
// quota leak. The check is that the space comes back, not merely that the manifest
// changed.
func TestClipboardReplacesASlotWithoutLeakingTheOldCopy(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	// A payload big enough that its presence or absence is unambiguous.
	e.mustRun("copy", "--slot", slot, e.writeLocal("big.bin", payload(512<<10)))

	var listed []clipManifest
	e.runJSON(&listed, "clipboard", "list")
	stagedPath := stagedPathOf(listed, slot, "big.bin")
	if stagedPath == "" {
		t.Fatalf("the first copy staged nothing for slot %q: %+v", slot, listed)
	}
	if _, _, code := e.run("stat", stagedPath); code != 0 {
		t.Fatalf("the staged payload is not at %s", stagedPath)
	}

	e.mustRun("copy", "--slot", slot, e.writeLocal("small.txt", []byte("replacement")))

	// The old payload is gone, and the new copy is what pastes.
	if _, _, code := e.run("stat", stagedPath); code == 0 {
		t.Errorf("replacing the slot left the previous payload at %s", stagedPath)
	}
	dest := t.TempDir()
	e.mustRun("paste", "--slot", slot, dest)
	if got, err := os.ReadFile(filepath.Join(dest, "small.txt")); err != nil || string(got) != "replacement" {
		t.Errorf("pasted %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "big.bin")); err == nil {
		t.Error("the replaced copy is still on the clipboard")
	}
}

// TestClipboardManifestIsReplacedConditionally checks against a real reva that the
// precondition the concurrency guard rests on is actually enforced. If reva ever
// stops honouring If-Match on PUT the guard becomes a no-op, and this is what
// would say so.
func TestClipboardManifestIsReplacedConditionally(t *testing.T) {
	e := setup(t)

	remote := e.remotePath("precondition.txt")
	e.mustRun("put", e.writeLocal("precondition.txt", []byte("original")), remote)
	info := e.stat(remote)
	if info.ETag == "" {
		t.Fatal("the server reported no ETag, so there is nothing to be conditional on")
	}

	url := endpoint + "/remote.php/dav/files/" + username + remote
	client := &http.Client{Timeout: 10 * time.Second, Transport: devTransport()}

	// A PUT carrying the wrong ETag has to be refused and change nothing.
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader("clobbered"))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("If-Match", `"definitely-not-the-etag"`)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("PUT with a stale If-Match answered %d, want 412: the clipboard's "+
			"concurrency guard depends on this", resp.StatusCode)
	}
	if got := e.mustRun("cat", remote); got != "original" {
		t.Errorf("the file was modified despite the failed precondition: %q", got)
	}

	// And one carrying the right ETag has to go through.
	req, err = http.NewRequest(http.MethodPut, url, strings.NewReader("updated"))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("If-Match", `"`+info.ETag+`"`)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("PUT with a matching If-Match answered %d", resp.StatusCode)
	}
	if got := e.mustRun("cat", remote); got != "updated" {
		t.Errorf("the matching precondition did not let the write through: %q", got)
	}
}

// stagedPathOf finds where a listing says a named entry's bytes were uploaded,
// which is the path that has to stop existing when the slot is replaced.
func stagedPathOf(listed []clipManifest, slot, name string) string {
	for _, m := range listed {
		if m.Slot != slot {
			continue
		}
		for _, e := range m.Entries {
			if e.Name == name && e.Staged {
				return e.Path
			}
		}
	}
	return ""
}

// clipManifest mirrors the JSON shape of a clipboard manifest, so the tests read
// the same thing a script would.
type clipManifest struct {
	Version int    `json:"version"`
	Slot    string `json:"slot"`
	Origin  struct {
		Host string `json:"host"`
		User string `json:"user"`
		Dir  string `json:"dir"`
	} `json:"origin"`
	Entries []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		IsDir  bool   `json:"is_dir"`
		Size   int64  `json:"size"`
		Staged bool   `json:"staged"`
	} `json:"entries"`
}
