package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// drop writes a file into an outbox folder and backdates it, so that it counts as
// finished without the test having to wait for a settle window.
func drop(t *testing.T, dir, name, body string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOutboxPushUploadsWhatIsWaiting(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	drop(t, dir, "shot.png", "image bytes", time.Minute)

	stdout, stderr, err := run(t, box, "outbox", "push", dir, "--to", "Screenshots")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, stdout, stderr)
	}
	if got := box.files["/eos/user/e/einstein/Screenshots/shot.png"]; got != "image bytes" {
		t.Errorf("not uploaded: %+v", box.files)
	}
	// And the local file is left alone, because keep is the default.
	if _, err := os.Stat(filepath.Join(dir, "shot.png")); err != nil {
		t.Errorf("the local file went away with the default policy: %v", err)
	}
}

// TestOutboxLeavesAFileStillBeingWritten is the whole reason for the settle
// window: a screenshot appears while the tool is still writing it, and uploading
// then produces half an image.
func TestOutboxLeavesAFileStillBeingWritten(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	// Written just now, so it has not settled.
	if err := os.WriteFile(filepath.Join(dir, "half.png"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := run(t, box, "outbox", "push", dir, "--to", "Screenshots", "--settle", "1h")
	if err != nil {
		t.Fatal(err)
	}
	if _, up := box.files["/eos/user/e/einstein/Screenshots/half.png"]; up {
		t.Error("a file that was still being written was uploaded")
	}
	if !strings.Contains(stderr, "still being written") {
		t.Errorf("a file left behind should be explained:\n%s", stderr)
	}
}

func TestOutboxDateLayout(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	p := drop(t, dir, "shot.png", "x", time.Minute)
	// The date comes from the file, not from today: a screenshot belongs to the
	// day it was taken whenever it happens to be uploaded.
	when := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, box, "outbox", "push", dir, "--to", "Shots", "--layout", "date"); err != nil {
		t.Fatal(err)
	}
	if _, ok := box.files["/eos/user/e/einstein/Shots/2026/03/07/shot.png"]; !ok {
		t.Errorf("not filed under its own date: %+v", box.files)
	}
}

func TestOutboxAfterDelete(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	drop(t, dir, "shot.png", "x", time.Minute)

	if _, _, err := run(t, box, "outbox", "push", dir, "--to", "Shots", "--after", "delete"); err != nil {
		t.Fatal(err)
	}
	if _, ok := box.files["/eos/user/e/einstein/Shots/shot.png"]; !ok {
		t.Fatalf("not uploaded, so nothing should have been deleted: %+v", box.files)
	}
	if _, err := os.Stat(filepath.Join(dir, "shot.png")); !os.IsNotExist(err) {
		t.Errorf("the local file survived --after delete: %v", err)
	}
}

// TestOutboxAfterDeleteKeepsTheFileWhenTheUploadFails: the one thing this must
// never do is remove somebody's only copy because an upload looked fine.
func TestOutboxAfterDeleteKeepsTheFileWhenTheUploadFails(t *testing.T) {
	box := newTestBox(t)
	box.failPath = "/eos/user/e/einstein/Shots/shot.png"
	dir := t.TempDir()
	drop(t, dir, "shot.png", "precious", time.Minute)

	if _, _, err := run(t, box, "outbox", "push", dir, "--to", "Shots", "--after", "delete"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "shot.png"))
	if err != nil || string(body) != "precious" {
		t.Errorf("the local file was lost after a failed upload: %v %q", err, body)
	}
}

func TestOutboxAfterMove(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	drop(t, dir, "shot.png", "x", time.Minute)

	if _, _, err := run(t, box, "outbox", "push", dir, "--to", "Shots", "--after", "move"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shot.png")); !os.IsNotExist(err) {
		t.Error("the file was not moved out of the outbox")
	}
	if _, err := os.Stat(filepath.Join(dir, uploadedDir, "shot.png")); err != nil {
		t.Errorf("the file is not in %s: %v", uploadedDir, err)
	}
}

// TestOutboxSkipsWhatIsAlreadyThere keeps a second run cheap and stateless: the
// destination is the record of what went, so there is no local bookkeeping to
// drift out of date.
func TestOutboxSkipsWhatIsAlreadyThere(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	drop(t, dir, "shot.png", "x", time.Minute)

	if _, _, err := run(t, box, "outbox", "push", dir, "--to", "Shots"); err != nil {
		t.Fatal(err)
	}
	before := putCount(box, "/Shots/shot.png")

	_, stderr, err := run(t, box, "outbox", "push", dir, "--to", "Shots")
	if err != nil {
		t.Fatal(err)
	}
	if after := putCount(box, "/Shots/shot.png"); after != before {
		t.Errorf("uploaded again: %d writes then %d", before, after)
	}
	if !strings.Contains(stderr, "already there") {
		t.Errorf("the second run should say why it did nothing:\n%s", stderr)
	}
}

// TestOutboxNeverOverwrites: an outbox adds to CERNBox. Something already using
// the name is not ours to replace, so the upload goes alongside it.
func TestOutboxNeverOverwrites(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Shots")
	box.putFile("/eos/user/e/einstein/Shots/shot.png", "an older and different picture")

	dir := t.TempDir()
	drop(t, dir, "shot.png", "mine", time.Minute)

	if _, _, err := run(t, box, "outbox", "push", dir, "--to", "Shots"); err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/Shots/shot.png"]; got != "an older and different picture" {
		t.Errorf("what was already there got overwritten: %q", got)
	}
	if got := box.files["/eos/user/e/einstein/Shots/shot (2).png"]; got != "mine" {
		t.Errorf("the upload did not go alongside: %+v", box.files)
	}
}

func TestOutboxSkipsTempAndHiddenNames(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	for _, name := range []string{
		"real.png", ".hidden.png", "download.crdownload", "half.part", "editor~", "x.tmp",
	} {
		drop(t, dir, name, "x", time.Minute)
	}

	if _, _, err := run(t, box, "outbox", "push", dir, "--to", "Shots"); err != nil {
		t.Fatal(err)
	}
	if _, ok := box.files["/eos/user/e/einstein/Shots/real.png"]; !ok {
		t.Error("the real file was not uploaded")
	}
	for _, name := range []string{".hidden.png", "download.crdownload", "half.part", "editor~", "x.tmp"} {
		if _, ok := box.files["/eos/user/e/einstein/Shots/"+name]; ok {
			t.Errorf("%s should not have been uploaded", name)
		}
	}
}

// TestOutboxSaysWhenAFolderWasSkipped: an outbox sends files, and somebody who
// dropped a folder in would otherwise believe it had gone.
func TestOutboxSaysWhenAFolderWasSkipped(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "a-folder"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := run(t, box, "outbox", "push", dir, "--to", "Shots")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "not folders") {
		t.Errorf("a skipped folder must be reported:\n%s", stderr)
	}
}

func TestOutboxStatusExplainsEachFile(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	// Older than the settle window below, and something written just now.
	drop(t, dir, "settled.png", "x", 2*time.Hour)
	if err := os.WriteFile(filepath.Join(dir, "fresh.png"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := run(t, box, "outbox", "status", dir, "--to", "Shots", "--settle", "1h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "waiting") || !strings.Contains(stdout, "still being written") {
		t.Errorf("status should distinguish the two:\n%s", stdout)
	}
	// And it uploads nothing.
	if len(box.files) != 0 {
		t.Errorf("status uploaded something: %+v", box.files)
	}
}

func TestOutboxLinkPrintsTheURL(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	drop(t, dir, "shot.png", "x", time.Minute)

	stdout, _, err := run(t, box, "outbox", "push", dir, "--to", "Shots", "--link")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "https://") {
		t.Errorf("no link on stdout:\n%s", stdout)
	}
}

// ── flags and configuration ──────────────────────────────────────────────────

func TestOutboxNeedsADestination(t *testing.T) {
	_, _, err := run(t, newTestBox(t), "outbox", "push", t.TempDir())
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error without --to", err)
	}
}

func TestOutboxWithoutArgumentsNeedsConfiguration(t *testing.T) {
	_, _, err := run(t, newTestBox(t), "outbox", "push")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error with nothing configured", err)
	}
}

func TestOutboxRejectsNonsense(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"outbox", "push", dir, "--to", "X", "--layout", "sideways"},
		{"outbox", "push", dir, "--to", "X", "--after", "incinerate"},
		{"outbox", "push", "--to", "X"},
	} {
		if _, _, err := run(t, newTestBox(t), args...); cberr.ExitCode(err) != cberr.ExitUsage {
			t.Errorf("%v was accepted (%v)", args, err)
		}
	}
}

func TestOutboxJobDefaults(t *testing.T) {
	job, err := newOutboxJob(OutboxFolder{Local: "/tmp/x", Remote: "Shots"})
	if err != nil {
		t.Fatal(err)
	}
	if job.layout != "flat" {
		t.Errorf("layout = %q, want flat", job.layout)
	}
	// Keeping the file is the only choice that cannot lose anything.
	if job.after != "keep" {
		t.Errorf("after = %q, want keep", job.after)
	}
}

func TestSkipOutboxName(t *testing.T) {
	for _, skip := range []string{".hidden", "x.part", "y.crdownload", "z~", "a.tmp", "b.SWP"} {
		if !skipOutboxName(skip) {
			t.Errorf("%q should be skipped", skip)
		}
	}
	for _, keep := range []string{"shot.png", "report.pdf", "a.partial.png", "notes.txt"} {
		if skipOutboxName(keep) {
			t.Errorf("%q should not be skipped", keep)
		}
	}
}

// ── watching ─────────────────────────────────────────────────────────────────

// TestOutboxWatchUploadsWhatWasAlreadyThere: nothing announced the files that
// existed before the watch started, so the first thing it does is look.
func TestOutboxWatchUploadsWhatWasAlreadyThere(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	drop(t, dir, "early.png", "before the watch", time.Minute)

	// The global timeout is what ends the watch; it returns when the context does.
	if _, _, err := run(t, box, "--timeout", "2s", "outbox", "watch", dir,
		"--to", "Shots", "--settle", "100ms"); err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/Shots/early.png"]; got != "before the watch" {
		t.Errorf("a file present at startup was not uploaded: %+v", box.files)
	}
}

// TestOutboxWatchUploadsOnArrival is the notification path: a file that appears
// while the watch is running goes up without the folder being read through on a
// timer.
func TestOutboxWatchUploadsOnArrival(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()

	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(dir, "late.png"), []byte("arrived later"), 0o644)
	}()

	// Long enough for the arrival, the settle window and a look at the pending
	// files, but well short of the sweep — so this can only pass through the
	// notification path.
	if _, _, err := run(t, box, "--timeout", "4s", "outbox", "watch", dir,
		"--to", "Shots", "--settle", "100ms", "--sweep", "1h"); err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/Shots/late.png"]; got != "arrived later" {
		t.Errorf("a file that appeared during the watch was not uploaded: %+v", box.files)
	}
}

// TestOutboxWatchWaitsForAnArrivalToSettle: an event says a file exists, not that
// whatever is writing it has finished.
func TestOutboxWatchWaitsForAnArrivalToSettle(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(dir, "slow.png"), []byte("half"), 0o644)
	}()

	// The settle window outlasts the watch, so the file is seen and deliberately
	// not sent.
	if _, _, err := run(t, box, "--timeout", "2s", "outbox", "watch", dir,
		"--to", "Shots", "--settle", "1h", "--sweep", "1h"); err != nil {
		t.Fatal(err)
	}
	if _, up := box.files["/eos/user/e/einstein/Shots/slow.png"]; up {
		t.Error("a file was uploaded before it had settled")
	}
}

// ── the exec hook ────────────────────────────────────────────────────────────

// shrinkHook returns a hook that rewrites the file it is handed, in place, with
// the given body — standing in for the thing people actually want here: strip a
// screenshot's metadata, or shrink it, before it leaves the laptop.
func shrinkHook(t *testing.T, body string) (script, log string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "calls.log")
	script = fakeProgram(t, "append "+quote(log)+
		` "$1|$CERNBOX_OUTBOX_NAME|$CERNBOX_OUTBOX_REMOTE\n"`+"\n"+
		"write $1 "+quote(body))
	return script, log
}

func failingHook(t *testing.T, code string) string {
	t.Helper()
	return fakeProgram(t, "exit "+code)
}

// TestOutboxHookUploadsWhatTheHookLeft is the whole point: the bytes that go up
// are the ones the hook produced, not the ones the folder held.
func TestOutboxHookUploadsWhatTheHookLeft(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"),
		[]byte(strings.Repeat("x", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	script, log := shrinkHook(t, "smaller")

	if _, _, err := run(t, box, "outbox", "push", dir,
		"--to", "/eos/user/e/einstein/Shots", "--settle", "0", "--exec", script); err != nil {
		t.Fatal(err)
	}

	if got := box.snapshotFiles()["/eos/user/e/einstein/Shots/shot.png"]; got != "smaller" {
		t.Errorf("uploaded %q, want what the hook left behind", got)
	}
	if calls := hookCalls(t, log); len(calls) != 1 {
		t.Errorf("the hook ran %d times, want once: %v", len(calls), calls)
	}
	// Rewritten where it was, which is the rule that makes the pass below stable.
	local, _ := os.ReadFile(filepath.Join(dir, "shot.png"))
	if string(local) != "smaller" {
		t.Errorf("the local file is %q, want the hook's own output", local)
	}
}

// TestOutboxHookDoesNotRunAgainOnASecondPass is why the hook runs after the
// already-there test rather than before it. The skip compares the local size
// against the remote one, so they only agree once the local copy is the one the
// hook produced — and rewriting in place is what makes that true. Running the
// hook first, every pass, would re-compress the same screenshot for ever.
func TestOutboxHookDoesNotRunAgainOnASecondPass(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"),
		[]byte(strings.Repeat("x", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	script, log := shrinkHook(t, "smaller")

	for range 3 {
		if _, _, err := run(t, box, "outbox", "push", dir,
			"--to", "/eos/user/e/einstein/Shots", "--settle", "0", "--exec", script); err != nil {
			t.Fatal(err)
		}
	}

	if calls := hookCalls(t, log); len(calls) != 1 {
		t.Errorf("the hook ran %d times over three passes, want once: %v", len(calls), calls)
	}
	// And nothing was uploaded a second time under another name.
	var shots int
	for p := range box.snapshotFiles() {
		if strings.HasPrefix(p, "/eos/user/e/einstein/Shots/") {
			shots++
		}
	}
	if shots != 1 {
		t.Errorf("the folder holds %d files, want one", shots)
	}
}

// TestOutboxHookFailureUploadsNothing: the hook is a precondition, so a failure
// means the file stays where it is — even under a policy that would have
// deleted it.
func TestOutboxHookFailureUploadsNothing(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	local := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(local, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := run(t, box, "outbox", "push", dir,
		"--to", "/eos/user/e/einstein/Shots", "--settle", "0",
		"--after", "delete", "--exec", failingHook(t, "4"))
	if err != nil {
		t.Fatalf("a failing hook is a warning, not the end of the run: %v", err)
	}
	if !strings.Contains(stderr, "exited with status 4") {
		t.Errorf("the message should say the hook ran and refused:\n%s", stderr)
	}
	if !strings.Contains(stderr, "not uploaded") {
		t.Errorf("the message should say what the failure meant:\n%s", stderr)
	}

	for p := range box.snapshotFiles() {
		if strings.HasPrefix(p, "/eos/user/e/einstein/Shots/") {
			t.Errorf("%s was uploaded despite the hook failing", p)
		}
	}
	if _, err := os.Stat(local); err != nil {
		t.Errorf("the local file should still be there: %v", err)
	}
}

// TestOutboxHookRenameIsNotFollowed is the decision, said out loud: a hook may
// rewrite a file where it is, and a file it renames is not chased.
func TestOutboxHookRenameIsNotFollowed(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.heic"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fakeProgram(t, "rename $1 $1.jpg")

	_, stderr, err := run(t, box, "outbox", "push", dir,
		"--to", "/eos/user/e/einstein/Shots", "--settle", "0", "--exec", script)
	if err != nil {
		t.Fatalf("a rename is reported, not fatal: %v", err)
	}
	if !strings.Contains(stderr, "rename is not followed") {
		t.Errorf("the message should explain the rule:\n%s", stderr)
	}
	for p := range box.snapshotFiles() {
		if strings.HasPrefix(p, "/eos/user/e/einstein/Shots/") {
			t.Errorf("%s was uploaded from a path the hook had emptied", p)
		}
	}
}

func TestOutboxHookIsKilledWhenItHangs(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fakeProgram(t, "sleep 60s")

	start := time.Now()
	_, stderr, err := run(t, box, "outbox", "push", dir,
		"--to", "/eos/user/e/einstein/Shots", "--settle", "0",
		"--exec", script, "--exec-timeout", "300ms")
	if err != nil {
		t.Fatalf("a hook that hangs is a warning, not the end of the run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("waited %s for a hook with a 300ms timeout", elapsed)
	}
	if !strings.Contains(stderr, "did not finish") {
		t.Errorf("nothing said the hook was killed:\n%s", stderr)
	}
}
