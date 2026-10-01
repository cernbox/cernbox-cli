package cli

import (
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// TestLinkCreateUploadSendsCreateOnly: the server has two names for this
// permission set and only ever answers with one of them, so the CLI sends the
// one that comes back.
func TestLinkCreateUploadSendsCreateOnly(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/incoming")

	stdout, _, err := run(t, box, "link", "create", "/eos/user/e/einstein/incoming", "--role", "upload")
	if err != nil {
		t.Fatal(err)
	}
	if got := box.lastPostBody(); !strings.Contains(got, `"type":"createOnly"`) {
		t.Errorf("sent %s, want the createOnly link type", got)
	}
	if !strings.Contains(stdout, "createOnly") {
		t.Errorf("the type the server reported is not shown:\n%s", stdout)
	}
}

func TestLinkCreateAcceptsDropAsTheSameThing(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/incoming")

	if _, _, err := run(t, box, "link", "create",
		"/eos/user/e/einstein/incoming", "--role", "drop"); err != nil {
		t.Fatal(err)
	}
	if got := box.lastPostBody(); !strings.Contains(got, `"type":"createOnly"`) {
		t.Errorf("sent %s, want the createOnly link type", got)
	}
}

// TestLinkCreateUploadRefusesAFile: the server refuses this too, with a message
// about matching permissions that says nothing about the reason. Measured
// against the dev instance before deciding to answer it here.
func TestLinkCreateUploadRefusesAFile(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/report.pdf", "x")

	_, _, err := run(t, box, "link", "create", "/eos/user/e/einstein/report.pdf", "--role", "upload")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("got %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "folder") {
		t.Errorf("the message does not say what is wrong: %v", err)
	}
	// And nothing was sent: refusing after the request would leave the server to
	// explain it.
	box.mu.Lock()
	defer box.mu.Unlock()
	for _, body := range box.postBodies {
		if strings.Contains(body, "createOnly") {
			t.Errorf("the request went out anyway: %s", body)
		}
	}
}

func TestLinkUpdateUploadRefusesAFile(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/report.pdf", "x")

	_, _, err := run(t, box, "link", "update",
		"/eos/user/e/einstein/report.pdf", "link-1", "--role", "upload")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("got %v, want a usage error", err)
	}
}

// TestLinkUpdateToViewerStillWorksOnAFile guards the check above from being
// written as "a file cannot have its link role changed".
func TestLinkUpdateToViewerStillWorksOnAFile(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/report.pdf", "x")

	if _, _, err := run(t, box, "link", "update",
		"/eos/user/e/einstein/report.pdf", "link-1", "--role", "viewer"); err != nil {
		t.Fatalf("changing a file's link to viewer should work: %v", err)
	}
}

func TestLinkRoleErrorMentionsUpload(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/incoming")

	_, _, err := run(t, box, "link", "create", "/eos/user/e/einstein/incoming", "--role", "nonsense")
	if err == nil || !strings.Contains(err.Error(), "upload") {
		t.Errorf("the error should list the roles that exist: %v", err)
	}
}

func TestLinkTypeOfMapsEverySpelling(t *testing.T) {
	cases := map[string]string{
		"viewer": "view", "view": "view", "read": "view",
		"editor": "edit", "edit": "edit", "write": "edit",
		"upload": "createOnly", "drop": "createOnly",
	}
	for role, want := range cases {
		got, err := linkTypeOf(role)
		if err != nil {
			t.Errorf("linkTypeOf(%q): %v", role, err)
			continue
		}
		if got != want {
			t.Errorf("linkTypeOf(%q) = %q, want %q", role, got, want)
		}
	}
}
