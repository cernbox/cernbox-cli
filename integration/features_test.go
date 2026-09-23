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

// TestTrashPurgeAllRefusesNonInteractively is the guard that matters most here:
// the integration run has no terminal, so the confirmation cannot be answered.
func TestTrashPurgeAllRefusesNonInteractively(t *testing.T) {
	e := setup(t)
	_, stderr, code := e.run("trash", "purge", "--all")
	if code == 0 {
		t.Error("purge --all should be refused with no terminal to confirm at")
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("the refusal should mention --yes:\n%s", stderr)
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
