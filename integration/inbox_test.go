//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInboxCollectsAnArrival is the inbox's reason to exist: a CERNBox folder
// somebody else writes into, whose arrivals turn up locally.
func TestInboxCollectsAnArrival(t *testing.T) {
	e := setup(t)

	remote := e.remotePath("incoming")
	e.mustRun("mkdir", remote)
	e.mustRun("put", e.writeLocal("arrival.txt", []byte("from a collaborator")),
		remote+"/arrival.txt")

	local := filepath.Join(e.localDir, "collected")
	e.mustRun("inbox", "pull", remote, "--to", local, "--settle", "0")

	got, err := os.ReadFile(filepath.Join(local, "arrival.txt"))
	if err != nil {
		t.Fatalf("the arrival was not collected: %v", err)
	}
	if string(got) != "from a collaborator" {
		t.Errorf("got %q", got)
	}

	// A second pass must not fetch it again, which is what makes this usable
	// from a timer.
	_, stderr, code := e.run("inbox", "pull", remote, "--to", local, "--settle", "0")
	if code != 0 {
		t.Fatalf("the second pass exited %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "already here") {
		t.Errorf("the second pass should have skipped it: %s", stderr)
	}
}

// TestInboxStatusExplainsWhatItIsWaitingFor: a file that has only just been
// written is listed as still arriving rather than left out with no reason.
func TestInboxStatusExplainsWhatItIsWaitingFor(t *testing.T) {
	e := setup(t)

	remote := e.remotePath("incoming")
	e.mustRun("mkdir", remote)
	e.mustRun("put", e.writeLocal("fresh.txt", []byte("just now")), remote+"/fresh.txt")

	local := filepath.Join(e.localDir, "collected")
	out := e.mustRun("inbox", "status", remote, "--to", local, "--settle", "1h")
	if !strings.Contains(out, "still arriving") {
		t.Errorf("a file written a moment ago should be waiting:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(local, "fresh.txt")); !os.IsNotExist(err) {
		t.Error("status downloaded something")
	}
}

// TestInboxAfterMoveClearsTheFolder is how an inbox stays an inbox: once a file
// is safely down, the CERNBox copy goes out of the way.
func TestInboxAfterMoveClearsTheFolder(t *testing.T) {
	e := setup(t)

	remote := e.remotePath("incoming")
	e.mustRun("mkdir", remote)
	e.mustRun("put", e.writeLocal("arrival.txt", []byte("collected")), remote+"/arrival.txt")

	local := filepath.Join(e.localDir, "collected")
	e.mustRun("inbox", "pull", remote, "--to", local, "--settle", "0", "--after", "move")

	for _, name := range e.names(remote) {
		if name == "arrival.txt" {
			t.Error("the CERNBox copy was left in the inbox")
		}
	}
	if got := e.names(remote + "/.collected"); len(got) != 1 || got[0] != "arrival.txt" {
		t.Errorf(".collected holds %v, want the one file", got)
	}
}

// TestInboxAfterDeleteVerifiesBeforeRemoving: deleting somebody's only copy on
// the strength of an assumption is not good enough, so that policy checksums
// the transfer. What this checks is the outcome — the file is here, and there
// it is gone.
func TestInboxAfterDeleteVerifiesBeforeRemoving(t *testing.T) {
	e := setup(t)

	remote := e.remotePath("incoming")
	e.mustRun("mkdir", remote)
	body := strings.Repeat("payload\n", 1000)
	e.mustRun("put", e.writeLocal("big.txt", []byte(body)), remote+"/big.txt")

	local := filepath.Join(e.localDir, "collected")
	e.mustRun("inbox", "pull", remote, "--to", local, "--settle", "0", "--after", "delete")

	got, err := os.ReadFile(filepath.Join(local, "big.txt"))
	if err != nil {
		t.Fatalf("the local copy is missing after a delete policy: %v", err)
	}
	if string(got) != body {
		t.Errorf("the local copy is %d bytes, want %d", len(got), len(body))
	}
	if names := e.names(remote); len(names) != 0 {
		t.Errorf("the CERNBox copy is still there: %v", names)
	}
}

func TestInboxNeedsADestination(t *testing.T) {
	e := setup(t)

	_, stderr, code := e.run("inbox", "pull", e.remote)
	if code == 0 {
		t.Fatal("an inbox folder with nowhere to go should be refused")
	}
	if !strings.Contains(stderr, "--to") {
		t.Errorf("the message should name the flag: %s", stderr)
	}
}

// TestInboxWatchCollectsOnATimer drives the watch, which unlike the outbox's
// has to ask rather than be told: nothing on this surface announces that a
// remote folder changed.
func TestInboxWatchCollectsOnATimer(t *testing.T) {
	e := setup(t)

	remote := e.remotePath("incoming")
	e.mustRun("mkdir", remote)
	local := filepath.Join(e.localDir, "collected")

	// Put the file there while the watch is running, so the pass that collects
	// it is one the timer drove and not the first sweep.
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.mustRun("put", e.writeLocal("late.txt", []byte("arrived later")),
			remote+"/late.txt")
	}()

	out, stderr, code := e.run("--timeout", "20s", "inbox", "watch", remote,
		"--to", local, "--settle", "0", "--interval", "1s")
	<-done
	if code != 0 {
		t.Fatalf("watching ended with %d:\n%s\n%s", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(local, "late.txt")); err != nil {
		t.Errorf("the watch did not collect the arrival: %v\n%s", err, stderr)
	}
}
