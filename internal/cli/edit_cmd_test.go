package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// fakeEditor returns an --editor that acts out script, in the language
// fakeProgram describes. A real program rather than a seam in the code: the
// point of this command is that it runs somebody else's program against a file
// on disk, and a test that stubbed that out would be testing the wrong thing.
func fakeEditor(t *testing.T, script string) string {
	t.Helper()
	return fakeProgram(t, script)
}

// putCount is how many times a path was written.
func putCount(box *testBox, name string) int {
	n := 0
	for _, r := range box.requests {
		if strings.HasPrefix(r, "PUT ") && strings.HasSuffix(r, name) {
			n++
		}
	}
	return n
}

// TestEditPutsABareNameInTheEditFolder is the case the command exists for:
// "cernbox edit notes.txt" from anywhere, landing somewhere predictable.
func TestEditPutsABareNameInTheEditFolder(t *testing.T) {
	box := newTestBox(t)
	editor := fakeEditor(t, `write $1 "hello from the editor"`)

	stdout, stderr, err := run(t, box, "edit", "notes.txt", "--editor", editor)
	if err != nil {
		t.Fatal(err)
	}

	want := "/eos/user/e/einstein/myfiles/notes.txt"
	if got := box.files[want]; got != "hello from the editor" {
		t.Errorf("%s = %q, want the editor's content. Files: %+v", want, got, box.files)
	}
	// The folder has to be created, since a PUT into a missing collection fails.
	if !box.dirs["/eos/user/e/einstein/myfiles"] {
		t.Error("the edit folder was not created")
	}
	// Where it went must be said before the editor opens, or the whole
	// arrangement is a guess.
	if !strings.Contains(stderr, want) {
		t.Errorf("the resolved path was not printed:\n%s%s", stdout, stderr)
	}
}

func TestEditTakesAPathLiterally(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents")
	editor := fakeEditor(t, `write $1 x`)

	if _, _, err := run(t, box, "edit", "Documents/report.md", "--editor", editor); err != nil {
		t.Fatal(err)
	}
	if _, ok := box.files["/eos/user/e/einstein/Documents/report.md"]; !ok {
		t.Errorf("a path should not be redirected into the edit folder: %+v", box.files)
	}
}

func TestEditFolderIsConfigurable(t *testing.T) {
	box := newTestBox(t)
	editor := fakeEditor(t, `write $1 x`)

	if _, _, err := run(t, box, "edit", "todo.md", "--in", "Scratch", "--editor", editor); err != nil {
		t.Fatal(err)
	}
	if _, ok := box.files["/eos/user/e/einstein/Scratch/todo.md"]; !ok {
		t.Errorf("--in was ignored: %+v", box.files)
	}
}

func TestEditFolderFromEnvironment(t *testing.T) {
	box := newTestBox(t)
	t.Setenv("CERNBOX_EDIT_FOLDER", "Inbox")
	editor := fakeEditor(t, `write $1 x`)

	if _, _, err := run(t, box, "edit", "note.txt", "--editor", editor); err != nil {
		t.Fatal(err)
	}
	if _, ok := box.files["/eos/user/e/einstein/Inbox/note.txt"]; !ok {
		t.Errorf("CERNBOX_EDIT_FOLDER was ignored: %+v", box.files)
	}
}

// TestEditOpensTheExistingContent: the editor must be given the file as it is on
// the server, not an empty buffer, or editing is really overwriting.
func TestEditOpensTheExistingContent(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/myfiles")
	box.putFile("/eos/user/e/einstein/myfiles/notes.txt", "first line\n")
	editor := fakeEditor(t, `append $1 "second line\n"`)

	if _, _, err := run(t, box, "edit", "notes.txt", "--editor", editor); err != nil {
		t.Fatal(err)
	}
	got := box.files["/eos/user/e/einstein/myfiles/notes.txt"]
	if got != "first line\nsecond line\n" {
		t.Errorf("content = %q: the working copy did not start from the server's version", got)
	}
}

// TestEditSavesWhileTheEditorIsStillOpen is the heart of the feature. The upload
// must happen on each save, not only when the editor exits.
func TestEditSavesWhileTheEditorIsStillOpen(t *testing.T) {
	box := newTestBox(t)
	editor := fakeEditor(t, `
write $1 "draft one"
sleep 400ms
write $1 "draft two"
`)

	if _, _, err := run(t, box, "edit", "notes.txt",
		"--editor", editor, "--interval", "50ms"); err != nil {
		t.Fatal(err)
	}

	if n := putCount(box, "/myfiles/notes.txt"); n < 2 {
		t.Errorf("the file was written %d times, want at least 2: the mid-session save was missed", n)
	}
	if got := box.files["/eos/user/e/einstein/myfiles/notes.txt"]; got != "draft two" {
		t.Errorf("final content = %q", got)
	}
}

// TestEditDetectsASaveByRename covers the editors that save by writing a new
// file and renaming it over the old one — sed -i does it, and the "atomic save"
// editors are known for it. A watch on the file itself would follow the inode
// that was replaced and see nothing ever again; watching the path does not care.
func TestEditDetectsASaveByRename(t *testing.T) {
	box := newTestBox(t)
	editor := fakeEditor(t, `
write $1 "in place"
sleep 400ms
write $1.new "by rename"
rename $1.new $1
sleep 400ms
`)

	if _, _, err := run(t, box, "edit", "notes.txt",
		"--editor", editor, "--interval", "50ms"); err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/myfiles/notes.txt"]; got != "by rename" {
		t.Errorf("content = %q: a save by rename was not noticed", got)
	}
}

// TestEditWithNoWatchOnlySavesAtTheEnd: the escape hatch for a slow link, where
// a save per keystroke-burst is not wanted.
func TestEditWithNoWatchOnlySavesAtTheEnd(t *testing.T) {
	box := newTestBox(t)
	editor := fakeEditor(t, `
write $1 one
sleep 300ms
write $1 two
`)

	if _, _, err := run(t, box, "edit", "notes.txt",
		"--editor", editor, "--interval", "50ms", "--no-watch"); err != nil {
		t.Fatal(err)
	}
	if n := putCount(box, "/myfiles/notes.txt"); n != 1 {
		t.Errorf("wrote %d times, want exactly 1 with --no-watch", n)
	}
	if got := box.files["/eos/user/e/einstein/myfiles/notes.txt"]; got != "two" {
		t.Errorf("final content = %q, want the last version", got)
	}
}

// TestEditUploadsNothingWhenNothingChanged: quitting an editor without saving
// must not touch the server, and must not create a folder either.
func TestEditUploadsNothingWhenNothingChanged(t *testing.T) {
	box := newTestBox(t)
	editor := fakeEditor(t, `true`)

	_, stderr, err := run(t, box, "edit", "notes.txt", "--editor", editor)
	if err != nil {
		t.Fatal(err)
	}
	if n := putCount(box, "/myfiles/notes.txt"); n != 0 {
		t.Errorf("wrote %d times for an untouched file", n)
	}
	if box.dirs["/eos/user/e/einstein/myfiles"] {
		t.Error("the folder was created for a file that was never saved")
	}
	if !strings.Contains(stderr, "No changes") {
		t.Errorf("should say nothing happened:\n%s", stderr)
	}
}

// TestEditSavingTheSameContentUploadsNothing: editors write the file on :w
// whether or not anything changed.
func TestEditSavingTheSameContentUploadsNothing(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/myfiles")
	box.putFile("/eos/user/e/einstein/myfiles/notes.txt", "unchanged")
	editor := fakeEditor(t, `
sleep 200ms
touch $1
write $1 unchanged
sleep 200ms
`)

	if _, _, err := run(t, box, "edit", "notes.txt",
		"--editor", editor, "--interval", "50ms"); err != nil {
		t.Fatal(err)
	}
	if n := putCount(box, "/myfiles/notes.txt"); n != 0 {
		t.Errorf("wrote %d times although the content never changed", n)
	}
}

// TestEditRefusesToClobberAConcurrentChange: somebody else saved the file while
// it was open. Overwriting would throw their work away without telling anybody.
func TestEditRefusesToClobberAConcurrentChange(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/myfiles")
	box.putFile("/eos/user/e/einstein/myfiles/notes.txt", "mine")

	// Another client writes between the read and the write that depends on it.
	box.beforePut = func(p string) {
		box.files[p] = "theirs"
		box.bumpETag(p)
		box.beforePut = nil
	}

	editor := fakeEditor(t, `write $1 "my edit"`)
	_, stderr, err := run(t, box, "edit", "notes.txt", "--editor", editor)

	if cberr.KindOf(err) != cberr.KindConflict {
		t.Fatalf("err = %v (kind %v), want a conflict", err, cberr.KindOf(err))
	}
	if got := box.files["/eos/user/e/einstein/myfiles/notes.txt"]; got != "theirs" {
		t.Errorf("the other client's version was overwritten: %q", got)
	}

	// The user's work is the only copy of itself, so it has to survive and be
	// findable.
	local := localPathFrom(err.Error() + "\n" + stderr)
	if local == "" {
		t.Fatalf("the refusal does not say where the edit was kept:\n%v\n%s", err, stderr)
	}
	data, readErr := os.ReadFile(local)
	if readErr != nil {
		t.Fatalf("the working copy was deleted despite the failure: %v", readErr)
	}
	if string(data) != "my edit" {
		t.Errorf("kept file = %q, want the edit", data)
	}
	_ = os.RemoveAll(filepath.Dir(local))
}

// TestEditForceOverwritesAConcurrentChange is the escape hatch, for when you know
// yours is the version that should win.
func TestEditForceOverwritesAConcurrentChange(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/myfiles")
	box.putFile("/eos/user/e/einstein/myfiles/notes.txt", "mine")
	box.beforePut = func(p string) {
		box.files[p] = "theirs"
		box.bumpETag(p)
		box.beforePut = nil
	}

	editor := fakeEditor(t, `write $1 "my edit"`)
	if _, _, err := run(t, box, "edit", "notes.txt", "--editor", editor, "--force"); err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/myfiles/notes.txt"]; got != "my edit" {
		t.Errorf("content = %q, want the forced version", got)
	}
}

// TestEditKeepsTheWorkingCopyWhenTheUploadFails: an upload can fail for reasons
// that have nothing to do with a conflict, and the edit still exists only locally.
func TestEditKeepsTheWorkingCopyWhenTheUploadFails(t *testing.T) {
	box := newTestBox(t)
	box.failPath = "/eos/user/e/einstein/myfiles/notes.txt"
	editor := fakeEditor(t, `write $1 precious`)

	_, stderr, err := run(t, box, "edit", "notes.txt", "--editor", editor)
	if err == nil {
		t.Fatal("a failed upload must be reported")
	}
	local := localPathFrom(err.Error() + "\n" + stderr)
	if local == "" {
		t.Fatalf("the failure does not say where the edit was kept:\n%v\n%s", err, stderr)
	}
	data, readErr := os.ReadFile(local)
	if readErr != nil || string(data) != "precious" {
		t.Errorf("the edit was lost: %v %q", readErr, data)
	}
	_ = os.RemoveAll(filepath.Dir(local))
}

// localPathFrom picks the working-copy path out of a message.
func localPathFrom(msg string) string {
	for _, field := range strings.FieldsFunc(msg, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t'
	}) {
		field = strings.Trim(field, ".,;—()")
		if strings.Contains(field, "cernbox-edit-") {
			return field
		}
	}
	return ""
}

func TestEditRefusesADirectory(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents")
	editor := fakeEditor(t, `true`)

	// A path, not a bare name: "Documents" on its own is a file name and would
	// mean a new file in the edit folder, which is the documented rule.
	_, _, err := run(t, box, "edit", "/eos/user/e/einstein/Documents", "--editor", editor)
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("err = %v, want a usage error for a directory", err)
	}
}

// TestEditNeedsATerminalUnlessTheEditorIsNamed: a scheduled job that starts vi
// waits for a keystroke nobody will type.
func TestEditNeedsATerminalUnlessTheEditorIsNamed(t *testing.T) {
	box := newTestBox(t)
	t.Setenv("EDITOR", "vi")

	_, _, err := run(t, box, "edit", "notes.txt")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("err = %v, want a usage error with no terminal", err)
	}
	if !strings.Contains(errLine(err), "terminal") {
		t.Errorf("the refusal should explain itself: %v", err)
	}
}

func TestEditReportsAnEditorThatCannotRun(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "edit", "notes.txt", "--editor", "/nonexistent/editor")
	if err == nil {
		t.Fatal("an editor that cannot start must be reported")
	}
}

// TestEditStillSavesWhenTheEditorFails: the editor's exit status says nothing
// about whether the file was written.
func TestEditStillSavesWhenTheEditorFails(t *testing.T) {
	box := newTestBox(t)
	editor := fakeEditor(t, `
write $1 "saved anyway"
exit 3
`)

	_, stderr, err := run(t, box, "edit", "notes.txt", "--editor", editor)
	if err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/myfiles/notes.txt"]; got != "saved anyway" {
		t.Errorf("content = %q: a failing editor lost the edit", got)
	}
	if !strings.Contains(stderr, "exited with an error") {
		t.Errorf("the editor's failure should be mentioned:\n%s", stderr)
	}
}

func TestEditCleansUpOnSuccess(t *testing.T) {
	box := newTestBox(t)

	// The editor records the path it was given, so the test can check that the
	// working copy is gone afterwards.
	where := filepath.Join(t.TempDir(), "where")
	editor := fakeEditor(t, "write $1 x\nwrite "+quote(where)+" $1")

	if _, _, err := run(t, box, "edit", "notes.txt", "--editor", editor); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(where)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(recorded)); !os.IsNotExist(err) {
		t.Errorf("the working copy at %s outlived a successful edit", recorded)
	}
}

// ── the small decisions ──────────────────────────────────────────────────────

func TestIsBareEditName(t *testing.T) {
	bare := []string{"notes.txt", "a", "file with spaces.md", "weird-name"}
	notBare := []string{"", ".", "..", "a/b", "/eos/user/x", "home:Documents", "./x", "cb:/eos/x"}

	for _, s := range bare {
		if !isBareEditName(s) {
			t.Errorf("%q should be a bare name", s)
		}
	}
	for _, s := range notBare {
		if isBareEditName(s) {
			t.Errorf("%q should not be a bare name", s)
		}
	}
}

func TestEditorCommandPrecedence(t *testing.T) {
	t.Setenv("VISUAL", "visual-editor")
	t.Setenv("EDITOR", "env-editor")

	app := &App{cfg: DefaultConfig(), flags: &globalFlags{}}

	// The flag wins over everything, and carries its own arguments.
	got, err := app.editorCommand(editOptions{editor: "code -w", editorGiven: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "code,-w" {
		t.Errorf("got %v, want the flag split into arguments", got)
	}

	// Then this CLI's own setting, then VISUAL, then EDITOR.
	app.cfg.Edit.Command = "cfg-editor"
	if got, _ := app.editorCommand(editOptions{editorGiven: true}); got[0] != "cfg-editor" {
		t.Errorf("got %v, want the configured editor", got)
	}
	app.cfg.Edit.Command = ""
	if got, _ := app.editorCommand(editOptions{editorGiven: true}); got[0] != "visual-editor" {
		t.Errorf("got %v, want VISUAL", got)
	}
	t.Setenv("VISUAL", "")
	if got, _ := app.editorCommand(editOptions{editorGiven: true}); got[0] != "env-editor" {
		t.Errorf("got %v, want EDITOR", got)
	}
	// And vi in the end, as git and crontab do, or Notepad on Windows.
	t.Setenv("EDITOR", "")
	if got, _ := app.editorCommand(editOptions{editorGiven: true}); got[0] != fallbackEditor {
		t.Errorf("got %v, want %s as the last resort", got, fallbackEditor)
	}
}

// TestEditWithRealVim drives the whole flow with an actual editor rather than a
// script standing in for one, because the thing most likely to be wrong is an
// assumption about how a real editor writes a file. Skipped where vim is absent;
// -es is its silent batch mode, which needs no terminal.
func TestEditWithRealVim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the wrapper below is a shell script")
	}
	vim, err := exec.LookPath("vim")
	if err != nil {
		t.Skip("vim is not installed")
	}

	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/myfiles")
	box.putFile("/eos/user/e/einstein/myfiles/notes.txt", "line one\n")

	// A wrapper, because --editor splits on spaces and vim's arguments contain
	// none that would survive it.
	editor := filepath.Join(t.TempDir(), "vim.sh")
	script := "#!/bin/sh\nset -e\n" + vim + ` -u NONE -es -c ':normal Goline two' -c ':wq' "$1" </dev/null` + "\n"
	if err := os.WriteFile(editor, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, box, "edit", "notes.txt", "--editor", editor); err != nil {
		t.Fatal(err)
	}
	got := box.files["/eos/user/e/einstein/myfiles/notes.txt"]
	if got != "line one\nline two\n" {
		t.Errorf("content = %q, want vim's edit of the server's version", got)
	}
}

// ── editing a file on this machine ───────────────────────────────────────────

// TestEditLocalFileUploadsOnSave: the local half of the command. The file on this
// machine is what the editor opens, and CERNBox keeps up with it.
func TestEditLocalFileUploadsOnSave(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	local := filepath.Join(dir, "local.txt")
	if err := os.WriteFile(local, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := fakeEditor(t, `append $1 "edited locally\n"`)

	stdout, stderr, err := run(t, box, "edit", local, "--editor", editor)
	if err != nil {
		t.Fatal(err)
	}

	// The local file is the one that was edited, and it is still there.
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("the user's own file was removed: %v", err)
	}
	if string(got) != "mine\nedited locally\n" {
		t.Errorf("local file = %q", got)
	}
	// And its content reached CERNBox, under its own name, in the edit folder.
	if up := box.files["/eos/user/e/einstein/myfiles/local.txt"]; up != "mine\nedited locally\n" {
		t.Errorf("uploaded = %q, want the local file's content. Files: %+v", up, box.files)
	}
	// Both places have to be named, since they are different.
	for _, want := range []string{local, "/eos/user/e/einstein/myfiles/local.txt"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("%q is not mentioned:\n%s%s", want, stdout, stderr)
		}
	}
}

// TestEditLocalFileIsNotOverwrittenByTheServer is the one that would hurt: the
// user asked to edit what is here, so a download must never land on top of it.
func TestEditLocalFileIsNotOverwrittenByTheServer(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/myfiles")
	box.putFile("/eos/user/e/einstein/myfiles/notes.txt", "THE SERVER VERSION")

	dir := t.TempDir()
	local := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(local, []byte("my local version"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An editor that only reports what it was given to open.
	seen := filepath.Join(dir, "seen")
	editor := fakeEditor(t, "copy $1 "+quote(seen))

	// A different file already has that name in CERNBox, so this is refused:
	// 'cp' wants --force before replacing a destination, and so does this.
	_, _, err := run(t, box, "edit", local, "--editor", editor)
	if cberr.KindOf(err) != cberr.KindConflict {
		t.Fatalf("err = %v (kind %v), want a refusal to replace the remote file",
			err, cberr.KindOf(err))
	}
	if box.files["/eos/user/e/einstein/myfiles/notes.txt"] != "THE SERVER VERSION" {
		t.Error("the remote file was replaced despite the refusal")
	}
	if _, err := os.Stat(seen); err == nil {
		t.Error("the editor was started before the refusal")
	}

	// With --force it goes ahead, and the editor still gets the local file
	// rather than the download.
	if _, _, err := run(t, box, "edit", local, "--editor", editor, "--force"); err != nil {
		t.Fatal(err)
	}
	opened, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != "my local version" {
		t.Errorf("the editor was given %q, want the local file untouched by the download", opened)
	}
	if up := box.files["/eos/user/e/einstein/myfiles/notes.txt"]; up != "my local version" {
		t.Errorf("uploaded = %q, want the local file with --force", up)
	}
}

// TestEditLocalFileSavesEveryChange: the same watch as the remote case.
func TestEditLocalFileSavesEveryChange(t *testing.T) {
	box := newTestBox(t)
	local := filepath.Join(t.TempDir(), "log.txt")
	if err := os.WriteFile(local, []byte("start\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := fakeEditor(t, `
append $1 "one\n"
sleep 400ms
append $1 "two\n"
`)

	if _, _, err := run(t, box, "edit", local, "--editor", editor, "--interval", "50ms"); err != nil {
		t.Fatal(err)
	}
	if n := putCount(box, "/myfiles/log.txt"); n < 2 {
		t.Errorf("wrote %d times, want at least 2", n)
	}
	if up := box.files["/eos/user/e/einstein/myfiles/log.txt"]; up != "start\none\ntwo\n" {
		t.Errorf("uploaded = %q", up)
	}
}

// TestEditLocalFileUploadsEvenIfTheEditorChangesNothing: for a local file the
// first upload is the point, whether or not anything was typed — otherwise
// "cernbox edit ./notes.txt" on an existing file would do nothing at all.
func TestEditLocalFileUploadsEvenIfTheEditorChangesNothing(t *testing.T) {
	box := newTestBox(t)
	local := filepath.Join(t.TempDir(), "already.txt")
	if err := os.WriteFile(local, []byte("as it was"), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := fakeEditor(t, `true`)

	if _, _, err := run(t, box, "edit", local, "--editor", editor); err != nil {
		t.Fatal(err)
	}
	if up := box.files["/eos/user/e/einstein/myfiles/already.txt"]; up != "as it was" {
		t.Errorf("uploaded = %q, want the file sent even though the editor changed nothing", up)
	}
}

func TestEditLocalFileCreatesItWhenMissing(t *testing.T) {
	box := newTestBox(t)
	local := filepath.Join(t.TempDir(), "new.txt")
	editor := fakeEditor(t, `write $1 "brand new\n"`)

	if _, _, err := run(t, box, "edit", "file:"+local, "--editor", editor); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); err != nil {
		t.Errorf("the local file was not created: %v", err)
	}
	if up := box.files["/eos/user/e/einstein/myfiles/new.txt"]; up != "brand new\n" {
		t.Errorf("uploaded = %q", up)
	}
}

// TestEditLocalFileSurvivesAFailedUpload: an upload that fails must leave the
// user's file exactly where it is, and say the upload is what failed.
func TestEditLocalFileSurvivesAFailedUpload(t *testing.T) {
	box := newTestBox(t)
	box.failPath = "/eos/user/e/einstein/myfiles/doomed.txt"
	local := filepath.Join(t.TempDir(), "doomed.txt")
	if err := os.WriteFile(local, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := fakeEditor(t, `write $1 "still here"`)

	_, _, err := run(t, box, "edit", local, "--editor", editor)
	if err == nil {
		t.Fatal("a failed upload must be reported")
	}
	got, readErr := os.ReadFile(local)
	if readErr != nil || string(got) != "still here" {
		t.Errorf("the local file was disturbed: %v %q", readErr, got)
	}
}

func TestEditRefusesALocalDirectory(t *testing.T) {
	box := newTestBox(t)
	editor := fakeEditor(t, `true`)
	_, _, err := run(t, box, "edit", "file:"+t.TempDir(), "--editor", editor)
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("err = %v, want a usage error for a directory", err)
	}
}

// TestEditPrefersCERNBoxForAnUnmarkedPath: on lxplus "/eos/user/..." is both a
// CERNBox path and a real local mount. This is a CERNBox command, so a path that
// is in CERNBox is the CERNBox one.
func TestEditPrefersCERNBoxForAnUnmarkedPath(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents")
	box.putFile("/eos/user/e/einstein/Documents/both.txt", "the cernbox one")

	seen := filepath.Join(t.TempDir(), "seen")
	editor := fakeEditor(t, "copy $1 "+quote(seen))

	if _, _, err := run(t, box, "edit", "/eos/user/e/einstein/Documents/both.txt",
		"--editor", editor); err != nil {
		t.Fatal(err)
	}
	opened, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != "the cernbox one" {
		t.Errorf("the editor was given %q, want the CERNBox file", opened)
	}
}

// TestEditFallsBackToALocalPath: an unmarked path that CERNBox does not have,
// but this machine does, is the local one — which is what makes an absolute
// local path work without a marker, since shells expand "~" before we see it.
func TestEditFallsBackToALocalPath(t *testing.T) {
	box := newTestBox(t)
	local := filepath.Join(t.TempDir(), "onlyhere.txt")
	if err := os.WriteFile(local, []byte("only here"), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := fakeEditor(t, `append $1 touched`)

	if _, _, err := run(t, box, "edit", local, "--editor", editor); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); err != nil {
		t.Errorf("the local file went missing: %v", err)
	}
	if up := box.files["/eos/user/e/einstein/myfiles/onlyhere.txt"]; up != "only heretouched" {
		t.Errorf("uploaded = %q", up)
	}
}

func TestLooksLocal(t *testing.T) {
	yes := []string{"./x", "../x", ".", "..", "~/x", "~"}
	no := []string{"x", "a/b", "/eos/user/x", "home:Documents", "cb:/eos/x", "~x"}
	for _, s := range yes {
		if !looksLocal(s) {
			t.Errorf("%q should read as a local path", s)
		}
	}
	for _, s := range no {
		if looksLocal(s) {
			t.Errorf("%q should not read as a local path", s)
		}
	}
}

// TestEditLocalFileTwiceNeedsNoForce is the everyday local workflow: keep editing
// the same file, keep it backed up. The second run finds its own upload already in
// CERNBox, which must not read as a collision — only a *different* file under the
// same name is one.
func TestEditLocalFileTwiceNeedsNoForce(t *testing.T) {
	box := newTestBox(t)
	local := filepath.Join(t.TempDir(), "diary.txt")
	if err := os.WriteFile(local, []byte("day one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first := fakeEditor(t, `append $1 "day two\n"`)
	if _, _, err := run(t, box, "edit", local, "--editor", first); err != nil {
		t.Fatal(err)
	}

	second := fakeEditor(t, `append $1 "day three\n"`)
	if _, _, err := run(t, box, "edit", local, "--editor", second); err != nil {
		t.Fatalf("the second run should not need --force: %v", err)
	}

	want := "day one\nday two\nday three\n"
	if up := box.files["/eos/user/e/einstein/myfiles/diary.txt"]; up != want {
		t.Errorf("uploaded = %q, want %q", up, want)
	}
}

// TestEditLocalFileMatchingTheServerUploadsNothing: when the two sides already
// agree there is nothing to send, so an editor that changes nothing costs no
// upload at all.
func TestEditLocalFileMatchingTheServerUploadsNothing(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/myfiles")
	box.putFile("/eos/user/e/einstein/myfiles/same.txt", "identical")

	local := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(local, []byte("identical"), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := fakeEditor(t, `true`)

	if _, _, err := run(t, box, "edit", local, "--editor", editor); err != nil {
		t.Fatal(err)
	}
	if n := putCount(box, "/myfiles/same.txt"); n != 0 {
		t.Errorf("wrote %d times although both sides already matched", n)
	}
}
