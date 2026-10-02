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
		Username string `json:"username"`
		Admin    *bool  `json:"admin"`
	}
	e.runJSON(&me, "--as", otherUser, "whoami")
	if me.Username != otherUser {
		t.Errorf("whoami --as = %+v, want %s", me, otherUser)
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

	if _, _, code := e.run("cp", local, "cb:"+dir+"/"); code != 4 {
		t.Fatalf("put into %s without --as exited %d, want 4: the test proves nothing", dir, code)
	}

	e.mustRun("--as", otherUser, "cp", local, "cb:"+dir+"/")
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

// TestIdentityMarkerReachesAnotherUsersHome reads marie's home through
// "marie@cb:~", which the admin's own identity could not list.
func TestIdentityMarkerReachesAnotherUsersHome(t *testing.T) {
	e := setup(t)
	dir := e.recipientDir()
	e.mustRunAs(e.other(), "cp", e.writeLocal("hers.txt", []byte("marie's")), "cb:"+dir+"/")
	rel := strings.TrimPrefix(dir, otherHomeRoot+"/")

	if _, _, code := e.run("ls", dir); code != 4 {
		t.Fatalf("listing %s as %s exited %d, want 4: the test proves nothing", dir, username, code)
	}
	out := e.mustRun("ls", otherUser+"@cb:~/"+rel)
	if !strings.Contains(out, "hers.txt") {
		t.Errorf("ls %s@cb:~/%s = %q, want hers.txt", otherUser, rel, out)
	}
}

// TestCpBetweenUsers copies from the admin's own space into marie's: read as
// one user, written as the other, which no server-side copy can do.
func TestCpBetweenUsers(t *testing.T) {
	e := setup(t)
	e.mustRun("cp", e.writeLocal("report.txt", []byte("for marie")), "cb:"+e.remotePath("report.txt"))
	dir := e.recipientDir()

	e.mustRun("cp", "--verify", "cb:"+e.remotePath("report.txt"), otherUser+"@cb:"+dir+"/")
	if got := e.mustRunAs(e.other(), "cat", dir+"/report.txt"); got != "for marie" {
		t.Errorf("%s reads %q", otherUser, got)
	}
}

func TestCpBetweenUsersCopiesATree(t *testing.T) {
	e := setup(t)
	e.mustRun("mkdir", "-p", e.remotePath("tree/sub"))
	e.mustRun("cp", e.writeLocal("a.txt", []byte("alpha")), "cb:"+e.remotePath("tree/a.txt"))
	e.mustRun("cp", e.writeLocal("b.txt", []byte("beta")), "cb:"+e.remotePath("tree/sub/b.txt"))
	dir := e.recipientDir()

	e.mustRun("cp", "-r", "cb:"+e.remotePath("tree"), otherUser+"@cb:"+dir+"/")
	if got := e.mustRunAs(e.other(), "cat", dir+"/tree/sub/b.txt"); got != "beta" {
		t.Errorf("%s reads %q for the nested file", otherUser, got)
	}
}
