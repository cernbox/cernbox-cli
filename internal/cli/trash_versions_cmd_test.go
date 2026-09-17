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
	// The server reports the location relative to the root of the space the
	// bin belongs to, as in TestTrashListJSON above, not as a full path.
	box.trash["key-1"] = trashEntry{name: "notes.txt", location: "notes.txt", body: "recovered"}

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
	// The server reports the location relative to the root of the space the
	// bin belongs to, as in TestTrashListJSON above, not as a full path.
	box.trash["key-1"] = trashEntry{name: "notes.txt", location: "notes.txt", body: "recovered"}

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

// ── sync ─────────────────────────────────────────────────────────────────────

func TestSyncPushCommand(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, box, "sync", dir, "cb:/eos/user/e/einstein/data"); err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/data/a.txt"]; got != "alpha" {
		t.Errorf("sync did not upload the file: %+v", box.files)
	}
}

func TestSyncRequiresOneRemoteSide(t *testing.T) {
	box := newTestBox(t)

	// Two local paths: the cb: prefix is missing, which is the same trap cp has.
	_, _, err := run(t, box, "sync", t.TempDir(), t.TempDir())
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}

	// Two remote paths: sync mirrors between local and CERNBox, not within it.
	_, _, err = run(t, box, "sync", "cb:/eos/user/e/einstein/a", "cb:/eos/user/e/einstein/b")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

// TestSyncDeleteAnnouncesItself: --delete can remove a lot of data on the
// strength of one mistyped path, so it must say what it is about to do.
func TestSyncDeleteAnnouncesItself(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/data")

	_, stderr, err := run(t, box, "sync", t.TempDir(), "cb:/eos/user/e/einstein/data", "--delete")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "removed from the destination") {
		t.Errorf("--delete should warn before acting:\n%s", stderr)
	}
}

func TestSyncDryRunReportsWithoutActing(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := run(t, box, "sync", "--dry-run", dir, "cb:/eos/user/e/einstein/data")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Would mirror") {
		t.Errorf("a dry run should say it is a plan:\n%s", stderr)
	}
	if _, uploaded := box.files["/eos/user/e/einstein/data/a.txt"]; uploaded {
		t.Error("a dry run uploaded the file")
	}
}

// ── open ─────────────────────────────────────────────────────────────────────

func TestOpenPrintsApplicationLink(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/report.odt", "doc")

	stdout, _, err := run(t, box, "open", "/eos/user/e/einstein/report.odt")
	if err != nil {
		t.Fatal(err)
	}
	// The URL goes to stdout so it can be piped into a clipboard tool.
	if strings.TrimSpace(stdout) != "https://office.test/edit?wopi=abc" {
		t.Errorf("stdout = %q, want just the URL", stdout)
	}
}

func TestOpenWebPrintsTheInterfaceLink(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/report.odt", "doc")

	stdout, _, err := run(t, box, "open", "--web", "/eos/user/e/einstein/report.odt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "/files/spaces/") {
		t.Errorf("stdout = %q, want the web interface link", stdout)
	}
}

// TestOpenWarnsAboutPostSessions: a POST session cannot be opened by pasting
// the link, and printing one without saying so sends the user in circles.
func TestOpenWarnsAboutPostSessions(t *testing.T) {
	box := newTestBox(t)
	box.appMethod = "POST"
	box.putFile("/eos/user/e/einstein/report.odt", "doc")

	_, stderr, err := run(t, box, "open", "/eos/user/e/einstein/report.odt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "cannot be opened directly") {
		t.Errorf("stderr should explain the POST session:\n%s", stderr)
	}
}

func TestOpenRejectsUnknownViewMode(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/report.odt", "doc")

	_, _, err := run(t, box, "open", "--view-mode", "sideways", "/eos/user/e/einstein/report.odt")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

func TestAppsCommand(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "apps")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"odt", "Collabora"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("apps output is missing %q:\n%s", want, stdout)
		}
	}
}

// ── federated sharing ────────────────────────────────────────────────────────

func TestOCMInviteCreate(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "ocm", "invite", "create", "--description", "joint analysis")
	if err != nil {
		t.Fatal(err)
	}
	// The link is the thing the user has to pass on, so it must be on stdout.
	if !strings.Contains(stdout, "https://cernbox.test/ocm/invite?token=abc123") {
		t.Errorf("the invite link is missing from stdout:\n%s", stdout)
	}
}

func TestOCMInviteList(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "ocm", "invite", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "abc123") {
		t.Errorf("invite list:\n%s", stdout)
	}
}

func TestOCMInviteAcceptWithProvider(t *testing.T) {
	box := newTestBox(t)
	if _, _, err := run(t, box, "ocm", "invite", "accept", "abc123", "--provider", "other-lab.org"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(box.acceptedInvite, `"providerDomain":"other-lab.org"`) {
		t.Errorf("accept request = %q", box.acceptedInvite)
	}
}

// TestOCMInviteAcceptFromLink: both the token and the provider are already in
// the link someone was sent, so retyping either is needless friction.
func TestOCMInviteAcceptFromLink(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "ocm", "invite", "accept",
		"https://other-lab.org/ocm/invite?token=xyz789")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(box.acceptedInvite, `"token":"xyz789"`) {
		t.Errorf("the token was not taken from the link: %q", box.acceptedInvite)
	}
	if !strings.Contains(box.acceptedInvite, `"providerDomain":"other-lab.org"`) {
		t.Errorf("the provider was not taken from the link: %q", box.acceptedInvite)
	}
}

func TestOCMInviteAcceptNeedsAProvider(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "ocm", "invite", "accept", "abc123")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

func TestOCMContacts(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "ocm", "contacts")
	if err != nil {
		t.Fatal(err)
	}
	// The address column is what gets pasted into --with-remote.
	if !strings.Contains(stdout, "alice@other-lab.org") {
		t.Errorf("contacts output:\n%s", stdout)
	}
}

func TestOCMContactsRemove(t *testing.T) {
	box := newTestBox(t)
	if _, _, err := run(t, box, "ocm", "contacts", "--remove", "alice@other-lab.org"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(box.removedContact, `"user_id":"alice"`) {
		t.Errorf("remove request = %q", box.removedContact)
	}
}

func TestOCMProviders(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "ocm", "providers")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "other-lab.org") {
		t.Errorf("providers output:\n%s", stdout)
	}
}

func TestOCMReceived(t *testing.T) {
	box := newTestBox(t)
	stdout, _, err := run(t, box, "ocm", "received")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "shared-data") {
		t.Errorf("the federated share is missing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "other-lab.org") {
		t.Errorf("the provider column is missing:\n%s", stdout)
	}
	// The local share in the same listing must not be reported as federated.
	if strings.Contains(stdout, "Marie") {
		t.Errorf("a local share leaked into the federated listing:\n%s", stdout)
	}
}

func TestShareWithRemoteRecipient(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "x")

	_, _, err := run(t, box, "share", "create", "/eos/user/e/einstein/notes.txt",
		"--with-remote", "alice@other-lab.org")
	if err != nil {
		t.Fatal(err)
	}
	body := box.lastPostBody()
	if !strings.Contains(body, `"@libre.graph.recipient.type":"remote"`) {
		t.Errorf("the recipient type should be remote:\n%s", body)
	}
	if !strings.Contains(body, `"objectId":"alice@other-lab.org"`) {
		t.Errorf("the recipient address is wrong:\n%s", body)
	}
}

// TestShareWithRemoteRejectsBareUsername: a federated recipient without a
// provider is not addressable, and failing early with advice beats a 400.
func TestShareWithRemoteRejectsBareUsername(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "x")

	_, _, err := run(t, box, "share", "create", "/eos/user/e/einstein/notes.txt",
		"--with-remote", "alice")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
	if err == nil || !strings.Contains(err.Error(), "ocm contacts") {
		t.Errorf("the error should point at 'ocm contacts', got %v", err)
	}
}
