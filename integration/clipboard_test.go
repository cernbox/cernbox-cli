//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
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
	if !strings.Contains(stderr, "nothing was transferred") {
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

// TestClipboardStreamsAPipeWithoutTouchingLocalDisk is the point of streaming: a
// pipe of unknown length reaches CERNBox without being spooled to a temporary
// file first, which is what let it need as much free local disk as the stream was
// large.
//
// The chunk is set small so the split path runs on a stream a test can afford,
// and the payload is position-dependent so a dropped or misordered piece shows up
// as wrong content rather than as a length that happens to match.
func TestClipboardStreamsAPipeWithoutTouchingLocalDisk(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	cfg := e.writeLocal("small-chunk.yaml", []byte("transfer:\n  chunk_size: 64K\n"))
	body := streamBody(300 << 10) // a little under five chunks

	// TMPDIR points at a directory that does not exist, so anything that tried to
	// spool the stream to disk would fail rather than quietly succeed. That is the
	// assertion: the bytes never touch the filesystem.
	cmd := e.cmd("--config", cfg, "copy", "--slot", slot, "-", "--name", "piped.bin")
	cmd.Env = append(cmd.Env, "TMPDIR="+filepath.Join(e.localDir, "no-such-dir"))
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("streaming a pipe failed: %v\n%s", err, out)
	}

	var listed []clipManifest
	e.runJSON(&listed, "clipboard", "list")
	entry, ok := entryOf(listed, slot, "piped.bin")
	if !ok {
		t.Fatalf("the stream is not on the clipboard: %+v", listed)
	}
	if entry.Parts != 5 {
		t.Errorf("Parts = %d, want 5 for %d bytes at 64K", entry.Parts, len(body))
	}
	if entry.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", entry.Size, len(body))
	}

	// And it comes back byte for byte, through a pipe on the way out too.
	got := e.mustRun("paste", "--slot", slot, "-")
	if got != body {
		t.Errorf("the stream came back as %d bytes, want %d", len(got), len(body))
	}
	if sha256hex([]byte(got)) != sha256hex([]byte(body)) {
		t.Error("the stream came back with the right length but the wrong contents")
	}
}

// TestClipboardStreamJoinsIntoAFile: the same stream, pasted to a path rather than
// a pipe.
func TestClipboardStreamJoinsIntoAFile(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	cfg := e.writeLocal("small-chunk.yaml", []byte("transfer:\n  chunk_size: 64K\n"))
	body := streamBody(200 << 10)

	cmd := e.cmd("--config", cfg, "copy", "--slot", slot, "-", "--name", "joined.bin")
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("streaming a pipe failed: %v\n%s", err, out)
	}

	dest := t.TempDir()
	e.mustRun("paste", "--slot", slot, dest)

	got, err := os.ReadFile(filepath.Join(dest, "joined.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("the joined file is %d bytes, want %d", len(got), len(body))
	}
	if _, err := os.Stat(filepath.Join(dest, "joined.bin.part")); err == nil {
		t.Error("the temporary join file was left behind")
	}
}

// TestClipboardStreamPastesInsideCERNBox: a streamed entry cannot be a server-side
// COPY, because nothing in WebDAV joins objects. It is pulled and pushed back
// through a pipe instead, so the result has to be identical even though this is the
// one paste inside CERNBox that moves data.
func TestClipboardStreamPastesInsideCERNBox(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	cfg := e.writeLocal("small-chunk.yaml", []byte("transfer:\n  chunk_size: 64K\n"))
	body := streamBody(150 << 10)

	cmd := e.cmd("--config", cfg, "copy", "--slot", slot, "-", "--name", "rebuilt.bin")
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("streaming a pipe failed: %v\n%s", err, out)
	}

	landing := e.remotePath("landing")
	e.mustRun("mkdir", landing)
	e.mustRun("paste", "--slot", slot, "cb:"+landing+"/")

	if got := e.mustRun("cat", landing+"/rebuilt.bin"); got != body {
		t.Errorf("the rebuilt file is %d bytes, want %d", len(got), len(body))
	}
	if info := e.stat(landing + "/rebuilt.bin"); info.Size != int64(len(body)) {
		t.Errorf("the server reports %d bytes, want %d", info.Size, len(body))
	}
}

// TestClipboardClearReleasesEveryPieceOfAStream: the pieces are a directory, so one
// recursive delete should take all of them.
func TestClipboardClearReleasesEveryPieceOfAStream(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)

	cfg := e.writeLocal("small-chunk.yaml", []byte("transfer:\n  chunk_size: 64K\n"))
	cmd := e.cmd("--config", cfg, "copy", "--slot", slot, "-", "--name", "doomed.bin")
	cmd.Stdin = strings.NewReader(streamBody(200 << 10))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("streaming a pipe failed: %v\n%s", err, out)
	}

	var listed []clipManifest
	e.runJSON(&listed, "clipboard", "list")
	base := stagedPathOf(listed, slot, "doomed.bin")
	if base == "" {
		t.Fatal("the stream is not on the clipboard")
	}
	if _, _, code := e.run("stat", base); code != 0 {
		t.Fatalf("the pieces are not at %s", base)
	}

	e.mustRun("clipboard", "clear", slot)
	if _, _, code := e.run("stat", base); code == 0 {
		t.Errorf("the pieces survived the clear at %s", base)
	}
}

// TestClipboardHandsOverLiveBetweenTwoProcesses is the live handover with two real
// processes and a real server: one blocks holding the source, the other arrives,
// the bytes move, and nothing is left behind.
//
// This is the test the feature exists for. Nothing smaller can show it, because
// the whole mechanism is two processes signalling each other through files on a
// server that offers no notification of any kind.
func TestClipboardHandsOverLiveBetweenTwoProcesses(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	cfg := e.writeLocal("small-chunk.yaml", []byte("transfer:\n  chunk_size: 64K\n"))
	body := streamBody(512 << 10) // eight chunks through a window of four

	// The sender has the whole stream ready on its standard input and must still
	// not move a byte of it until somebody pastes.
	sender := e.cmd("--config", cfg, "copy", "--stream", "--slot", slot, "-",
		"--name", "live.bin", "--wait", "60s")
	sender.Stdin = strings.NewReader(body)
	var senderLog strings.Builder
	sender.Stdout = &senderLog
	sender.Stderr = &senderLog
	if err := sender.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Process.Kill() }()

	// Give it long enough that an implementation which just uploaded everything
	// would have finished, then check it is still sitting there waiting.
	time.Sleep(3 * time.Second)
	if sender.ProcessState != nil {
		t.Fatalf("the sender exited before anybody pasted:\n%s", senderLog.String())
	}
	var waiting []clipManifest
	e.runJSON(&waiting, "clipboard", "list")
	if m, ok := manifestOf(waiting, slot); !ok {
		t.Fatalf("the waiting sender published no slot: %+v", waiting)
	} else if m.Mode != "stream" {
		t.Errorf("the slot says mode %q, want stream", m.Mode)
	}

	// Now the other machine turns up.
	got := e.mustRun("--config", cfg, "paste", "--slot", slot, "--wait", "60s", "-")

	if err := sender.Wait(); err != nil {
		t.Fatalf("the sender failed: %v\n%s", err, senderLog.String())
	}
	if got != body {
		t.Errorf("the receiver got %d bytes, want %d", len(got), len(body))
	}
	if sha256hex([]byte(got)) != sha256hex([]byte(body)) {
		t.Error("the handover delivered the right length but the wrong bytes")
	}
	if !strings.Contains(senderLog.String(), "Connected.") {
		t.Errorf("the sender should report the receiver arriving:\n%s", senderLog.String())
	}

	// A handover stores nothing: the slot is gone, so there is no quota to reclaim
	// and nothing to clear.
	var after []clipManifest
	e.runJSON(&after, "clipboard", "list")
	if _, ok := manifestOf(after, slot); ok {
		t.Errorf("the handover left the slot behind: %+v", after)
	}
}

// TestClipboardHandsOverLiveToAFile: the receiving end can be a path as well as a
// pipe, and an interrupted handover must not leave a plausible partial file.
func TestClipboardHandsOverLiveToAFile(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	cfg := e.writeLocal("small-chunk.yaml", []byte("transfer:\n  chunk_size: 64K\n"))
	body := streamBody(200 << 10)

	sender := e.cmd("--config", cfg, "copy", "--stream", "--slot", slot, "-",
		"--name", "landed.bin", "--wait", "60s")
	sender.Stdin = strings.NewReader(body)
	var senderLog strings.Builder
	sender.Stdout = &senderLog
	sender.Stderr = &senderLog
	if err := sender.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Process.Kill() }()

	// The sender publishes its slot a moment after starting, and a real user types
	// the second command later still. Waiting for it keeps the test about the
	// handover rather than about who won a startup race.
	e.waitForSlot(slot)

	dest := t.TempDir()
	e.mustRun("--config", cfg, "paste", "--slot", slot, "--wait", "60s", dest)
	if err := sender.Wait(); err != nil {
		t.Fatalf("the sender failed: %v\n%s", err, senderLog.String())
	}

	got, err := os.ReadFile(filepath.Join(dest, "landed.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("the file holds %d bytes, want %d", len(got), len(body))
	}
	if _, err := os.Stat(filepath.Join(dest, "landed.bin.part")); err == nil {
		t.Error("the partial file was left behind")
	}
}

// TestClipboardStreamSenderGivesUpAndCleansUp: copy waits, but not for ever, and a
// sender that gives up must not leave a slot nobody will ever collect.
func TestClipboardStreamSenderGivesUpAndCleansUp(t *testing.T) {
	e := setup(t)
	slot := clipSlot(t)
	e.clearSlot(slot)

	cmd := e.cmd("copy", "--stream", "--slot", slot, "-", "--wait", "2s")
	cmd.Stdin = strings.NewReader("nobody is coming")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the sender should give up when nobody pastes:\n%s", out)
	}
	if !strings.Contains(string(out), "nobody pasted") {
		t.Errorf("the error should say what it waited for:\n%s", out)
	}

	var after []clipManifest
	e.runJSON(&after, "clipboard", "list")
	if _, ok := manifestOf(after, slot); ok {
		t.Errorf("the abandoned handover left its slot behind: %+v", after)
	}
}

// TestClipboardStreamRefusesARemoteSource: a file already on the server has a
// strictly better path, so streaming it is refused rather than quietly wasteful.
func TestClipboardStreamRefusesARemoteSource(t *testing.T) {
	e := setup(t)

	remote := e.remotePath("already.txt")
	e.mustRun("put", e.writeLocal("already.txt", []byte("on the server")), remote)

	_, stderr, code := e.run("copy", "--stream", "--slot", clipSlot(t), "cb:"+remote)
	if code == 0 {
		t.Fatal("streaming something already in CERNBox should be refused")
	}
	if !strings.Contains(stderr, "already in CERNBox") {
		t.Errorf("the error should explain why:\n%s", stderr)
	}
}

// waitForSlot blocks until a streaming sender has published its slot.
func (e *env) waitForSlot(slot string) {
	e.t.Helper()
	e.waitFor("the sender to publish the "+slot+" slot", func() bool {
		out, _, code := e.run("--output", "json", "clipboard", "list")
		if code != 0 {
			return false
		}
		var listed []clipManifest
		if json.Unmarshal([]byte(out), &listed) != nil {
			return false
		}
		_, ok := manifestOf(listed, slot)
		return ok
	})
}

// manifestOf finds a slot in a listing.
func manifestOf(listed []clipManifest, slot string) (clipManifest, bool) {
	for _, m := range listed {
		if m.Slot == slot {
			return m, true
		}
	}
	return clipManifest{}, false
}

// streamBody is content whose every offset is identifiable, so a piece joined out
// of order fails on content rather than passing on length.
func streamBody(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%08d-", i)
	}
	return b.String()[:n]
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

// entryOf finds a named entry in a listing.
func entryOf(listed []clipManifest, slot, name string) (clipEntry, bool) {
	for _, m := range listed {
		if m.Slot != slot {
			continue
		}
		for _, e := range m.Entries {
			if e.Name == name {
				return e, true
			}
		}
	}
	return clipEntry{}, false
}

// stagedPathOf finds where a listing says a named entry's bytes were uploaded,
// which is the path that has to stop existing when the slot is replaced.
func stagedPathOf(listed []clipManifest, slot, name string) string {
	if e, ok := entryOf(listed, slot, name); ok && e.Staged {
		return e.Path
	}
	return ""
}

// clipManifest mirrors the JSON shape of a clipboard manifest, so the tests read
// the same thing a script would.
type clipManifest struct {
	Version int    `json:"version"`
	Slot    string `json:"slot"`
	// Mode is "stream" for a live handover and absent for a stored copy.
	Mode   string `json:"mode"`
	Origin struct {
		Host string `json:"host"`
		User string `json:"user"`
		Dir  string `json:"dir"`
	} `json:"origin"`
	Entries []clipEntry `json:"entries"`
}

// clipEntry is one item on the clipboard, as a script would read it.
type clipEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	IsDir  bool   `json:"is_dir"`
	Size   int64  `json:"size"`
	Staged bool   `json:"staged"`
	// Parts is non-zero when the entry was streamed in and stored as pieces.
	Parts int `json:"parts"`
}
