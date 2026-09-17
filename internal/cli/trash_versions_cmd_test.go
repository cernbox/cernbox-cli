package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// ── trash ────────────────────────────────────────────────────────────────────

func TestTrashList(t *testing.T) {
	box := newTestBox(t)
	box.trash["key-1"] = trashEntry{name: "notes.txt", location: "eos/user/e/einstein/notes.txt", body: "gone"}

	stdout, _, err := run(t, box, "trash", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"key-1", "notes.txt", "eos/user/e/einstein/notes.txt"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("trash list is missing %q:\n%s", want, stdout)
		}
	}
}

func TestTrashListEmpty(t *testing.T) {
	box := newTestBox(t)
	_, stderr, err := run(t, box, "trash", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "empty") {
		t.Errorf("an empty bin should say so:\n%s", stderr)
	}
}

func TestTrashListJSON(t *testing.T) {
	box := newTestBox(t)
	box.trash["key-1"] = trashEntry{name: "notes.txt", location: "Documents/notes.txt", body: "gone"}

	stdout, _, err := run(t, box, "--output", "json", "trash", "list")
	if err != nil {
		t.Fatal(err)
	}

	var items []struct {
		Key          string `json:"key"`
		Name         string `json:"name"`
		OriginalPath string `json:"original_path"`
	}
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout)
	}
	if len(items) != 1 || items[0].Key != "key-1" || items[0].OriginalPath != "Documents/notes.txt" {
		t.Errorf("items = %+v", items)
	}
}

func TestTrashRestoreToOriginalLocation(t *testing.T) {
	box := newTestBox(t)
	box.trash["key-1"] = trashEntry{name: "notes.txt", location: "eos/user/e/einstein/notes.txt", body: "recovered"}

	if _, _, err := run(t, box, "trash", "restore", "key-1"); err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/notes.txt"]; got != "recovered" {
		t.Errorf("the file was not restored to its original location: %+v", box.files)
	}
	if _, still := box.trash["key-1"]; still {
		t.Error("the item is still in the trash bin")
	}
}

func TestTrashRestoreToExplicitPath(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein")
	box.trash["key-1"] = trashEntry{name: "notes.txt", location: "eos/user/e/einstein/notes.txt", body: "recovered"}

	_, _, err := run(t, box, "trash", "restore", "key-1", "--to", "/eos/user/e/einstein/elsewhere.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/elsewhere.txt"]; got != "recovered" {
		t.Errorf("the file was not restored to --to: %+v", box.files)
	}
}

// TestTrashRestoreRejectsToWithSeveralKeys: one destination cannot receive
// several files, and silently restoring only the last would lose data.
func TestTrashRestoreRejectsToWithSeveralKeys(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "trash", "restore", "key-1", "key-2", "--to", "/eos/user/e/einstein/x.txt")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

func TestTrashPurgeItem(t *testing.T) {
	box := newTestBox(t)
	box.trash["key-1"] = trashEntry{name: "notes.txt", location: "Documents/notes.txt", body: "gone"}
	box.trash["key-2"] = trashEntry{name: "other.txt", location: "Documents/other.txt", body: "gone"}

	if _, _, err := run(t, box, "trash", "purge", "key-1"); err != nil {
		t.Fatal(err)
	}
	if _, still := box.trash["key-1"]; still {
		t.Error("the item was not purged")
	}
	if _, gone := box.trash["key-2"]; !gone {
		t.Error("purging one key removed another")
	}
}

func TestTrashPurgeAllWithYes(t *testing.T) {
	box := newTestBox(t)
	box.trash["key-1"] = trashEntry{name: "a", location: "a", body: "x"}
	box.trash["key-2"] = trashEntry{name: "b", location: "b", body: "y"}

	if _, _, err := run(t, box, "trash", "purge", "--all", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(box.trash) != 0 {
		t.Errorf("the bin still holds %d items", len(box.trash))
	}
}

// TestTrashPurgeAllRefusesWithoutConfirmation is the guard that matters:
// emptying the bin destroys exactly the things someone is about to recover, and
// a non-interactive run has nobody to ask.
func TestTrashPurgeAllRefusesWithoutConfirmation(t *testing.T) {
	box := newTestBox(t)
	box.trash["key-1"] = trashEntry{name: "a", location: "a", body: "x"}

	_, stderr, err := run(t, box, "trash", "purge", "--all")
	if err == nil {
		t.Fatal("expected the purge to be refused")
	}
	if len(box.trash) != 1 {
		t.Error("the bin was emptied without confirmation")
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("the refusal should mention --yes:\n%s", stderr)
	}
}

func TestTrashPurgeRejectsAllWithKeys(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "trash", "purge", "--all", "key-1")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

func TestTrashPurgeRequiresAnArgument(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "trash", "purge")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

// ── versions ─────────────────────────────────────────────────────────────────

// versionsFor registers a file plus its history under the resource id the test
// server derives from the path.
func versionsFor(box *testBox, p, body string, history ...versionEntry) string {
	box.putFile(p, body)
	id := "s1$ABC!" + strings.ReplaceAll(p, "/", "_")
	box.versions[id] = history
	return id
}

func TestVersionsList(t *testing.T) {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/report.txt", "current",
		versionEntry{key: "1767139200", body: "older"},
		versionEntry{key: "1767225600", body: "newer"},
	)

	stdout, _, err := run(t, box, "versions", "list", "/eos/user/e/einstein/report.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1767139200", "1767225600"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("versions list is missing %q:\n%s", want, stdout)
		}
	}
}

func TestVersionsListOnFileWithoutHistory(t *testing.T) {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/fresh.txt", "current")

	_, stderr, err := run(t, box, "versions", "list", "/eos/user/e/einstein/fresh.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "no previous versions") {
		t.Errorf("should say there is no history:\n%s", stderr)
	}
}

func TestVersionsListRejectsDirectory(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents")

	_, _, err := run(t, box, "versions", "list", "/eos/user/e/einstein/Documents")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error: directories have no versions", err)
	}
}

func TestVersionsRestore(t *testing.T) {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/report.txt", "current",
		versionEntry{key: "1767139200", body: "older"},
	)

	if _, _, err := run(t, box, "versions", "restore", "/eos/user/e/einstein/report.txt", "1767139200"); err != nil {
		t.Fatal(err)
	}
	if box.restored != "1767139200" {
		t.Errorf("restored version = %q, want 1767139200", box.restored)
	}
}

func TestVersionsDownload(t *testing.T) {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/report.txt", "current",
		versionEntry{key: "1767139200", body: "the older contents"},
	)

	dir := t.TempDir()
	dst := filepath.Join(dir, "old.txt")
	_, _, err := run(t, box, "versions", "download",
		"/eos/user/e/einstein/report.txt", "1767139200", "--output-file", dst)
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the older contents" {
		t.Errorf("downloaded %q", got)
	}

	// The current version must be untouched: downloading is not restoring.
	if box.files["/eos/user/e/einstein/report.txt"] != "current" {
		t.Error("downloading a version modified the current file")
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Error("the .part file was left behind")
	}
}

func TestVersionsDownloadMissingVersion(t *testing.T) {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/report.txt", "current")

	_, _, err := run(t, box, "versions", "download",
		"/eos/user/e/einstein/report.txt", "nope", "--output-file", filepath.Join(t.TempDir(), "x"))
	if cberr.ExitCode(err) != cberr.ExitNotFound {
		t.Errorf("exit code = %d, want %d", cberr.ExitCode(err), cberr.ExitNotFound)
	}
}
