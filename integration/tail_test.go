//go:build integration

package integration_test

import (
	"strings"
	"testing"
)

// storageDoesRanges reports whether this deployment can serve a byte range at
// all, and skips the test when it cannot.
//
// EOS here cannot: XRootD answers a ranged GET with chunked framing inside a
// body it has already given a Content-Length, so four bytes of hexadecimal
// arrive in place of four bytes of file. reva forwards it faithfully and the
// client refuses it rather than hand back the wrong bytes. Everything that
// reads part of a file depends on this — following a log, resuming a download —
// so the skip is the honest answer until the storage is fixed, and these tests
// start covering it the moment it is.
func storageDoesRanges(t *testing.T, e *env) bool {
	t.Helper()

	// Its own file, with enough lines that asking for the last one needs an
	// offset. A single-line file is read whole, carries no Range at all, and
	// would make this answer yes on a storage that cannot do ranges.
	probe := e.remotePath("range-probe.txt")
	e.mustRun("put", e.writeLocal("range-probe.txt",
		[]byte("one\ntwo\nthree\nfour\nfive\n")), probe)

	out, stderr, code := e.run("tail", "-n", "1", probe)
	switch {
	case code == 0 && out == "five\n":
		return true
	case strings.Contains(stderr, "chunked framing"):
		t.Skip("the storage cannot serve a byte range: " + strings.TrimSpace(stderr))
	case code != 0:
		t.Fatalf("the range probe failed for another reason: %s", stderr)
	default:
		t.Fatalf("the range probe returned %q, want the last line only", out)
	}
	return false
}

func TestTailShowsTheEndOfAFile(t *testing.T) {
	e := setup(t)

	var sb strings.Builder
	for i := 1; i <= 30; i++ {
		sb.WriteString("line ")
		sb.WriteString(strings.TrimSpace(strings.Repeat(" ", 0)))
		sb.WriteString(itoaInt(i))
		sb.WriteString("\n")
	}
	remote := e.remotePath("job.log")
	e.mustRun("put", e.writeLocal("job.log", []byte(sb.String())), remote)

	if !storageDoesRanges(t, e) {
		return
	}

	out := e.mustRun("tail", "-n", "3", remote)
	if out != "line 28\nline 29\nline 30\n" {
		t.Errorf("got %q, want the last three lines", out)
	}
}

// TestTailFollowPrintsWhatWasAppended is the command's reason to exist: a log
// something else is still writing, followed without downloading it again.
func TestTailFollowPrintsWhatWasAppended(t *testing.T) {
	e := setup(t)

	remote := e.remotePath("growing.log")
	e.mustRun("put", e.writeLocal("growing.log", []byte("first\n")), remote)

	if !storageDoesRanges(t, e) {
		return
	}

	// Appended while the follow is running, by writing the file again with more
	// in it, which is what a job's log looks like from here.
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.mustRun("put", e.writeLocal("growing2.log", []byte("first\nsecond\n")), remote)
	}()

	out, _, code := e.run("--timeout", "20s", "tail", "-f", "--interval", "500ms", remote)
	<-done
	if code != 0 {
		t.Fatalf("following ended with %d:\n%s", code, out)
	}
	if !strings.Contains(out, "second") {
		t.Errorf("the appended line was not followed:\n%s", out)
	}
}

func TestTailRefusesADirectory(t *testing.T) {
	e := setup(t)

	_, stderr, code := e.run("tail", e.remote)
	if code == 0 {
		t.Fatal("tailing a directory should be refused")
	}
	if !strings.Contains(stderr, "is a directory") {
		t.Errorf("got %q", stderr)
	}
}

func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
