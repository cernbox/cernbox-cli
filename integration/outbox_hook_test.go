//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOutboxHookTransformsBeforeUpload is the case this exists for: do
// something to a file before it leaves the machine — strip a screenshot's
// metadata, shrink it — and upload what comes out.
func TestOutboxHookTransformsBeforeUpload(t *testing.T) {
	e := setup(t)

	dir := filepath.Join(e.localDir, "shots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shot.txt"),
		[]byte(strings.Repeat("original ", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	hook := filepath.Join(e.localDir, "shrink.sh")
	if err := os.WriteFile(hook,
		[]byte("#!/bin/sh\nprintf 'shrunk' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	remote := e.remotePath("shots")
	e.mustRun("outbox", "push", dir, "--to", remote, "--settle", "0", "--exec", hook)

	if got := e.mustRun("cat", remote+"/shot.txt"); got != "shrunk" {
		t.Errorf("uploaded %q, want what the hook left", got)
	}

	// A second pass changes nothing: the hook rewrote the file where it was, so
	// the sizes now agree and the file is skipped before the hook is reached.
	// Without that, "after: keep" would shrink and re-upload for ever.
	out, _, code := e.run("outbox", "push", dir, "--to", remote, "--settle", "0", "--exec", hook)
	if code != 0 {
		t.Fatalf("the second pass exited %d: %s", code, out)
	}
	if names := e.names(remote); len(names) != 1 {
		t.Errorf("the folder holds %v, want the one file", names)
	}
}

// TestOutboxHookFailureKeepsTheFileHome: the hook is a precondition, so if it
// refuses, nothing is uploaded and nothing is tidied away — even under a policy
// that would have deleted the local copy.
func TestOutboxHookFailureKeepsTheFileHome(t *testing.T) {
	e := setup(t)

	dir := filepath.Join(e.localDir, "shots2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(dir, "shot.txt")
	if err := os.WriteFile(local, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	hook := filepath.Join(e.localDir, "refuse.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	remote := e.remotePath("shots2")
	e.mustRun("mkdir", remote)
	_, stderr, code := e.run("outbox", "push", dir, "--to", remote,
		"--settle", "0", "--after", "delete", "--exec", hook)
	if code != 0 {
		t.Fatalf("a refusing hook is a warning, not a failure: %s", stderr)
	}
	if !strings.Contains(stderr, "exited with status 7") {
		t.Errorf("the message should say the hook ran and refused:\n%s", stderr)
	}
	if names := e.names(remote); len(names) != 0 {
		t.Errorf("something was uploaded anyway: %v", names)
	}
	if _, err := os.Stat(local); err != nil {
		t.Errorf("the local copy should still be there despite --after delete: %v", err)
	}
}
