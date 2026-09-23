package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/clipboard"
	"github.com/cernbox/cernbox-cli/pkg/pathspec"
)

// The clipboard lives in the caller's home space, which the fake reports as
// /eos/user/e/einstein.
const (
	clipHome = "/eos/user/e/einstein"
	clipRoot = clipHome + "/.cernbox/clipboard"
)

func manifestPath(slot string) string { return clipRoot + "/" + slot + "/manifest.json" }
func payloadPath(slot, name string) string {
	return clipRoot + "/" + slot + "/payload/" + name
}

// storedManifest decodes the manifest the CLI wrote, which is the state the other
// machine would read.
func storedManifest(t *testing.T, box *testBox, slot string) *clipboard.Manifest {
	t.Helper()
	body, ok := box.files[manifestPath(slot)]
	if !ok {
		t.Fatalf("no manifest stored for slot %q; the box holds:\n  %s",
			slot, strings.Join(boxPaths(box), "\n  "))
	}
	m, err := clipboard.Decode([]byte(body))
	if err != nil {
		t.Fatalf("the stored manifest does not decode: %v\n%s", err, body)
	}
	return m
}

func boxPaths(box *testBox) []string {
	var out []string
	for p := range box.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// requested reports whether the box saw a given method against a CERNBox path.
func requested(box *testBox, method, p string) bool {
	return slices.Contains(box.requests, method+" "+testDavPrefix+p)
}

// ── copy ─────────────────────────────────────────────────────────────────────

func TestCopyStagesALocalFile(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	local := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(local, []byte("a report"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := run(t, box, "copy", local)
	if err != nil {
		t.Fatal(err)
	}

	m := storedManifest(t, box, clipboard.DefaultSlot)
	if len(m.Entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(m.Entries), m.Entries)
	}
	e := m.Entries[0]
	if e.Name != "report.pdf" {
		t.Errorf("name = %q, want report.pdf", e.Name)
	}
	if !e.Staged {
		t.Error("a local file has to be staged: the other machine cannot read it otherwise")
	}
	if got := box.files[payloadPath(clipboard.DefaultSlot, "report.pdf")]; got != "a report" {
		t.Errorf("the payload was not uploaded, got %q", got)
	}
	if !strings.Contains(stderr, "cernbox paste") {
		t.Errorf("copy should say how to paste it:\n%s", stderr)
	}
}

// TestCopyReferencesARemotePath is the property that makes this cheap on lxplus:
// a file already in CERNBox is pointed at, not duplicated.
func TestCopyReferencesARemotePath(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/notes.txt", "already in cernbox")

	if _, _, err := run(t, box, "copy", "cb:"+clipHome+"/notes.txt"); err != nil {
		t.Fatal(err)
	}

	m := storedManifest(t, box, clipboard.DefaultSlot)
	if len(m.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(m.Entries))
	}
	e := m.Entries[0]
	if e.Staged {
		t.Error("a path already in CERNBox must not be duplicated")
	}
	if e.Path != clipHome+"/notes.txt" {
		t.Errorf("path = %q, want the original location", e.Path)
	}
	if _, staged := box.files[payloadPath(clipboard.DefaultSlot, "notes.txt")]; staged {
		t.Error("nothing should have been uploaded for a referenced copy")
	}
}

// TestCopyReplacesTheSlot: copying twice leaves one copy, and the bytes the first
// one staged are gone rather than left to sit in the user's quota.
func TestCopyReplacesTheSlot(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	for p, body := range map[string]string{first: "one", second: "two"} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := run(t, box, "copy", first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", second); err != nil {
		t.Fatal(err)
	}

	m := storedManifest(t, box, clipboard.DefaultSlot)
	if len(m.Entries) != 1 || m.Entries[0].Name != "second.txt" {
		t.Fatalf("the slot should hold only the second copy, got %+v", m.Entries)
	}
	if _, left := box.files[payloadPath(clipboard.DefaultSlot, "first.txt")]; left {
		t.Error("the replaced copy's bytes were left behind, which would leak quota")
	}
}

// TestCopyDetectsAConcurrentCopyToTheSameSlot: the clipboard is shared state, so
// two machines can genuinely race on it. reva honours If-Match on PUT, so the
// loser can be told rather than silently overwriting the winner and orphaning
// whatever the winner had staged.
func TestCopyDetectsAConcurrentCopyToTheSameSlot(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/one.txt", "one")
	box.putFile(clipHome+"/two.txt", "two")

	// A first copy, so there is a manifest with an ETag to be conditional on.
	if _, _, err := run(t, box, "copy", "cb:"+clipHome+"/one.txt"); err != nil {
		t.Fatal(err)
	}
	before := box.files[manifestPath(clipboard.DefaultSlot)]

	// Another machine lands its own copy in the window between this one reading
	// the manifest and writing it back.
	box.beforePut = func(p string) {
		if p == manifestPath(clipboard.DefaultSlot) {
			box.beforePut = nil
			box.bumpETag(p)
		}
	}

	_, _, err := run(t, box, "copy", "cb:"+clipHome+"/two.txt")
	if err == nil {
		t.Fatal("a concurrent copy to the same slot should be reported, not silently overwritten")
	}
	if !strings.Contains(err.Error(), "another computer") {
		t.Errorf("the error should say what happened: %v", err)
	}
	if got := box.files[manifestPath(clipboard.DefaultSlot)]; got != before {
		t.Error("the losing copy overwrote the slot anyway")
	}
}

// TestAFailedCopyLeavesThePreviousCopyPastable: staging happens before the
// manifest is replaced, so a copy that dies halfway leaves the old one whole
// rather than an empty slot — or worse, a manifest pointing at deleted bytes.
func TestAFailedCopyLeavesThePreviousCopyPastable(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	local := filepath.Join(t.TempDir(), "good.txt")
	if err := os.WriteFile(local, []byte("the good copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", local); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(t.TempDir(), "not-there.txt")
	if _, _, err := run(t, box, "copy", missing); err == nil {
		t.Fatal("copying a file that does not exist should fail")
	}

	m := storedManifest(t, box, clipboard.DefaultSlot)
	if len(m.Entries) != 1 || m.Entries[0].Name != "good.txt" {
		t.Fatalf("the failed copy damaged the slot: %+v", m.Entries)
	}
	if got := box.files[payloadPath(clipboard.DefaultSlot, "good.txt")]; got != "the good copy" {
		t.Errorf("the previous copy's bytes are gone: %q", got)
	}

	// And it still pastes.
	dest := t.TempDir()
	if _, _, err := run(t, box, "paste", dest); err != nil {
		t.Fatalf("the surviving copy should still paste: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "good.txt")); string(got) != "the good copy" {
		t.Errorf("pasted %q", got)
	}
}

// TestCopyKeepsAStagedNameItIsReplacing: the cleanup after a copy commits must
// delete only what the new manifest does not reference. Replacing a copy with one
// of the same name writes the payload back over itself, so a cleanup that went by
// name alone would delete what it had just uploaded.
func TestCopyKeepsAStagedNameItIsReplacing(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	dir := t.TempDir()
	local := filepath.Join(dir, "same.txt")

	if err := os.WriteFile(local, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", local); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", local); err != nil {
		t.Fatal(err)
	}

	if got := box.files[payloadPath(clipboard.DefaultSlot, "same.txt")]; got != "second" {
		t.Errorf("the staged payload is %q, want the second copy", got)
	}
	dest := t.TempDir()
	if _, _, err := run(t, box, "paste", dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "same.txt")); string(got) != "second" {
		t.Errorf("pasted %q, want the second copy", got)
	}
}

func TestCopyRefusesADirectoryWithoutRecursive(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	dir := t.TempDir()

	_, _, err := run(t, box, "copy", dir)
	if err == nil {
		t.Fatal("copying a directory without -r should fail, as cp does")
	}
	if !strings.Contains(err.Error(), "-r") {
		t.Errorf("the error should name the flag: %v", err)
	}
}

func TestCopyRefusesARemoteDirectoryWithoutRecursive(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome + "/data")

	if _, _, err := run(t, box, "copy", "cb:"+clipHome+"/data"); err == nil {
		t.Fatal("copying a remote directory without -r should fail")
	}
}

// TestCopyRejectsTwoArgumentsWithTheSameName: they would land on top of each
// other both in the slot and wherever they were pasted, so one would be lost.
func TestCopyRejectsTwoArgumentsWithTheSameName(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	dir := t.TempDir()
	for _, sub := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "same.txt"), []byte(sub), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := run(t, box, "copy", filepath.Join(dir, "a", "same.txt"), filepath.Join(dir, "b", "same.txt"))
	if err == nil {
		t.Fatal("two arguments with the same base name should be refused")
	}
	if !strings.Contains(err.Error(), "same.txt") {
		t.Errorf("the error should name the collision: %v", err)
	}
}

func TestCopyValidatesTheSlotName(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/notes.txt", "x")

	_, _, err := run(t, box, "copy", "--slot", "../escape", "cb:"+clipHome+"/notes.txt")
	if err == nil {
		t.Fatal("a slot name with a path separator should be refused")
	}
	if _, wrote := box.files[clipHome+"/.cernbox/escape/manifest.json"]; wrote {
		t.Error("the slot name escaped the clipboard directory")
	}
}

// ── paste ────────────────────────────────────────────────────────────────────

func TestCopyAndPasteRoundTrip(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	local := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(local, []byte("a report"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", local); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if _, _, err := run(t, box, "paste", dest); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a report" {
		t.Errorf("pasted %q, want %q", got, "a report")
	}
}

// TestPasteDefaultsToTheWorkingDirectory: "cernbox copy x" then "cernbox paste"
// with no argument is the whole point, so the no-argument form is worth pinning.
func TestPasteDefaultsToTheWorkingDirectory(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	local := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", local); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	if _, _, err := run(t, box, "paste"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile("notes.txt"); err != nil || string(got) != "hello" {
		t.Errorf("paste with no destination did not land in the working directory: %q, %v", got, err)
	}
}

// TestPasteLeavesTheClipboardIntact is the decision this feature was built
// around: pasting is not consuming, so the same copy reaches several machines.
func TestPasteLeavesTheClipboardIntact(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	local := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", local); err != nil {
		t.Fatal(err)
	}

	first := t.TempDir()
	second := t.TempDir()
	if _, stderr, err := run(t, box, "paste", first); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(stderr, "Still on the clipboard") {
		t.Errorf("paste should say the clipboard survives:\n%s", stderr)
	}
	if _, _, err := run(t, box, "paste", second); err != nil {
		t.Fatalf("the second paste should work too: %v", err)
	}

	for _, dir := range []string{first, second} {
		if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
			t.Errorf("%s did not receive the file: %v", dir, err)
		}
	}
	if _, ok := box.files[manifestPath(clipboard.DefaultSlot)]; !ok {
		t.Error("pasting must not remove the manifest")
	}
}

// TestPasteToARemotePathMovesNoData: a referenced copy pasted to another CERNBox
// path is a server-side COPY, so neither half of the transfer happens.
func TestPasteToARemotePathMovesNoData(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/notes.txt", "already in cernbox")
	if _, _, err := run(t, box, "copy", "cb:"+clipHome+"/notes.txt"); err != nil {
		t.Fatal(err)
	}

	box.requests = nil
	_, stderr, err := run(t, box, "paste", "cb:"+clipHome+"/incoming/")
	if err != nil {
		t.Fatal(err)
	}

	if !requested(box, "COPY", clipHome+"/notes.txt") {
		t.Errorf("the paste should have been a server-side COPY; requests:\n  %s",
			strings.Join(box.requests, "\n  "))
	}
	if requested(box, "GET", clipHome+"/notes.txt") {
		t.Error("the file should not have been downloaded to be pasted inside CERNBox")
	}
	if got := box.files[clipHome+"/incoming/notes.txt"]; got != "already in cernbox" {
		t.Errorf("the destination holds %q", got)
	}
	if !strings.Contains(stderr, "nothing was transferred") {
		t.Errorf("paste should say the transfer was server-side:\n%s", stderr)
	}
}

func TestPasteRefusesAnExistingDestination(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	local := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(local, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", local); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	existing := filepath.Join(dest, "notes.txt")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := run(t, box, "paste", dest)
	if err == nil {
		t.Fatal("paste should refuse to overwrite without -f")
	}
	if !strings.Contains(err.Error(), "-f") {
		t.Errorf("the error should name the flag: %v", err)
	}
	if got, _ := os.ReadFile(existing); string(got) != "old" {
		t.Errorf("the existing file was overwritten anyway: %q", got)
	}

	if _, _, err := run(t, box, "paste", "-f", dest); err != nil {
		t.Fatalf("-f should allow it: %v", err)
	}
	if got, _ := os.ReadFile(existing); string(got) != "new" {
		t.Errorf("-f did not overwrite: %q", got)
	}
}

func TestPasteFromAnEmptySlotSaysSo(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)

	_, _, err := run(t, box, "paste", t.TempDir())
	if err == nil {
		t.Fatal("pasting with nothing copied should fail")
	}
	if !strings.Contains(err.Error(), "copied") {
		t.Errorf("the error should say nothing has been copied: %v", err)
	}
}

// TestPasteSeveralItemsNeedsADirectory: cp's rule, so that two items cannot
// silently collapse onto one name.
func TestPasteSeveralItemsNeedsADirectory(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := run(t, box, "copy", filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "not-a-directory")
	if _, _, err := run(t, box, "paste", target); err == nil {
		t.Fatal("pasting two items onto one name should be refused")
	}

	dest := t.TempDir()
	if _, _, err := run(t, box, "paste", dest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if got, err := os.ReadFile(filepath.Join(dest, name)); err != nil || string(got) != name {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
}

// TestPasteReportsAReferenceThatIsGone: a referenced copy is the user's own file,
// which they may have moved since. The raw 404 names a path and explains nothing.
func TestPasteReportsAReferenceThatIsGone(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/notes.txt", "here for now")
	if _, _, err := run(t, box, "copy", "cb:"+clipHome+"/notes.txt"); err != nil {
		t.Fatal(err)
	}

	delete(box.files, clipHome+"/notes.txt")

	_, _, err := run(t, box, "paste", t.TempDir())
	if err == nil {
		t.Fatal("pasting a reference whose source is gone should fail")
	}
	if !strings.Contains(err.Error(), "moved or deleted") {
		t.Errorf("the error should explain why it is stale: %v", err)
	}
}

// ── standard input and output ────────────────────────────────────────────────

// TestCopyStdinAndPasteStdout is the cross-machine pipe: tar on one side, untar
// on the other, with nothing touching the local filesystem in between.
func TestCopyStdinAndPasteStdout(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)

	if _, _, err := runStdin(t, box, "tarball bytes", "copy", "-", "--name", "dir.tgz"); err != nil {
		t.Fatal(err)
	}

	m := storedManifest(t, box, clipboard.DefaultSlot)
	if len(m.Entries) != 1 || m.Entries[0].Name != "dir.tgz" {
		t.Fatalf("--name did not set the entry name: %+v", m.Entries)
	}
	if !m.Entries[0].Staged {
		t.Error("standard input can only ever be staged")
	}

	stdout, _, err := run(t, box, "paste", "-")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "tarball bytes" {
		t.Errorf("paste - wrote %q", stdout)
	}
}

func TestCopyStdinDefaultsToTheNameStdin(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)

	if _, _, err := runStdin(t, box, "bytes", "copy", "-"); err != nil {
		t.Fatal(err)
	}
	m := storedManifest(t, box, clipboard.DefaultSlot)
	if len(m.Entries) != 1 || m.Entries[0].Name != "stdin" {
		t.Fatalf("entries = %+v, want one called stdin", m.Entries)
	}
}

func TestCopyRefusesStandardInputTwice(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)

	if _, _, err := runStdin(t, box, "bytes", "copy", "-", "-"); err == nil {
		t.Fatal("standard input cannot be copied twice: the second read would be empty")
	}
}

func TestPasteToStdoutNeedsExactlyOneItem(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := run(t, box, "copy", filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, box, "paste", "-"); err == nil {
		t.Fatal("two items cannot be written to standard output")
	}
}

// ── streaming ────────────────────────────────────────────────────────────────

// smallChunkConfig writes a configuration with a tiny transfer chunk, so that a
// test can reach the split-stream path without pushing megabytes through a fake
// server. --config wins over the environment, so this is the whole setup.
func smallChunkConfig(t *testing.T, chunk string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := "transfer:\n  chunk_size: " + chunk + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// streamPayload is recognisable at any offset, so a wrongly ordered or dropped
// piece shows up as a content mismatch rather than as a length that happens to
// match.
func streamPayload(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%06d-", i)
	}
	return b.String()[:n]
}

// TestCopyStreamsALargeStdinInPieces: a pipe has no knowable length and reva needs
// one, so a stream too big for a single request is cut into pieces rather than
// spooled to local disk to be measured.
func TestCopyStreamsALargeStdinInPieces(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	cfg := smallChunkConfig(t, "1K")
	body := streamPayload(5000)

	if _, _, err := runStdin(t, box, body, "--config", cfg, "copy", "-", "--name", "big.bin"); err != nil {
		t.Fatal(err)
	}

	m := storedManifest(t, box, clipboard.DefaultSlot)
	if len(m.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(m.Entries))
	}
	e := m.Entries[0]
	// 5000 bytes at 1024 per piece is five: four full and one of 904.
	if e.Parts != 5 {
		t.Errorf("Parts = %d, want 5", e.Parts)
	}
	if e.Size != 5000 {
		t.Errorf("Size = %d, want 5000", e.Size)
	}
	if !e.Staged {
		t.Error("a stream can only ever be staged")
	}

	// The pieces are really there, in order, and together they are the stream.
	var joined strings.Builder
	for i := range e.Parts {
		piece, ok := box.files[clipboard.PartPath(e.Path, i)]
		if !ok {
			t.Fatalf("piece %d is missing from %s", i, e.Path)
		}
		joined.WriteString(piece)
	}
	if joined.String() != body {
		t.Errorf("the pieces do not reassemble into the stream (%d bytes vs %d)",
			joined.Len(), len(body))
	}
}

// TestCopyKeepsASmallStreamWhole: only a stream that does not fit in one request
// is split, so the ordinary case of a small pipe stays an ordinary entry.
func TestCopyKeepsASmallStreamWhole(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	cfg := smallChunkConfig(t, "1K")

	if _, _, err := runStdin(t, box, "short", "--config", cfg, "copy", "-"); err != nil {
		t.Fatal(err)
	}
	m := storedManifest(t, box, clipboard.DefaultSlot)
	if got := m.Entries[0].Parts; got != 0 {
		t.Errorf("Parts = %d, want 0 for a stream that fits in one piece", got)
	}
	if got := box.files[payloadPath(clipboard.DefaultSlot, "stdin")]; got != "short" {
		t.Errorf("the payload is %q", got)
	}
}

// TestCopyStreamsAnExactMultipleOfTheChunk: the boundary case, where the last read
// fills the buffer exactly and the one after it sees end of file.
func TestCopyStreamsAnExactMultipleOfTheChunk(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	cfg := smallChunkConfig(t, "1K")
	body := streamPayload(3072) // exactly three pieces

	if _, _, err := runStdin(t, box, body, "--config", cfg, "copy", "-"); err != nil {
		t.Fatal(err)
	}
	e := storedManifest(t, box, clipboard.DefaultSlot).Entries[0]
	if e.Parts != 3 || e.Size != 3072 {
		t.Errorf("Parts = %d, Size = %d; want 3 and 3072", e.Parts, e.Size)
	}
	if _, extra := box.files[clipboard.PartPath(e.Path, 3)]; extra {
		t.Error("an empty trailing piece was written")
	}
}

func TestPasteJoinsAStreamedCopy(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	cfg := smallChunkConfig(t, "1K")
	body := streamPayload(4097)

	if _, _, err := runStdin(t, box, body, "--config", cfg, "copy", "-", "--name", "joined.bin"); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if _, _, err := run(t, box, "paste", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "joined.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("pasted %d bytes, want %d", len(got), len(body))
	}
	// The temporary file the join writes through must not be left behind.
	if _, err := os.Stat(filepath.Join(dest, "joined.bin.part")); err == nil {
		t.Error("the .part file was left behind")
	}
}

func TestPasteStreamedCopyToStandardOutput(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	cfg := smallChunkConfig(t, "1K")
	body := streamPayload(2600)

	if _, _, err := runStdin(t, box, body, "--config", cfg, "copy", "-"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := run(t, box, "paste", "-")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != body {
		t.Errorf("paste - wrote %d bytes, want %d", len(stdout), len(body))
	}
}

// TestPasteStreamedCopyToARemotePath: a server-side COPY cannot join pieces, so
// this is the one paste inside CERNBox that moves data — through a pipe, so the
// whole thing is never held anywhere.
func TestPasteStreamedCopyToARemotePath(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	cfg := smallChunkConfig(t, "1K")
	body := streamPayload(3000)

	if _, _, err := runStdin(t, box, body, "--config", cfg, "copy", "-", "--name", "remote.bin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "paste", "cb:"+clipHome+"/landing/"); err != nil {
		t.Fatal(err)
	}
	if got := box.files[clipHome+"/landing/remote.bin"]; got != body {
		t.Errorf("the remote copy is %d bytes, want %d", len(got), len(body))
	}
}

// TestClearReleasesEveryPieceOfAStream: the pieces live in a directory precisely so
// that this is one recursive delete, and none is left paying for quota.
func TestClearReleasesEveryPieceOfAStream(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	cfg := smallChunkConfig(t, "1K")

	if _, _, err := runStdin(t, box, streamPayload(4000), "--config", cfg, "copy", "-"); err != nil {
		t.Fatal(err)
	}
	e := storedManifest(t, box, clipboard.DefaultSlot).Entries[0]

	if _, _, err := run(t, box, "clipboard", "clear"); err != nil {
		t.Fatal(err)
	}
	for i := range e.Parts {
		if _, left := box.files[clipboard.PartPath(e.Path, i)]; left {
			t.Errorf("piece %d survived the clear", i)
		}
	}
}

// TestReplacingAStreamedCopyReleasesItsPieces: the same for the cleanup that runs
// after a copy commits.
func TestReplacingAStreamedCopyReleasesItsPieces(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	cfg := smallChunkConfig(t, "1K")

	if _, _, err := runStdin(t, box, streamPayload(4000), "--config", cfg, "copy", "-"); err != nil {
		t.Fatal(err)
	}
	first := storedManifest(t, box, clipboard.DefaultSlot).Entries[0]

	box.putFile(clipHome+"/plain.txt", "a plain file")
	if _, _, err := run(t, box, "copy", "cb:"+clipHome+"/plain.txt"); err != nil {
		t.Fatal(err)
	}
	for i := range first.Parts {
		if _, left := box.files[clipboard.PartPath(first.Path, i)]; left {
			t.Errorf("piece %d of the replaced stream was left behind", i)
		}
	}
}

// ── clipboard list and clear ─────────────────────────────────────────────────

func TestClipboardListShowsWhatWasCopied(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	local := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(local, []byte("a report"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", "--slot", "build", local); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := run(t, box, "clipboard", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SLOT", "build", "report.pdf"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the listing is missing %q:\n%s", want, stdout)
		}
	}
}

func TestClipboardListIsEmptyWhenNothingWasCopied(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)

	stdout, stderr, err := run(t, box, "clipboard", "list")
	if err != nil {
		t.Fatalf("an empty clipboard is not an error: %v", err)
	}
	if !strings.Contains(stderr, "empty") {
		t.Errorf("it should say the clipboard is empty:\n%s", stderr)
	}
	if strings.Contains(stdout, "manifest") {
		t.Errorf("the manifest should never appear as a slot:\n%s", stdout)
	}
}

func TestClipboardClearEmptiesTheSlot(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	local := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(local, []byte("a report"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, box, "copy", local); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := run(t, box, "clipboard", "clear")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Cleared") {
		t.Errorf("clear should say what it did:\n%s", stderr)
	}
	if _, left := box.files[manifestPath(clipboard.DefaultSlot)]; left {
		t.Error("the manifest survived a clear")
	}
	if _, left := box.files[payloadPath(clipboard.DefaultSlot, "report.pdf")]; left {
		t.Error("the staged bytes survived a clear, which would leak quota")
	}
}

// TestClipboardClearLeavesReferencedFilesAlone: a referenced copy points at the
// user's real file. Clearing the clipboard must not delete it.
func TestClipboardClearLeavesReferencedFilesAlone(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/notes.txt", "the real file")
	if _, _, err := run(t, box, "copy", "cb:"+clipHome+"/notes.txt"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, box, "clipboard", "clear"); err != nil {
		t.Fatal(err)
	}
	if got := box.files[clipHome+"/notes.txt"]; got != "the real file" {
		t.Errorf("clearing the clipboard deleted the referenced file (got %q)", got)
	}
}

func TestClipboardClearIsIdempotent(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)

	_, stderr, err := run(t, box, "clipboard", "clear")
	if err != nil {
		t.Fatalf("clearing an empty slot is not an error: %v", err)
	}
	if !strings.Contains(stderr, "already empty") {
		t.Errorf("it should say there was nothing there:\n%s", stderr)
	}
}

func TestClipboardClearAll(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	local := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(local, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"one", "two"} {
		if _, _, err := run(t, box, "copy", "--slot", slot, local); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := run(t, box, "clipboard", "clear", "--all"); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"one", "two"} {
		if _, left := box.files[manifestPath(slot)]; left {
			t.Errorf("slot %q survived --all", slot)
		}
	}
}

func TestClipboardClearAllRejectsASlotName(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)

	if _, _, err := run(t, box, "clipboard", "clear", "--all", "one"); err == nil {
		t.Fatal("--all with a slot name is contradictory and should be refused")
	}
}

// ── expiry ───────────────────────────────────────────────────────────────────

// TestExpiredSlotIsCollected: nothing runs on a schedule to reclaim staged
// bytes, so the commands that look at the clipboard do it on the way past.
func TestExpiredSlotIsCollected(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)

	stale := &clipboard.Manifest{
		Version: clipboard.Version,
		Slot:    "old",
		Created: time.Now().Add(-30 * 24 * time.Hour),
		Expires: time.Now().Add(-24 * time.Hour),
		Entries: []clipboard.Entry{{
			Name:   "gone.txt",
			Path:   payloadPath("old", "gone.txt"),
			Size:   3,
			Staged: true,
		}},
	}
	body, err := stale.Encode()
	if err != nil {
		t.Fatal(err)
	}
	box.putFile(manifestPath("old"), string(body))
	box.putFile(payloadPath("old", "gone.txt"), "old")

	if _, _, err := run(t, box, "clipboard", "list"); err != nil {
		t.Fatal(err)
	}
	if _, left := box.files[manifestPath("old")]; left {
		t.Error("an expired slot should have been collected")
	}
	if _, left := box.files[payloadPath("old", "gone.txt")]; left {
		t.Error("the expired slot's staged bytes should have been collected too")
	}
}

// TestCopyWithoutATTLNeverExpires: --ttl 0 means "keep it until I say
// otherwise", which a collector must honour.
func TestCopyWithoutATTLNeverExpires(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/notes.txt", "x")

	if _, _, err := run(t, box, "copy", "--ttl", "0", "cb:"+clipHome+"/notes.txt"); err != nil {
		t.Fatal(err)
	}
	m := storedManifest(t, box, clipboard.DefaultSlot)
	if !m.Expires.IsZero() {
		t.Errorf("expires = %v, want no expiry", m.Expires)
	}

	stdout, _, err := run(t, box, "clipboard", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "never") {
		t.Errorf("the listing should say it never expires:\n%s", stdout)
	}
}

// TestPasteWarnsAboutAnExpiredCopy: the bytes are evidently still there, so
// refusing would be unhelpful — but handing over a stale copy silently is worse.
func TestPasteWarnsAboutAnExpiredCopy(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/notes.txt", "still here")

	expired := &clipboard.Manifest{
		Version: clipboard.Version,
		Slot:    clipboard.DefaultSlot,
		Created: time.Now().Add(-30 * 24 * time.Hour),
		Expires: time.Now().Add(-24 * time.Hour),
		Entries: []clipboard.Entry{{Name: "notes.txt", Path: clipHome + "/notes.txt", Size: 10}},
	}
	body, err := expired.Encode()
	if err != nil {
		t.Fatal(err)
	}
	box.putFile(manifestPath(clipboard.DefaultSlot), string(body))

	// paste reads the slot directly, so the collector in "clipboard list" has not
	// had a chance to remove it.
	_, stderr, err := run(t, box, "paste", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "expired") {
		t.Errorf("paste should warn that the copy is stale:\n%s", stderr)
	}
}

// ── machine-readable output ──────────────────────────────────────────────────

func TestCopyJSONEmitsTheManifest(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/notes.txt", "x")

	stdout, _, err := run(t, box, "--output", "json", "copy", "cb:"+clipHome+"/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	var m clipboard.Manifest
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("copy --output json is not valid JSON: %v\n%s", err, stdout)
	}
	if len(m.Entries) != 1 || m.Entries[0].Name != "notes.txt" {
		t.Errorf("the manifest does not describe the copy: %+v", m)
	}
}

func TestClipboardListJSON(t *testing.T) {
	box := newTestBox(t)
	box.mkdir(clipHome)
	box.putFile(clipHome+"/notes.txt", "x")
	if _, _, err := run(t, box, "copy", "cb:"+clipHome+"/notes.txt"); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := run(t, box, "--output", "json", "clipboard", "list")
	if err != nil {
		t.Fatal(err)
	}
	var got []clipboard.Manifest
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("clipboard list --output json is not valid JSON: %v\n%s", err, stdout)
	}
	if len(got) != 1 || got[0].Slot != clipboard.DefaultSlot {
		t.Errorf("got %+v", got)
	}
}

// ── display helpers ──────────────────────────────────────────────────────────

func TestAgoColumn(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, "-"},
		{now, "just now"},
		{now.Add(30 * time.Second), "just now"}, // clock skew between machines
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-50 * time.Hour), "2d ago"},
	}
	for _, c := range cases {
		if got := agoColumn(c.at, now); got != c.want {
			t.Errorf("agoColumn(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestContentsColumn(t *testing.T) {
	cases := []struct {
		m    *clipboard.Manifest
		want string
	}{
		{&clipboard.Manifest{}, "-"},
		{&clipboard.Manifest{Entries: []clipboard.Entry{{Name: "a.txt"}}}, "a.txt"},
		{&clipboard.Manifest{Entries: []clipboard.Entry{{Name: "data", IsDir: true}}}, "data/"},
		{&clipboard.Manifest{Entries: []clipboard.Entry{
			{Name: "a.txt"}, {Name: "b.txt"}, {Name: "c.txt"},
		}}, "a.txt +2 more"},
	}
	for _, c := range cases {
		if got := contentsColumn(c.m); got != c.want {
			t.Errorf("contentsColumn = %q, want %q", got, c.want)
		}
	}
}

// TestProseSize: HumanSize prints "21" for 21 bytes, which is right in a size
// column and reads as nonsense in "Copied 1 item (21)".
func TestProseSize(t *testing.T) {
	cases := map[int64]string{
		0:       "0 bytes",
		1:       "1 byte",
		21:      "21 bytes",
		1023:    "1023 bytes",
		1024:    "1.0K",
		5 << 20: "5.0M",
		3 << 30: "3.0G",
	}
	for in, want := range cases {
		if got := proseSize(in); got != want {
			t.Errorf("proseSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestItemCountAndSlotArg(t *testing.T) {
	if got := itemCount(1); got != "1 item" {
		t.Errorf("itemCount(1) = %q", got)
	}
	if got := itemCount(3); got != "3 items" {
		t.Errorf("itemCount(3) = %q", got)
	}
	// The hints these produce are meant to be commands the reader can type, so
	// the default slot contributes nothing at all.
	if got := slotArg(clipboard.DefaultSlot); got != "" {
		t.Errorf("slotArg(default) = %q, want empty", got)
	}
	if got := slotArg("build"); got != " build" {
		t.Errorf("slotArg(build) = %q", got)
	}
	if got := pasteHint(clipboard.DefaultSlot); got != "cernbox paste" {
		t.Errorf("pasteHint(default) = %q", got)
	}
	if got := pasteHint("build"); got != "cernbox paste --slot build" {
		t.Errorf("pasteHint(build) = %q", got)
	}
}

func TestPasteTargets(t *testing.T) {
	two := &clipboard.Manifest{Entries: []clipboard.Entry{{Name: "a"}, {Name: "b"}}}
	one := &clipboard.Manifest{Entries: []clipboard.Entry{{Name: "a"}}}

	got, err := pasteTargets(two, "/dest", true, filepath.Join)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join("/dest", "a"), filepath.Join("/dest", "b")}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if _, err := pasteTargets(two, "/dest/name", false, filepath.Join); err == nil {
		t.Error("two items onto one name should be refused")
	}

	got, err = pasteTargets(one, "/dest/name", false, filepath.Join)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"/dest/name"}) {
		t.Errorf("got %v", got)
	}
}

func TestLooksLikeCERNBoxMount(t *testing.T) {
	// This only ever drives a hint. It must never be the thing that decides
	// local versus remote, which is pathspec's job and needs cb: to be explicit.
	if !looksLikeCERNBoxMount("/eos/user/g/gdelmont/x") {
		t.Error("an /eos path should raise the hint")
	}
	if looksLikeCERNBoxMount("/home/gdelmont/x") {
		t.Error("an ordinary path should not")
	}
}

func TestAnyStaged(t *testing.T) {
	remote, err := pathspec.ParseTransfer("cb:/eos/user/e/einstein/x")
	if err != nil {
		t.Fatal(err)
	}
	local, err := pathspec.ParseTransfer("./x")
	if err != nil {
		t.Fatal(err)
	}

	if anyStaged([]copySource{{spec: remote}}) {
		t.Error("a slot of pure references needs no payload directory")
	}
	if !anyStaged([]copySource{{spec: remote}, {spec: local}}) {
		t.Error("one local source is enough to need a payload directory")
	}
	if !anyStaged([]copySource{{stdin: true}}) {
		t.Error("standard input always has to be staged")
	}
}

func TestManifestExpiry(t *testing.T) {
	if got := manifestExpiry(&clipboard.Manifest{}); got != "never" {
		t.Errorf("no expiry rendered as %q", got)
	}
	past := &clipboard.Manifest{Expires: time.Now().Add(-time.Hour)}
	if got := manifestExpiry(past); got != "expired" {
		t.Errorf("a past expiry rendered as %q", got)
	}
	future := &clipboard.Manifest{Expires: time.Now().Add(48 * time.Hour)}
	if got := manifestExpiry(future); got == "expired" || got == "never" {
		t.Errorf("a future expiry rendered as %q", got)
	}
}

func TestEntryLabel(t *testing.T) {
	if got := entryLabel(clipboard.Entry{Name: "a"}); got != "a" {
		t.Errorf("got %q", got)
	}
	if got := entryLabel(clipboard.Entry{Name: "a", IsDir: true}); got != "a/" {
		t.Errorf("got %q", got)
	}
}

func TestWorkingDirIsRecorded(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// macOS reports /private/var... for /var..., so compare the resolved forms.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(workingDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("workingDir() = %q, want %q", got, want)
	}
}
