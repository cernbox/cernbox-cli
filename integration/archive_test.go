//go:build integration

package integration_test

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archiveTree puts a small directory in CERNBox and returns its remote path.
func (e *env) archiveTree() string {
	e.t.Helper()

	e.mustRun("mkdir", "-p", e.remotePath("tree/sub"))
	e.mustRun("put", e.writeLocal("a.txt", []byte("first")), e.remotePath("tree/a.txt"))
	e.mustRun("put", e.writeLocal("b.txt", []byte("second")), e.remotePath("tree/sub/b.txt"))
	return e.remotePath("tree")
}

// tarNames reads the entry names and bodies out of a tar file.
func tarNames(t *testing.T, p string) map[string]string {
	t.Helper()

	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("opening %s: %v", p, err)
	}
	defer f.Close()

	out := map[string]string{}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %s from %s: %v", hdr.Name, p, err)
		}
		out[strings.TrimSuffix(hdr.Name, "/")] = string(body)
	}
}

func TestArchiveDownloadsATreeAsOneFile(t *testing.T) {
	e := setup(t)
	tree := e.archiveTree()

	out := e.localPath("tree.tar")
	e.mustRun("archive", "--to", out, tree)

	entries := tarNames(t, out)
	if entries["tree/a.txt"] != "first" || entries["tree/sub/b.txt"] != "second" {
		t.Fatalf("the archive holds %v, want both files under tree/", entries)
	}
}

func TestArchiveNamesTheFileAfterTheDirectory(t *testing.T) {
	e := setup(t)
	tree := e.archiveTree()

	// With no --to the archive lands in the working directory under its own
	// name, which is the form somebody types by hand.
	cmd := e.cmd("archive", tree)
	cmd.Dir = e.localDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}

	if entries := tarNames(t, filepath.Join(e.localDir, "tree.tar")); entries["tree/a.txt"] != "first" {
		t.Fatalf("tree.tar holds %v", entries)
	}
}

func TestArchiveCanProduceAZip(t *testing.T) {
	e := setup(t)
	tree := e.archiveTree()

	out := e.localPath("tree.zip")
	e.mustRun("archive", "--format", "zip", "--to", out, tree)

	// The contents are a zip's business; what matters here is that the server
	// built one and the CLI saved all of it.
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 4 || string(b[:2]) != "PK" {
		t.Fatalf("%s is not a zip: first bytes %q", out, b[:min(len(b), 4)])
	}
}

func TestArchiveWritesToAPipe(t *testing.T) {
	e := setup(t)
	tree := e.archiveTree()

	stdout, stderr, code := e.run("archive", "--to", "-", tree)
	if code != 0 {
		t.Fatalf("archive --to -: exit %d: %s", code, stderr)
	}

	// Nothing but the archive may reach standard output, or the program on the
	// other end of the pipe cannot read it.
	tr := tar.NewReader(strings.NewReader(stdout))
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("the stream on standard output is not a tar: %v", err)
		}
		if hdr.Name == "tree/a.txt" {
			found = true
		}
	}
	if !found {
		t.Error("the streamed archive does not contain tree/a.txt")
	}
}

func TestArchiveKeepsAnExistingFile(t *testing.T) {
	e := setup(t)
	tree := e.archiveTree()

	out := e.writeLocal("keep.tar", []byte("precious"))
	_, stderr, code := e.run("archive", "--to", out, tree)
	if code == 0 {
		t.Fatal("archive overwrote an existing file without being asked")
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("the error does not say how to proceed: %s", stderr)
	}
	if b, _ := os.ReadFile(out); string(b) != "precious" {
		t.Errorf("the existing file was modified: %q", b)
	}

	e.mustRun("archive", "--force", "--to", out, tree)
	if entries := tarNames(t, out); entries["tree/a.txt"] != "first" {
		t.Errorf("--force did not replace the file: %v", entries)
	}
}
