package cli

import (
	"archive/tar"
	"io"
	"os"
	"strings"
	"testing"
)

// tarEntries reads an archive into a map of name to contents.
func tarEntries(t *testing.T, r io.Reader) map[string]string {
	t.Helper()

	out := map[string]string{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("reading the archive: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %s from the archive: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(body)
	}
}

func archiveBox(t *testing.T) *testBox {
	t.Helper()

	box := newTestBox(t)
	box.archiver = true
	box.putFile("/eos/user/e/einstein/data/a.txt", "first")
	box.putFile("/eos/user/e/einstein/data/sub/b.txt", "second")
	return box
}

func TestArchiveWritesOneFileForAWholeDirectory(t *testing.T) {
	box := archiveBox(t)
	t.Chdir(t.TempDir())

	_, stderr, err := run(t, box, "archive", "/eos/user/e/einstein/data")
	if err != nil {
		t.Fatalf("archive: %v (%s)", err, stderr)
	}

	// With no --to the archive is named after what was asked for and lands in
	// the current directory.
	f, err := os.Open("data.tar")
	if err != nil {
		t.Fatalf("opening the archive: %v", err)
	}
	defer f.Close()

	entries := tarEntries(t, f)
	if entries["data/a.txt"] != "first" || entries["data/sub/b.txt"] != "second" {
		t.Fatalf("archive holds %v, want both files under data/", entries)
	}
	if !strings.Contains(stderr, "data.tar") {
		t.Errorf("stderr does not say where the archive went: %q", stderr)
	}

	// One request for the whole tree is the entire point of the command.
	gets := 0
	for _, r := range box.requests {
		if strings.HasPrefix(r, "GET /archiver") || strings.HasPrefix(r, "GET "+testDavPrefix) {
			gets++
		}
	}
	if gets != 1 {
		t.Errorf("made %d download requests, want 1: %v", gets, box.requests)
	}
}

func TestArchiveWritesToStandardOutput(t *testing.T) {
	box := archiveBox(t)

	stdout, _, err := run(t, box, "archive", "--to", "-", "/eos/user/e/einstein/data")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	entries := tarEntries(t, strings.NewReader(stdout))
	if entries["data/a.txt"] != "first" {
		t.Fatalf("archive on standard output holds %v", entries)
	}
}

func TestArchiveKeepsAnExistingFileUnlessForced(t *testing.T) {
	box := archiveBox(t)
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.WriteFile("data.tar", []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := run(t, box, "archive", "/eos/user/e/einstein/data")
	if err == nil {
		t.Fatal("archive overwrote an existing file without being asked")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error does not say how to proceed: %v", err)
	}
	if b, _ := os.ReadFile("data.tar"); string(b) != "precious" {
		t.Errorf("the existing file was modified: %q", b)
	}

	if _, _, err := run(t, box, "archive", "--force", "/eos/user/e/einstein/data"); err != nil {
		t.Fatalf("archive --force: %v", err)
	}
	f, err := os.Open("data.tar")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if entries := tarEntries(t, f); entries["data/a.txt"] != "first" {
		t.Errorf("--force did not replace the file: %v", entries)
	}
}

func TestArchiveNamesSeveralPathsTogether(t *testing.T) {
	box := archiveBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "third")
	t.Chdir(t.TempDir())

	_, _, err := run(t, box, "archive",
		"/eos/user/e/einstein/data", "/eos/user/e/einstein/notes.txt")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	f, err := os.Open("archive.tar")
	if err != nil {
		t.Fatalf("two paths have no name in common, so the archive should be archive.tar: %v", err)
	}
	defer f.Close()

	entries := tarEntries(t, f)
	if entries["data/a.txt"] != "first" || entries["notes.txt"] != "third" {
		t.Fatalf("archive holds %v, want both paths", entries)
	}
}

func TestArchiveRejectsAnUnknownFormat(t *testing.T) {
	box := archiveBox(t)

	_, _, err := run(t, box, "archive", "--format", "rar", "/eos/user/e/einstein/data")
	if err == nil || !strings.Contains(err.Error(), "tar or zip") {
		t.Fatalf("error = %v, want one naming the formats that work", err)
	}
	if len(box.requests) != 0 {
		t.Errorf("a bad format reached the server: %v", box.requests)
	}
}

func TestArchiveSaysWhenTheServerHasNoArchiver(t *testing.T) {
	box := newTestBox(t) // no archiver advertised
	box.putFile("/eos/user/e/einstein/data/a.txt", "first")
	t.Chdir(t.TempDir())

	_, _, err := run(t, box, "archive", "/eos/user/e/einstein/data")
	if err == nil || !strings.Contains(err.Error(), "archiver") {
		t.Fatalf("error = %v, want one saying the server cannot build archives", err)
	}
	if _, statErr := os.Stat("data.tar"); statErr == nil {
		t.Error("a failed archive left a file behind")
	}
}
