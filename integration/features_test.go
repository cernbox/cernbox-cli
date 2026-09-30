//go:build integration

package integration_test

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// ── trash ────────────────────────────────────────────────────────────────────

func TestTrashRoundTrip(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("doomed.txt", []byte("delete me")), e.remotePath("doomed.txt"))
	e.mustRun("rm", e.remotePath("doomed.txt"))

	var items []struct {
		Key          string `json:"key"`
		Name         string `json:"name"`
		OriginalPath string `json:"original_path"`
	}
	e.runJSON(&items, "trash", "list")

	// The bin outlives the test, so previous runs have left their own
	// doomed.txt in it. Matching on the name alone picks one whose directory
	// was cleaned up long ago, and restoring that fails for want of a parent.
	mine := path.Base(e.remote)
	key := ""
	for _, it := range items {
		if it.Name == "doomed.txt" && strings.Contains(it.OriginalPath, mine) {
			key = it.Key
		}
	}
	if key == "" {
		t.Fatalf("this run's deleted file is not in the trash bin (looking for %s): %+v", mine, items)
	}

	e.mustRun("trash", "restore", key)
	if out := e.mustRun("cat", e.remotePath("doomed.txt")); out != "delete me" {
		t.Errorf("the restored file reads %q", out)
	}
}

// TestTrashListSince checks the one thing only a real server can settle: that it
// accepts the range and answers with it. ocdav discards a range it cannot parse
// and substitutes its own two days, so a wrong layout does not fail — it returns
// a plausible listing that ignores --since. A window wider than the storage will
// take in one request has to come back split rather than refused.
func TestTrashListSince(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("doomed.txt", []byte("delete me")), e.remotePath("doomed.txt"))
	e.mustRun("rm", e.remotePath("doomed.txt"))

	type item struct {
		Key          string `json:"key"`
		Name         string `json:"name"`
		OriginalPath string `json:"original_path"`
	}
	var recent, wide []item
	e.runJSON(&recent, "trash", "list")
	// 60 days is wider than max_days_in_recycle_list, so this only succeeds if
	// the client split it.
	e.runJSON(&wide, "trash", "list", "--since", "60d")

	if len(wide) < len(recent) {
		t.Errorf("--since 60d returned %d items, fewer than the default window's %d",
			len(wide), len(recent))
	}
	mine := path.Base(e.remote)
	found := false
	for _, it := range wide {
		if it.Name == "doomed.txt" && strings.Contains(it.OriginalPath, mine) {
			found = true
		}
	}
	if !found {
		t.Errorf("the file deleted by this run is missing from a 60-day listing: %+v", wide)
	}
}

func TestTrashPurge(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("gone.txt", []byte("x")), e.remotePath("gone.txt"))
	e.mustRun("rm", e.remotePath("gone.txt"))

	var items []struct {
		Key          string `json:"key"`
		Name         string `json:"name"`
		OriginalPath string `json:"original_path"`
	}
	e.runJSON(&items, "trash", "list")

	mine := path.Base(e.remote)
	for _, it := range items {
		if it.Name == "gone.txt" && strings.Contains(it.OriginalPath, mine) {
			e.mustRun("trash", "purge", it.Key)
		}
	}

	e.runJSON(&items, "trash", "list")
	for _, it := range items {
		if it.Name == "gone.txt" {
			t.Error("the purged item is still in the trash bin")
		}
	}
}

// TestTrashBrowseRestoresThroughTheTerminal drives the browser the way a person
// does: in a real terminal, with keystrokes. It is the only test that exercises
// raw mode, the alternate screen and escape-sequence decoding against a live
// server, and the only one that proves the tree it builds from the entries lines
// up with what the storage actually reports.
func TestTrashBrowseRestoresThroughTheTerminal(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("browsed.txt", []byte("bring me back")), e.remotePath("browsed.txt"))
	e.mustRun("rm", e.remotePath("browsed.txt"))

	// Narrow the top level to this run's own directory, open it, restore what the
	// cursor lands on, confirm, quit. The run directory's name is unique, so the
	// filter leaves exactly one row and the navigation is deterministic even
	// though the bin holds everything previous runs deleted.
	mine := path.Base(e.remote)
	keys := "/" + mine + "\r" + "\r" + "r" + "y" + "q"

	out := e.runInPty(keys, "trash", "browse")

	if !strings.Contains(out, "TRASH") {
		t.Fatalf("the browser did not draw a frame:\n%s", out)
	}
	if !strings.Contains(out, "browsed.txt") {
		t.Errorf("the deleted file was not shown:\n%s", out)
	}
	if got := e.mustRun("cat", e.remotePath("browsed.txt")); got != "bring me back" {
		t.Errorf("the file was not restored through the browser; cat gives %q", got)
	}
}

// TestTrashBrowseRefusesWithoutATerminal is the other half: no terminal means no
// browser, because a scheduled job that starts one waits for a keystroke nobody
// will type.
func TestTrashBrowseRefusesWithoutATerminal(t *testing.T) {
	e := setup(t)
	_, stderr, code := e.run("trash", "browse")
	if code != cberr.ExitUsage {
		t.Errorf("exit = %d, want %d with no terminal", code, cberr.ExitUsage)
	}
	if !strings.Contains(stderr, "trash list") {
		t.Errorf("the refusal should point at the listing:\n%s", stderr)
	}
}

// ── edit ─────────────────────────────────────────────────────────────────────

// TestEditRoundTrip checks against a real server the two things the fake cannot
// settle: that the working copy really comes from the server, and that a save
// made while the editor is still running arrives before it exits.
func TestEditRoundTrip(t *testing.T) {
	e := setup(t)
	target := e.remotePath("edited.txt")
	e.mustRun("put", e.writeLocal("edited.txt", []byte("from the server\n")), target)

	// An editor that appends, waits long enough for a save to be noticed, then
	// appends again — so the first version has to reach the server mid-session.
	editor := e.writeLocal("editor.sh", []byte("#!/bin/sh\n"+
		"printf 'first edit\\n' >> \"$1\"\n"+
		"sleep 3\n"+
		"printf 'second edit\\n' >> \"$1\"\n"))
	if err := os.Chmod(editor, 0o700); err != nil {
		t.Fatal(err)
	}

	e.mustRun("edit", target, "--editor", editor, "--interval", "500ms")

	got := e.mustRun("cat", target)
	want := "from the server\nfirst edit\nsecond edit\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}

	// And the file has a version history, which is what proves the mid-session
	// save was a separate write rather than one upload at the end.
	var versions []struct {
		Key string `json:"key"`
	}
	e.runJSON(&versions, "versions", "list", target)
	if len(versions) < 2 {
		t.Errorf("%d versions: the save made while the editor was open did not reach the server separately",
			len(versions))
	}
}

// TestEditBareNameGoesToTheEditFolder: the whole point of "cernbox edit notes.txt".
func TestEditBareNameGoesToTheEditFolder(t *testing.T) {
	e := setup(t)
	// Inside this run's own directory, and deliberately not created first: the
	// folder a bare name lands in has to be made on demand, since a PUT into a
	// missing collection is refused.
	folder := e.remotePath("edit-folder")
	name := "note.txt"

	editor := e.writeLocal("editor2.sh", []byte("#!/bin/sh\nprintf 'written\\n' > \"$1\"\n"))
	if err := os.Chmod(editor, 0o700); err != nil {
		t.Fatal(err)
	}

	e.mustRun("edit", name, "--in", folder, "--editor", editor)

	if got := e.mustRun("cat", folder+"/"+name); got != "written\n" {
		t.Errorf("content = %q", got)
	}
}

// TestEditLocalFileMirrorsItToCERNBox is the other half of edit: the file stays
// on this machine and CERNBox follows it.
func TestEditLocalFileMirrorsItToCERNBox(t *testing.T) {
	e := setup(t)
	local := e.writeLocal("mirrored.txt", []byte("local original\n"))
	folder := e.remotePath("edit-local")

	editor := e.writeLocal("editor3.sh", []byte("#!/bin/sh\n"+
		"printf 'appended\\n' >> \"$1\"\n"))
	if err := os.Chmod(editor, 0o700); err != nil {
		t.Fatal(err)
	}

	e.mustRun("edit", "file:"+local, "--in", folder, "--editor", editor)

	// The local file is still there, with the edit in it.
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("the local file was disturbed: %v", err)
	}
	if string(got) != "local original\nappended\n" {
		t.Errorf("local file = %q", got)
	}
	// And CERNBox has the same thing.
	if remote := e.mustRun("cat", folder+"/mirrored.txt"); remote != string(got) {
		t.Errorf("CERNBox has %q, local has %q", remote, got)
	}

	// Running it again must not need --force: the file already there is this
	// file's own earlier upload, not a collision.
	e.mustRun("edit", "file:"+local, "--in", folder, "--editor", editor)
	if remote := e.mustRun("cat", folder+"/mirrored.txt"); !strings.Contains(remote, "appended\nappended") {
		t.Errorf("the second run did not upload: %q", remote)
	}
}

// ── versions ─────────────────────────────────────────────────────────────────

func TestVersionsRoundTrip(t *testing.T) {
	e := setup(t)
	target := e.remotePath("evolving.txt")

	e.mustRun("put", e.writeLocal("evolving.txt", []byte("first draft")), target)
	e.mustRun("put", "--force", e.writeLocal("evolving.txt", []byte("second draft")), target)

	var versions []struct {
		Key  string `json:"key"`
		Size int64  `json:"size"`
	}
	e.runJSON(&versions, "versions", "list", target)
	if len(versions) == 0 {
		t.Skip("this storage keeps no version history")
	}

	// Downloading a version must not disturb the current one.
	out := e.localPath("old.txt")
	e.mustRun("versions", "download", target, versions[0].Key, "--output-file", out)
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("the version was not written: %v", err)
	}
	if got := e.mustRun("cat", target); got != "second draft" {
		t.Errorf("downloading a version changed the current file: %q", got)
	}

	e.mustRun("versions", "restore", target, versions[0].Key)
	if got := e.mustRun("cat", target); got != "first draft" {
		t.Errorf("after restoring the first version the file reads %q", got)
	}
}

func TestVersionsListRejectsDirectory(t *testing.T) {
	e := setup(t)
	_, _, code := e.run("versions", "list", e.remote)
	if code != cberr.ExitUsage {
		t.Errorf("exit code = %d, want %d: directories have no versions", code, cberr.ExitUsage)
	}
}

// ── sync ─────────────────────────────────────────────────────────────────────

func TestSyncPushAndPull(t *testing.T) {
	e := setup(t)

	e.writeLocal("tree/a.txt", []byte("alpha"))
	e.writeLocal("tree/sub/b.txt", []byte("beta"))
	remote := e.remotePath("mirror")

	e.mustRun("sync", e.localPath("tree"), "cb:"+remote)
	if out := e.mustRun("cat", remote+"/sub/b.txt"); out != "beta" {
		t.Errorf("sync push produced %q", out)
	}

	// A second run must move nothing.
	_, stderr, code := e.run("sync", e.localPath("tree"), "cb:"+remote)
	if code != 0 {
		t.Fatalf("second sync exited %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "0 created, 0 updated") {
		t.Errorf("an unchanged tree should transfer nothing:\n%s", stderr)
	}

	// And pulling it back reproduces the tree.
	back := e.localPath("back")
	e.mustRun("sync", "cb:"+remote, back)
	got, err := os.ReadFile(filepath.Join(back, "sub", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "beta" {
		t.Errorf("sync pull produced %q", got)
	}
}

func TestSyncDelete(t *testing.T) {
	e := setup(t)
	remote := e.remotePath("mirror")

	e.writeLocal("tree/keep.txt", []byte("keep"))
	e.writeLocal("tree/drop.txt", []byte("drop"))
	e.mustRun("sync", e.localPath("tree"), "cb:"+remote)

	if err := os.Remove(e.localPath("tree/drop.txt")); err != nil {
		t.Fatal(err)
	}

	// Without --delete the extra file survives.
	e.mustRun("sync", e.localPath("tree"), "cb:"+remote)
	if _, _, code := e.run("stat", remote+"/drop.txt"); code != 0 {
		t.Error("sync without --delete removed a file")
	}

	e.mustRun("sync", e.localPath("tree"), "cb:"+remote, "--delete")
	if _, _, code := e.run("stat", remote+"/drop.txt"); code != cberr.ExitNotFound {
		t.Error("sync --delete did not remove the extra file")
	}
	if _, _, code := e.run("stat", remote+"/keep.txt"); code != 0 {
		t.Error("sync --delete removed a file that should have been kept")
	}
}

func TestSyncDryRunChangesNothing(t *testing.T) {
	e := setup(t)
	remote := e.remotePath("mirror")

	e.writeLocal("tree/keep.txt", []byte("keep"))
	e.writeLocal("tree/drop.txt", []byte("drop"))
	e.mustRun("sync", e.localPath("tree"), "cb:"+remote)

	if err := os.Remove(e.localPath("tree/drop.txt")); err != nil {
		t.Fatal(err)
	}
	e.writeLocal("tree/new.txt", []byte("new"))

	// The plan is reported in full, and nothing happens. This is the rehearsal
	// worth doing before a --delete run against a path typed by hand.
	_, stderr, code := e.run("sync", e.localPath("tree"), "cb:"+remote, "--delete", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run exited %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "Would mirror") {
		t.Errorf("a dry run should say it is one:\n%s", stderr)
	}
	if !strings.Contains(stderr, "1 created") || !strings.Contains(stderr, "1 deleted") {
		t.Errorf("the dry run did not report the plan:\n%s", stderr)
	}
	if _, _, code := e.run("stat", remote+"/drop.txt"); code != 0 {
		t.Error("the dry run deleted a file")
	}
	if _, _, code := e.run("stat", remote+"/new.txt"); code != cberr.ExitNotFound {
		t.Error("the dry run uploaded a file")
	}
}

func TestSyncExcludeIsInvisibleToBothSides(t *testing.T) {
	e := setup(t)
	remote := e.remotePath("mirror")

	e.writeLocal("tree/main.c", []byte("source"))
	e.writeLocal("tree/main.o", []byte("object"))
	e.writeLocal("tree/build/app", []byte("binary"))
	e.mustRun("sync", e.localPath("tree"), "cb:"+remote, "--exclude", "*.o", "--exclude", "build")

	if _, _, code := e.run("stat", remote+"/main.c"); code != 0 {
		t.Error("the unexcluded file was not synced")
	}
	for _, rel := range []string{"/main.o", "/build"} {
		if _, _, code := e.run("stat", remote+rel); code != cberr.ExitNotFound {
			t.Errorf("%s was synced despite being excluded", rel)
		}
	}

	// An excluded entry on the destination is protected from --delete: it is not
	// "missing from the source", it is outside the mirror altogether.
	e.mustRun("put", e.writeLocal("theirs.o", []byte("not mine")), remote+"/theirs.o")
	e.mustRun("sync", e.localPath("tree"), "cb:"+remote, "--delete", "--exclude", "*.o", "--exclude", "build")
	if _, _, code := e.run("stat", remote+"/theirs.o"); code != 0 {
		t.Error("--delete removed an excluded file")
	}
}

// ── open ─────────────────────────────────────────────────────────────────────

func TestOpenWebLink(t *testing.T) {
	e := setup(t)
	target := e.remotePath("linked.txt")
	e.mustRun("put", e.writeLocal("linked.txt", []byte("x")), target)

	stdout, stderr, code := e.run("open", "--web", target)
	if code != 0 {
		// A deployment that does not report oc:privatelink is a valid
		// configuration, so this is a skip rather than a failure.
		t.Skipf("this server reports no web link: %s", stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "http") {
		t.Errorf("stdout = %q, want a URL", stdout)
	}
}
