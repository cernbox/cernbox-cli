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

	_, stderr, err := run(t, box, "--as", "marie", "share", "list")
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
	if !strings.Contains(stderr, "Acting as marie.") {
		t.Errorf("stderr does not say whom the command acts as: %q", stderr)
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
		if got.ImpersonatedBy != "" {
			t.Errorf("impersonated_by = %q without --as", got.ImpersonatedBy)
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
	for _, want := range []string{"marie", "Admin", "no", "Impersonated by", "einstein"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("whoami output lacks %q:\n%s", want, stdout)
		}
	}
}
