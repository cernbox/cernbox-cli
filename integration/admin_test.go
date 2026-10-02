//go:build integration

package integration_test

import (
	"strings"
	"testing"
)

// The dev revad makes einstein an admin (he is in sailing-lovers, the
// admin_group) and marie not.

func TestWhoamiSaysWhetherAdmin(t *testing.T) {
	e := setup(t)

	for _, tc := range []struct {
		who  account
		want bool
	}{
		{e.self(), true},
		{e.other(), false},
	} {
		var me struct {
			Username string `json:"username"`
			Admin    *bool  `json:"admin"`
		}
		e.runJSONAs(tc.who, &me, "whoami")
		if me.Admin == nil || *me.Admin != tc.want {
			t.Errorf("whoami as %s: admin = %v, want %v", me.Username, me.Admin, tc.want)
		}
	}
}

func TestAsShowsWhoActs(t *testing.T) {
	e := setup(t)

	var me struct {
		Username       string `json:"username"`
		Admin          *bool  `json:"admin"`
		ImpersonatedBy string `json:"impersonated_by"`
	}
	e.runJSON(&me, "--as", otherUser, "whoami")
	if me.Username != otherUser || me.ImpersonatedBy != username {
		t.Errorf("whoami --as = %+v, want %s impersonated by %s", me, otherUser, username)
	}
	if me.Admin == nil || *me.Admin {
		t.Errorf("admin = %v: whoami --as reports the impersonated user, who is not one", me.Admin)
	}
}

// TestAsListsAnotherUsersShares is the first reason for --as: seeing what
// someone else has shared, as they see it.
func TestAsListsAnotherUsersShares(t *testing.T) {
	e := setup(t)
	dir := e.recipientDir()
	e.mustRunAs(e.other(), "share", "create", dir, "--with", "richard", "--role", "viewer")

	var shares []struct {
		Path string `json:"path"`
	}
	e.runJSON(&shares, "--as", otherUser, "share", "list")
	for _, s := range shares {
		if s.Path == dir {
			return
		}
	}
	t.Errorf("%s's share of %s is not in the listing: %+v", otherUser, dir, shares)
}

// TestAsWritesIntoAnotherUsersHome is the second: putting a file where someone
// else can find it, which the admin's own identity is not allowed to do.
func TestAsWritesIntoAnotherUsersHome(t *testing.T) {
	e := setup(t)
	dir := e.recipientDir()
	local := e.writeLocal("fix.txt", []byte("from the admin"))

	if _, _, code := e.run("put", local, "cb:"+dir+"/"); code != 4 {
		t.Fatalf("put into %s without --as exited %d, want 4: the test proves nothing", dir, code)
	}

	e.mustRun("--as", otherUser, "put", local, "cb:"+dir+"/")
	if got := e.mustRunAs(e.other(), "cat", dir+"/fix.txt"); got != "from the admin" {
		t.Errorf("%s reads %q", otherUser, got)
	}
}

func TestAsRefusedForNonAdmin(t *testing.T) {
	e := setup(t)

	_, stderr, code := e.runAs(e.other(), "--as", username, "ls")
	if code != 4 {
		t.Errorf("exit %d, want 4 (permission); stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "not an administrator") {
		t.Errorf("stderr does not say why: %s", stderr)
	}
}
