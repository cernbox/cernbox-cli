package cli

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// TestAsActsAsUser checks that --as changes whose identity every request is
// made with, and that it asks the server once per command.
func TestAsActsAsUser(t *testing.T) {
	box := newTestBox(t)
	box.admin = true

	_, _, err := run(t, box, "--as", "marie", "share", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(box.impersonations, []string{"marie"}) {
		t.Errorf("impersonations = %v, want one, for marie", box.impersonations)
	}
	if got := box.auths[http.MethodGet+" /graph/v1beta1/me/drive/sharedByMe"]; got != "Bearer as-marie" {
		t.Errorf("share list was made with %q, want marie's token", got)
	}
	// Asking for the token is the admin's own request.
	if got := box.auths[http.MethodPost+" /admin/impersonate"]; got != "Bearer test-token" {
		t.Errorf("impersonate was made with %q, want the admin's token", got)
	}
}

func TestAsDeniedForNonAdmin(t *testing.T) {
	box := newTestBox(t)

	_, _, err := run(t, box, "--as", "marie", "ls")
	if cberr.ExitCode(err) != cberr.ExitPermission {
		t.Fatalf("err = %v (exit %d), want a permission error", err, cberr.ExitCode(err))
	}
	if !strings.Contains(err.Error(), "not an administrator") {
		t.Errorf("error does not say why: %v", err)
	}
	for k := range box.auths {
		if strings.HasPrefix(k, "PROPFIND") {
			t.Errorf("listed files after being refused: %s", k)
		}
	}
}

func TestWhoamiShowsAdmin(t *testing.T) {
	for _, admin := range []bool{true, false} {
		box := newTestBox(t)
		box.admin = admin

		stdout, _, err := run(t, box, "-o", "json", "whoami")
		if err != nil {
			t.Fatal(err)
		}
		var got whoamiResult
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("decoding %q: %v", stdout, err)
		}
		if got.Username != "einstein" || got.Admin == nil || *got.Admin != admin {
			t.Errorf("whoami = %+v, want einstein with admin %v", got, admin)
		}
	}
}

func TestWhoamiAs(t *testing.T) {
	box := newTestBox(t)
	box.admin = true

	stdout, _, err := run(t, box, "--as", "marie", "whoami")
	if err != nil {
		t.Fatal(err)
	}
	// Acting as someone is transparent: whoami answers as them, and says
	// nothing about who is behind it.
	for _, want := range []string{"marie", "Admin", "no"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("whoami output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "einstein") {
		t.Errorf("whoami --as mentions the signed-in user:\n%s", stdout)
	}
}

// TestIdentityMarkerActsAsUser checks that USER@cb: on a path makes the command
// act as that user, the way --as does.
func TestIdentityMarkerActsAsUser(t *testing.T) {
	box := newTestBox(t)
	box.admin = true
	box.putFile("/eos/user/e/einstein/shared/a.txt", "alpha")

	if _, _, err := run(t, box, "stat", "marie@cb:/eos/user/e/einstein/shared/a.txt"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(box.impersonations, []string{"marie"}) {
		t.Errorf("impersonations = %v, want one, for marie", box.impersonations)
	}
	key := "PROPFIND /remote.php/dav/files/marie/eos/user/e/einstein/shared/a.txt"
	if got := box.auths[key]; got != "Bearer as-marie" {
		t.Errorf("%s was made with %q, want marie's token", key, got)
	}
}

// TestNamingYourselfIsNotAnImpersonation keeps the audit log free of entries
// for a path the signed-in user wrote their own name on.
func TestNamingYourselfIsNotAnImpersonation(t *testing.T) {
	box := newTestBox(t)
	box.admin = true
	box.putFile("/eos/user/e/einstein/a.txt", "alpha")

	if _, _, err := run(t, box, "stat", "einstein@cb:~/a.txt"); err != nil {
		t.Fatal(err)
	}
	if len(box.impersonations) != 0 {
		t.Errorf("impersonated for the signed-in user: %v", box.impersonations)
	}
}

// TestOneUserPerCommand refuses paths for two different users outside cp and
// sync, before anyone is impersonated.
func TestOneUserPerCommand(t *testing.T) {
	for _, args := range [][]string{
		{"mv", "marie@cb:~/a", "richard@cb:~/b"},
		{"--as", "marie", "ls", "richard@cb:~"},
		{"copy", "marie@cb:~/a.txt"},
		{"paste", "marie@cb:~/"},
	} {
		box := newTestBox(t)
		box.admin = true
		_, _, err := run(t, box, args...)
		if cberr.ExitCode(err) != cberr.ExitUsage {
			t.Errorf("%v: err = %v (exit %d), want a usage error", args, err, cberr.ExitCode(err))
		}
		if len(box.impersonations) != 0 {
			t.Errorf("%v: impersonated before refusing: %v", args, box.impersonations)
		}
	}
}

// TestCpBetweenUsersRelays checks that a copy whose two sides are acted on as
// different users reads as one and writes as the other, since no server-side
// COPY can carry both.
func TestCpBetweenUsersRelays(t *testing.T) {
	box := newTestBox(t)
	box.admin = true
	box.putFile("/eos/user/e/einstein/a.txt", "alpha")
	box.mkdir("/eos/user/e/einstein/shared")

	if _, _, err := run(t, box, "cp", "cb:~/a.txt", "marie@cb:/eos/user/e/einstein/shared/"); err != nil {
		t.Fatal(err)
	}
	if got := box.files["/eos/user/e/einstein/shared/a.txt"]; got != "alpha" {
		t.Errorf("copied = %q", got)
	}
	for k := range box.auths {
		if strings.HasPrefix(k, "COPY ") {
			t.Errorf("used a server-side copy across users: %s", k)
		}
	}
	if got := box.auths["GET /remote.php/dav/files/einstein/eos/user/e/einstein/a.txt"]; got != "Bearer test-token" {
		t.Errorf("read with %q, want the signed-in user's token", got)
	}
	if got := box.auths["PUT /remote.php/dav/files/marie/eos/user/e/einstein/shared/a.txt"]; got != "Bearer as-marie" {
		t.Errorf("written with %q, want marie's token", got)
	}
}

// TestCpSameUserCopiesOnTheServer keeps a copy within one user's view a COPY,
// even when that user is named on both sides.
func TestCpSameUserCopiesOnTheServer(t *testing.T) {
	box := newTestBox(t)
	box.admin = true
	box.putFile("/eos/user/e/einstein/a.txt", "alpha")

	if _, _, err := run(t, box, "cp", "marie@cb:/eos/user/e/einstein/a.txt", "marie@cb:/eos/user/e/einstein/b.txt"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(box.impersonations, []string{"marie"}) {
		t.Errorf("impersonations = %v, want marie once", box.impersonations)
	}
	if got := box.auths["COPY /remote.php/dav/files/marie/eos/user/e/einstein/a.txt"]; got != "Bearer as-marie" {
		t.Errorf("COPY made with %q, want a server-side copy as marie", got)
	}
}

func TestCpBetweenUsersRelaysATree(t *testing.T) {
	box := newTestBox(t)
	box.admin = true
	box.putFile("/eos/user/e/einstein/data/a.txt", "alpha")
	box.putFile("/eos/user/e/einstein/data/sub/b.txt", "beta")
	box.mkdir("/eos/user/e/einstein/shared")

	if _, _, err := run(t, box, "cp", "-r", "--verify", "cb:~/data", "marie@cb:/eos/user/e/einstein/shared/"); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		"/eos/user/e/einstein/shared/data/a.txt":     "alpha",
		"/eos/user/e/einstein/shared/data/sub/b.txt": "beta",
	} {
		if got := box.files[p]; got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
}
