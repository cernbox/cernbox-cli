package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// auditBox is a file three levels down with a different grant at each level,
// which is the situation the command exists for.
func auditBox(t *testing.T) *testBox {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents/2026")
	box.putFile("/eos/user/e/einstein/Documents/2026/report.pdf", "x")

	// Shared with a person two levels above the file.
	box.sharesOn["/eos/user/e/einstein/Documents"] =
		`{"id":"s-doc","roles":["b1e2218d-eef8-4d4c-b82d-0f1a1b48f3b5"],` +
			`"grantedToV2":{"user":{"id":"marie","displayName":"Marie Curie"}}}`
	// A group one level above.
	box.sharesOn["/eos/user/e/einstein/Documents/2026"] =
		`{"id":"s-grp","roles":["b1e2218d-eef8-4d4c-b82d-0f1a1b48f3b5"],` +
			`"grantedToV2":{"group":{"id":"physics-lovers"}}}`
	// And a public link on the file itself.
	box.sharesOn["/eos/user/e/einstein/Documents/2026/report.pdf"] =
		`{"id":"s-link","link":{"type":"view","webUrl":"https://cernbox.test/s/abc"}}`
	// Nothing on the space root.
	box.sharesOn["/eos/user/e/einstein"] = ``
	return box
}

// TestShareAuditFindsGrantsAboveThePath is the premise: sharing a directory
// shares what is in it, so the file alone cannot answer who can see it. Verified
// against the real server too — marie reads a file with only its grandparent
// shared to her.
func TestShareAuditFindsGrantsAboveThePath(t *testing.T) {
	stdout, stderr, err := run(t, auditBox(t), "share", "audit",
		"/eos/user/e/einstein/Documents/2026/report.pdf")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"marie", "physics-lovers", "cernbox.test/s/abc"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q:\n%s", want, stdout)
		}
	}
	// Each grant has to say where it came from, or the answer is not actionable.
	if !strings.Contains(stdout, "/eos/user/e/einstein/Documents") {
		t.Errorf("the granting directory is not named:\n%s", stdout)
	}
	if !strings.Contains(stdout, "inherited") {
		t.Errorf("grants from above must be marked inherited:\n%s", stdout)
	}
	if !strings.Contains(stderr, "1 person") || !strings.Contains(stderr, "1 group") {
		t.Errorf("the summary should count the kinds:\n%s", stderr)
	}
}

// TestShareAuditMarksOnlyInheritedGrants: a grant on the file itself is not
// inherited, and calling it so would misdirect somebody trying to remove it.
func TestShareAuditMarksOnlyInheritedGrants(t *testing.T) {
	stdout, _, err := run(t, auditBox(t), "--output", "json", "share", "audit",
		"/eos/user/e/einstein/Documents/2026/report.pdf")
	if err != nil {
		t.Fatal(err)
	}

	var grants []accessGrant
	if err := json.Unmarshal([]byte(stdout), &grants); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout)
	}
	if len(grants) != 3 {
		t.Fatalf("got %d grants, want 3: %+v", len(grants), grants)
	}

	for _, g := range grants {
		onFile := g.GrantedOn == "/eos/user/e/einstein/Documents/2026/report.pdf"
		if onFile && g.Inherited {
			t.Errorf("%s is on the file itself but marked inherited", g.Who)
		}
		if !onFile && !g.Inherited {
			t.Errorf("%s is on %s but not marked inherited", g.Who, g.GrantedOn)
		}
	}
}

// TestShareAuditOrdersBroadestFirst: the rows read as an explanation, from the
// widest grant to the most specific.
func TestShareAuditOrdersBroadestFirst(t *testing.T) {
	stdout, _, err := run(t, auditBox(t), "--output", "json", "share", "audit",
		"/eos/user/e/einstein/Documents/2026/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	var grants []accessGrant
	if err := json.Unmarshal([]byte(stdout), &grants); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(grants); i++ {
		if len(grants[i].GrantedOn) < len(grants[i-1].GrantedOn) {
			t.Errorf("row %d (%s) is broader than the one before it (%s)",
				i, grants[i].GrantedOn, grants[i-1].GrantedOn)
		}
	}
}

func TestShareAuditReportsNothingWhenNothingIsShared(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/private.txt", "x")
	box.sharesOn["/eos/user/e/einstein"] = ``
	box.sharesOn["/eos/user/e/einstein/private.txt"] = ``

	stdout, stderr, err := run(t, box, "share", "audit", "/eos/user/e/einstein/private.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "marie") {
		t.Errorf("nothing is shared, so nobody should be listed:\n%s", stdout)
	}
	if !strings.Contains(stderr, "Nothing grants access") {
		t.Errorf("an empty audit should say so plainly:\n%s", stderr)
	}
}

// TestShareAuditWarnsAboutNestedGrants: the same person granted at two levels is
// allowed and does work. Which role applies is the storage's business, so this
// points it out rather than guessing.
func TestShareAuditWarnsAboutNestedGrants(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/nested")
	box.putFile("/eos/user/e/einstein/nested/f.txt", "x")
	box.sharesOn["/eos/user/e/einstein"] = ``
	marie := func(role string) string {
		return `{"id":"s-` + role + `","roles":["` + role + `"],` +
			`"grantedToV2":{"user":{"id":"marie"}}}`
	}
	box.sharesOn["/eos/user/e/einstein/nested"] = marie("b1e2218d-eef8-4d4c-b82d-0f1a1b48f3b5")
	box.sharesOn["/eos/user/e/einstein/nested/f.txt"] = marie("fb6c3e19-e378-47e5-b277-9732f9de6e21")

	_, stderr, err := run(t, box, "share", "audit", "/eos/user/e/einstein/nested/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "different levels") {
		t.Errorf("a grant at two levels should be pointed out:\n%s", stderr)
	}
}

// TestShareAuditSaysProjectMembersCanReachIt: membership of a project is access,
// and no share reports it. Leaving it out would make an audit of a project path
// read as far more private than it is.
func TestShareAuditSaysProjectMembersCanReachIt(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/project/c/cernbox/data")
	box.putFile("/eos/project/c/cernbox/data/f.txt", "x")
	box.sharesOn["/eos/project/c/cernbox"] = ``
	box.sharesOn["/eos/project/c/cernbox/data"] = ``
	box.sharesOn["/eos/project/c/cernbox/data/f.txt"] = ``

	_, stderr, err := run(t, box, "share", "audit", "/eos/project/c/cernbox/data/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "project/cernbox") {
		t.Errorf("the space should be named:\n%s", stderr)
	}
	if !strings.Contains(stderr, "can reach this as well") {
		t.Errorf("project membership is access and has to be said:\n%s", stderr)
	}
	// The owner and the caller's own role are in the drive response, so they are
	// said rather than left to guesswork.
	if !strings.Contains(stderr, "owned by") {
		t.Errorf("the owner is known and should be named:\n%s", stderr)
	}
	// And the part that is genuinely unavailable is declared as such, instead of
	// an absence that reads like nobody else has access.
	if !strings.Contains(stderr, "not published") {
		t.Errorf("the unlistable groups should be declared, not silently omitted:\n%s", stderr)
	}
}

// TestShareAuditNamesTheProjectOwnerAndYourRole: both come from the drive
// listing, unlike the groups, which no driver puts in a response at all.
func TestShareAuditNamesTheProjectOwnerAndYourRole(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/project/c/cernbox/data")
	box.sharesOn["/eos/project/c/cernbox"] = ``
	box.sharesOn["/eos/project/c/cernbox/data"] = ``
	_, stderr, err := run(t, box, "share", "audit", "/eos/project/c/cernbox/data")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "owned by richard") {
		t.Errorf("the owner is missing:\n%s", stderr)
	}
	if !strings.Contains(stderr, "your own role is editor") {
		t.Errorf("the caller's own role is missing:\n%s", stderr)
	}
}

// TestShareAuditPersonalSpaceSaysNothingAboutMembership: a personal space has no
// membership to explain, and saying so anyway would be noise.
func TestShareAuditPersonalSpaceSaysNothingAboutMembership(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/f.txt", "x")
	box.sharesOn["/eos/user/e/einstein"] = ``
	box.sharesOn["/eos/user/e/einstein/f.txt"] = ``

	_, stderr, err := run(t, box, "share", "audit", "/eos/user/e/einstein/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "not published") || strings.Contains(stderr, "owned by") {
		t.Errorf("a personal space needs no membership note:\n%s", stderr)
	}
}

func TestAncestorsFrom(t *testing.T) {
	got := ancestorsFrom("/eos/user/e/einstein", "/eos/user/e/einstein/a/b/c.txt")
	want := []string{
		"/eos/user/e/einstein",
		"/eos/user/e/einstein/a",
		"/eos/user/e/einstein/a/b",
		"/eos/user/e/einstein/a/b/c.txt",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}

	// The root itself is one level, not zero.
	if got := ancestorsFrom("/eos/user/e/einstein", "/eos/user/e/einstein"); len(got) != 1 {
		t.Errorf("auditing the root gave %v", got)
	}
	// A path outside the root is all there is to go on, rather than a panic or a
	// walk of somebody else's tree.
	if got := ancestorsFrom("/eos/user/e/einstein", "/eos/project/c/x"); len(got) != 1 {
		t.Errorf("a path outside the space gave %v", got)
	}
}

// TestShareAuditPicksTheLongestMatchingSpace: a project nested under a home path
// would otherwise be audited as though it were part of the home.
func TestShareAuditPicksTheLongestMatchingSpace(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/project/c/cernbox/data")
	box.sharesOn["/eos/project/c/cernbox"] = ``
	box.sharesOn["/eos/project/c/cernbox/data"] = ``

	stdout, _, err := run(t, box, "--output", "json", "share", "audit",
		"/eos/project/c/cernbox/data")
	if err != nil {
		t.Fatal(err)
	}
	var grants []accessGrant
	if err := json.Unmarshal([]byte(stdout), &grants); err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if strings.HasPrefix(g.GrantedOn, "/eos/user") {
			t.Errorf("the walk escaped into the home space: %+v", g)
		}
	}
}
