//go:build integration

package integration_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// completeLine asks the CLI for completions the way a shell does, and returns
// the candidates without their descriptions.
func (e *env) completeLine(args ...string) []string {
	e.t.Helper()

	stdout := e.mustRun(append([]string{"__complete"}, args...)...)
	var out []string
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		out = append(out, strings.SplitN(line, "\t", 2)[0])
	}
	return out
}

func TestCompleteRemotePaths(t *testing.T) {
	e := setup(t)
	e.mustRun("mkdir", "-p", e.remotePath("papers"))
	e.mustRun("put", e.writeLocal("notes.txt", []byte("x")), e.remotePath("notes.txt"))

	got := e.completeLine("ls", e.remote+"/")
	want := map[string]bool{e.remotePath("papers") + "/": true, e.remotePath("notes.txt"): true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("candidates = %v, want the directory with a trailing slash and the file without", got)
	}

	// A fragment narrows it, and a directory is offered so the next keystroke
	// carries on inside it.
	if got := e.completeLine("ls", e.remote+"/pa"); len(got) != 1 || got[0] != e.remotePath("papers")+"/" {
		t.Errorf("candidates = %v, want just papers/", got)
	}
}

func TestCompleteKeepsTheSpaceAliasAsTyped(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("notes.txt", []byte("x")), e.remotePath("notes.txt"))

	// The remote directory sits in the home space, so the same listing is
	// reachable through the alias — and the candidate has to come back in the
	// form the user was typing, or the shell rewrites the line into a path that
	// means something else.
	rel := strings.TrimPrefix(e.remote, homeRoot+"/")
	got := e.completeLine("cat", "home:"+rel+"/not")
	if len(got) != 1 || got[0] != "home:"+rel+"/notes.txt" {
		t.Fatalf("candidates = %v, want the home: prefix kept", got)
	}
}

func TestCompleteNeedsTheMarkerOnATransferPath(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("notes.txt", []byte("x")), e.remotePath("notes.txt"))

	// cp cannot tell the two sides apart by the string alone, so an unmarked
	// path is local and the shell completes it.
	if got := e.completeLine("cp", e.remote+"/"); len(got) != 0 {
		t.Errorf("candidates = %v, want none for an unmarked path", got)
	}
	if got := e.completeLine("cp", "cb:"+e.remote+"/not"); len(got) != 1 ||
		got[0] != "cb:"+e.remotePath("notes.txt") {
		t.Errorf("candidates = %v, want the cb: path", got)
	}
}

func TestCompleteSpacesAndSlots(t *testing.T) {
	e := setup(t)

	if got := e.completeLine("space", "info", ""); len(got) == 0 || got[0] != "home" {
		t.Errorf("space aliases = %v, want home among them", got)
	}

	e.clearSlot("it-completion")
	e.mustRun("copy", "--slot", "it-completion", e.writeLocal("slotted.txt", []byte("x")))

	got := e.completeLine("clipboard", "clear", "it-")
	if len(got) != 1 || got[0] != "it-completion" {
		t.Errorf("clipboard slots = %v, want it-completion", got)
	}
}

func TestCompleteIsFastEnoughForAKeyPress(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("notes.txt", []byte("x")), e.remotePath("notes.txt"))

	// Warm the token cache first: signing in is not what is being measured, and
	// a shell that has completed once already has a token.
	e.completeLine("ls", e.remote+"/")

	start := time.Now()
	e.completeLine("ls", e.remote+"/")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("a completion took %s, which is long enough to feel like a hang", elapsed)
	}
}

func TestCompleteSaysNothingWithoutCredentials(t *testing.T) {
	e := setup(t)

	// A shell asks for completions whether or not the user has signed in. With
	// no way to authenticate, the answer is an empty list and a clean exit: no
	// password prompt, no device-flow code, no error printed into the middle of
	// the command line being typed.
	cmd := exec.Command(binary, "--endpoint", endpoint, "--method", "basic",
		"__complete", "ls", e.remote+"/")
	cmd.Env = []string{
		"CERNBOX_CONFIG=/nonexistent/absent.yaml",
		"CERNBOX_TOKEN_CACHE=" + e.localPath("empty-cache"),
		"SSL_CERT_FILE=" + devCACert(),
	}

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("completion without credentials failed: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" && !strings.HasPrefix(line, ":") {
			t.Errorf("completion suggested %q with no way to sign in", line)
		}
	}
}
