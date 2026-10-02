package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// inboxSetup makes a CERNBox folder with one file in it and a local folder to
// collect into.
func inboxSetup(t *testing.T, name, body string) (*testBox, string, string) {
	t.Helper()
	box := newTestBox(t)
	remote := "/eos/user/e/einstein/incoming"
	box.mkdir(remote)
	box.putFile(remote+"/"+name, body)
	return box, remote, t.TempDir()
}

func TestInboxPullDownloadsWhatIsThere(t *testing.T) {
	box, remote, local := inboxSetup(t, "arrival.txt", "collected")

	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(local, "arrival.txt"))
	if err != nil {
		t.Fatalf("the file was not collected: %v", err)
	}
	if string(got) != "collected" {
		t.Errorf("got %q", got)
	}
}

// TestInboxPullSkipsWhatIsAlreadyHere: an inbox run from a timer must not
// re-download everything every time it fires.
func TestInboxPullSkipsWhatIsAlreadyHere(t *testing.T) {
	box, remote, local := inboxSetup(t, "arrival.txt", "collected")

	if _, _, err := run(t, box, "inbox", "pull", remote, "--to", local, "--settle", "0"); err != nil {
		t.Fatal(err)
	}
	box.mu.Lock()
	box.requests = nil
	box.mu.Unlock()

	_, stderr, err := run(t, box, "inbox", "pull", remote, "--to", local, "--settle", "0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "already here") {
		t.Errorf("the second pass should say it skipped: %s", stderr)
	}
	box.mu.Lock()
	defer box.mu.Unlock()
	for _, req := range box.requests {
		if strings.HasPrefix(req, "GET "+testDavPrefix) {
			t.Errorf("the file was downloaded again: %v", box.requests)
			break
		}
	}
}

// TestInboxWaitsForAFileStillArriving is what the settle test is for: a file
// being uploaded is visible before it is complete, so collecting it at once
// would collect half of it.
func TestInboxWaitsForAFileStillArriving(t *testing.T) {
	box, remote, local := inboxSetup(t, "arriving.txt", "half")
	// Written just now, so it is inside any settle time worth asking for.
	box.mtimes[remote+"/arriving.txt"] = time.Now()

	stdout, _, err := run(t, box, "inbox", "status", remote, "--to", local, "--settle", "1h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "still arriving") {
		t.Errorf("a file younger than the settle time should be waiting:\n%s", stdout)
	}

	if _, _, err := run(t, box, "inbox", "pull", remote, "--to", local, "--settle", "1h"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(local, "arriving.txt")); !os.IsNotExist(err) {
		t.Error("a file still arriving was collected anyway")
	}
}

// TestInboxUsesTheServerClockForSettling is the difference from the outbox, and
// it is not cosmetic: the timestamps being compared were written by the server,
// so taking now from this machine would be wrong by however far the two clocks
// are apart — which is a real condition, and what the doctor's clock check
// exists to find.
func TestInboxUsesTheServerClockForSettling(t *testing.T) {
	box, remote, local := inboxSetup(t, "arrival.txt", "collected")
	// The file was written two minutes ago by this machine's clock, which is
	// comfortably settled at a settle time of thirty seconds. The server's clock
	// is an hour behind, so by the clock that wrote the timestamp the file is
	// still in the future and cannot have settled at all. Judging it here would
	// collect a file that may still be arriving.
	box.mtimes[remote+"/arrival.txt"] = time.Now().Add(-2 * time.Minute)
	box.clockSkew = -time.Hour

	stdout, _, err := run(t, box, "inbox", "status", remote, "--to", local, "--settle", "30s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "still arriving") {
		t.Errorf("settling was judged against the wrong clock:\n%s", stdout)
	}
}

func TestInboxAfterMoveTakesTheCopyOutOfTheWay(t *testing.T) {
	box, remote, local := inboxSetup(t, "arrival.txt", "collected")

	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0", "--after", "move"); err != nil {
		t.Fatal(err)
	}
	files := box.snapshotFiles()
	if _, still := files[remote+"/arrival.txt"]; still {
		t.Error("the CERNBox copy was left where it was")
	}
	if _, moved := files[remote+"/"+collectedDir+"/arrival.txt"]; !moved {
		t.Errorf("the copy is not in %s: %v", collectedDir, keysOf(files))
	}
}

func TestInboxAfterDeleteRemovesTheCopy(t *testing.T) {
	box, remote, local := inboxSetup(t, "arrival.txt", "collected")

	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0", "--after", "delete"); err != nil {
		t.Fatal(err)
	}
	if _, still := box.snapshotFiles()[remote+"/arrival.txt"]; still {
		t.Error("the CERNBox copy was not deleted")
	}
	if _, err := os.Stat(filepath.Join(local, "arrival.txt")); err != nil {
		t.Errorf("the local copy should be there before the remote one goes: %v", err)
	}
}

func TestInboxDateLayoutFilesByTheFileOwnTimestamp(t *testing.T) {
	box, remote, local := inboxSetup(t, "arrival.txt", "collected")

	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0", "--layout", "date"); err != nil {
		t.Fatal(err)
	}
	// The fake reports 2 January 2026 for everything.
	want := filepath.Join(local, "2026", "01", "02", "arrival.txt")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("not filed under its own date: %v", err)
	}
}

// TestInboxNeverWritesOverSomethingElse: an inbox adds to the local folder, so
// a name already taken by a different file goes alongside.
func TestInboxNeverWritesOverSomethingElse(t *testing.T) {
	box, remote, local := inboxSetup(t, "arrival.txt", "from cernbox")
	mine := filepath.Join(local, "arrival.txt")
	if err := os.WriteFile(mine, []byte("mine, and a different size"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(mine); string(got) != "mine, and a different size" {
		t.Errorf("my file was written over: %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(local, "arrival (2).txt")); err != nil {
		t.Errorf("the arrival went nowhere: %v", err)
	} else if string(got) != "from cernbox" {
		t.Errorf("got %q", got)
	}
}

func TestInboxCountsSubdirectoriesItWillNotCollect(t *testing.T) {
	box, remote, local := inboxSetup(t, "arrival.txt", "collected")
	box.mkdir(remote + "/a-folder")

	_, stderr, err := run(t, box, "inbox", "status", remote, "--to", local, "--settle", "0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "subdirectory") {
		t.Errorf("a folder nobody will collect should be mentioned:\n%s", stderr)
	}
}

func TestInboxNeedsADestination(t *testing.T) {
	box, remote, _ := inboxSetup(t, "arrival.txt", "x")

	_, _, err := run(t, box, "inbox", "pull", remote)
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("got %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "--to") {
		t.Errorf("the message should name the flag: %v", err)
	}
}

func TestInboxWithoutConfigurationSaysWhatToDo(t *testing.T) {
	box := newTestBox(t)

	_, _, err := run(t, box, "inbox", "pull")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("got %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "inbox") {
		t.Errorf("the message should point at the configuration: %v", err)
	}
}

func TestInboxRejectsAnUnknownLayoutAndPolicy(t *testing.T) {
	box, remote, local := inboxSetup(t, "a.txt", "x")

	_, _, err := run(t, box, "inbox", "pull", remote, "--to", local, "--layout", "sideways")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("layout: got %v, want a usage error", err)
	}
	_, _, err = run(t, box, "inbox", "pull", remote, "--to", local, "--after", "burn")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("after: got %v, want a usage error", err)
	}
}

func TestInboxSkipsNamesThatAreNotFinishedFiles(t *testing.T) {
	box := newTestBox(t)
	remote := "/eos/user/e/einstein/incoming"
	box.mkdir(remote)
	box.putFile(remote+"/good.txt", "yes")
	box.putFile(remote+"/half.part", "no")
	box.putFile(remote+"/.hidden", "no")
	box.putFile(remote+"/backup~", "no")
	local := t.TempDir()

	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(local)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "good.txt" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("collected %v, want only good.txt", names)
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── the exec hook ────────────────────────────────────────────────────────────

// hookScript returns a hook that records every file it was handed and exits
// with exitCode, and the path of its log.
func hookScript(t *testing.T, exitCode int) (script, log string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "calls.log")
	script = fakeProgram(t, "append "+quote(log)+
		` "$1|$CERNBOX_INBOX_NAME|$CERNBOX_INBOX_SIZE|$CERNBOX_INBOX_REMOTE\n"`+"\n"+
		"exit "+itoa(exitCode))
	return script, log
}

func hookCalls(t *testing.T, log string) []string {
	t.Helper()
	body, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

func TestInboxHookRunsForEachFileCollected(t *testing.T) {
	box, remote, local := inboxSetup(t, "measurement.csv", "1,2,3")
	script, log := hookScript(t, 0)

	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0", "--exec", script); err != nil {
		t.Fatal(err)
	}

	calls := hookCalls(t, log)
	if len(calls) != 1 {
		t.Fatalf("the hook ran %d times, want once: %v", len(calls), calls)
	}
	want := filepath.Join(local, "measurement.csv") + "|measurement.csv|5|" + remote + "/measurement.csv"
	if calls[0] != want {
		t.Errorf("the hook was handed\n  %s\nwant\n  %s", calls[0], want)
	}
}

// TestInboxHookDoesNotRunForSomethingAlreadyHere: with keep there is no way to
// tell from the folder whether a hook has run, so running it on every pass
// would mean running it for ever.
func TestInboxHookDoesNotRunForSomethingAlreadyHere(t *testing.T) {
	box, remote, local := inboxSetup(t, "measurement.csv", "1,2,3")
	script, log := hookScript(t, 0)

	for range 3 {
		if _, _, err := run(t, box, "inbox", "pull", remote,
			"--to", local, "--settle", "0", "--exec", script); err != nil {
			t.Fatal(err)
		}
	}
	if calls := hookCalls(t, log); len(calls) != 1 {
		t.Errorf("the hook ran %d times over three passes, want once: %v", len(calls), calls)
	}
}

// TestInboxHookFailureLeavesTheFileInTheInbox is the ordering that makes the
// hook worth having: if the hook is why the inbox exists, moving the CERNBox
// copy out of the way after it failed throws away the only evidence that
// something is unfinished.
func TestInboxHookFailureLeavesTheFileInTheInbox(t *testing.T) {
	box, remote, local := inboxSetup(t, "measurement.csv", "1,2,3")
	script, _ := hookScript(t, 3)

	_, stderr, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0", "--after", "move", "--exec", script)
	if err != nil {
		t.Fatalf("a failing hook is a warning, not the end of the run: %v", err)
	}
	if !strings.Contains(stderr, "left in the inbox") {
		t.Errorf("nothing said what the failure meant:\n%s", stderr)
	}

	files := box.snapshotFiles()
	if _, still := files[remote+"/measurement.csv"]; !still {
		t.Error("the CERNBox copy was moved even though the hook failed")
	}
	// The file is still downloaded: that part worked, and pretending otherwise
	// would mean fetching it again.
	if _, err := os.Stat(filepath.Join(local, "measurement.csv")); err != nil {
		t.Errorf("the local copy should be there: %v", err)
	}
}

// TestInboxHookRetriesWhereTheFolderRemembers: with move or delete, a file
// still in the inbox is one whose hook did not finish, so the next pass runs it
// again — which is what makes a failure recoverable rather than permanent.
func TestInboxHookRetriesWhereTheFolderRemembers(t *testing.T) {
	box, remote, local := inboxSetup(t, "measurement.csv", "1,2,3")
	failing, _ := hookScript(t, 3)
	working, log := hookScript(t, 0)

	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0", "--after", "move", "--exec", failing); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0", "--after", "move", "--exec", working); err != nil {
		t.Fatal(err)
	}

	if calls := hookCalls(t, log); len(calls) != 1 {
		t.Errorf("the working hook ran %d times, want once on the retry: %v", len(calls), calls)
	}
	files := box.snapshotFiles()
	if _, still := files[remote+"/measurement.csv"]; still {
		t.Error("the retry succeeded, so the copy should have been moved")
	}
	if _, moved := files[remote+"/"+collectedDir+"/measurement.csv"]; !moved {
		t.Errorf("not in %s: %v", collectedDir, keysOf(files))
	}
}

// TestInboxHookGetsNoShell matters more here than anywhere else in the CLI: the
// names come from whoever is putting things in the folder, which with an upload
// link is a stranger.
func TestInboxHookGetsNoShell(t *testing.T) {
	box := newTestBox(t)
	remote := "/eos/user/e/einstein/incoming"
	box.mkdir(remote)
	// A name a shell would both split into three words and act on. No slashes
	// in it: a name with one is not a name, it is a path, and the file would not
	// be in this folder at all — which this test got wrong twice before saying
	// anything useful.
	const nasty = "safe.txt; touch pwned"
	box.putFile(remote+"/"+nasty, "x")
	local := t.TempDir()
	script, log := hookScript(t, 0)

	if _, _, err := run(t, box, "inbox", "pull", remote,
		"--to", local, "--settle", "0", "--exec", script); err != nil {
		t.Fatal(err)
	}

	// The name arrived whole, as one argument: nothing split it and nothing
	// interpreted it. That is the property worth pinning, because the names come
	// from whoever is putting things in the folder, which with an upload link is
	// a stranger.
	calls := hookCalls(t, log)
	if len(calls) != 1 {
		t.Fatalf("the hook ran %d times: %v", len(calls), calls)
	}
	if want := filepath.Join(local, nasty) + "|" + nasty + "|"; !strings.HasPrefix(calls[0], want) {
		t.Errorf("the hook was handed\n  %s\nwant it to start with\n  %s", calls[0], want)
	}
}

func TestInboxHookIsKilledWhenItHangs(t *testing.T) {
	box, remote, local := inboxSetup(t, "measurement.csv", "1,2,3")

	script := fakeProgram(t, "sleep 60s")

	start := time.Now()
	_, stderr, err := run(t, box, "inbox", "pull", remote, "--to", local,
		"--settle", "0", "--after", "move", "--exec", script, "--exec-timeout", "300ms")
	if err != nil {
		t.Fatalf("a hook that hangs is a warning, not the end of the run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("waited %s for a hook with a 300ms timeout", elapsed)
	}
	if !strings.Contains(stderr, "did not finish") {
		t.Errorf("nothing said the hook was killed:\n%s", stderr)
	}
	// And the policy did not run, so the next pass can try again.
	if _, still := box.snapshotFiles()[remote+"/measurement.csv"]; !still {
		t.Error("the copy was moved even though the hook never finished")
	}
}
