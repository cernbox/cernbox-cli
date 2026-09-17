//go:build integration

package integration_test

import (
	"strings"
	"testing"
)

// requirePartner skips a test when the federation partner is not running. A
// deployment with a single provider is a valid one; it just cannot federate.
func requirePartner(t *testing.T) {
	t.Helper()
	if !partnerReachable() {
		t.Skipf("the federation partner at %s is not running", partnerEndpoint)
	}
}

type invite struct {
	Token string `json:"token"`
	Link  string `json:"link"`
}

type contact struct {
	DisplayName string `json:"display_name"`
	IDP         string `json:"idp"`
	UserID      string `json:"user_id"`
	Mail        string `json:"mail"`
}

func TestOCMProvidersListsThePartner(t *testing.T) {
	requirePartner(t)
	e := setup(t)

	var providers []struct {
		Domain   string `json:"domain"`
		FullName string `json:"full_name"`
	}
	e.runJSON(&providers, "ocm", "providers")

	for _, p := range providers {
		if p.Domain == partnerDomain {
			return
		}
	}
	t.Errorf("the partner %q is not among the known providers: %+v", partnerDomain, providers)
}

func TestOCMInviteCreateAndList(t *testing.T) {
	requirePartner(t)
	e := setup(t)

	var created invite
	e.runJSON(&created, "ocm", "invite", "create", "--description", "integration test")
	if created.Token == "" {
		t.Fatal("no invitation token was issued")
	}

	var invites []invite
	e.runJSON(&invites, "ocm", "invite", "list")
	for _, inv := range invites {
		if inv.Token == created.Token {
			return
		}
	}
	t.Errorf("the new invitation is not in the listing: %+v", invites)
}

// TestOCMInviteAcceptEstablishesAContact is the whole point of the partner
// instance: an invitation created here, accepted there, becomes a contact on
// both sides.
func TestOCMInviteAcceptEstablishesAContact(t *testing.T) {
	requirePartner(t)
	e := setup(t)

	var created invite
	e.runJSON(&created, "ocm", "invite", "create", "--description", "contact test")
	if created.Token == "" {
		t.Fatal("no invitation token was issued")
	}

	// Alice, at the partner, accepts the invitation einstein created here.
	stdout, stderr, code := e.runAs(e.partner(),
		"ocm", "invite", "accept", created.Token, "--provider", "revad")
	if code != 0 {
		t.Fatalf("accepting the invitation failed (%d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// The contact must now be visible from both ends.
	var mine []contact
	e.runJSON(&mine, "ocm", "contacts")
	if !containsUser(mine, partnerUser) {
		t.Errorf("%s is not among einstein's contacts: %+v", partnerUser, mine)
	}

	var theirs []contact
	e.runJSONAs(e.partner(), &theirs, "ocm", "contacts")
	if !containsUser(theirs, username) {
		t.Errorf("%s is not among alice's contacts: %+v", username, theirs)
	}
}

// TestOCMShareReachesThePartner walks the whole federated path: invite, accept,
// share, and see it arrive on the other side.
func TestOCMShareReachesThePartner(t *testing.T) {
	requirePartner(t)
	e := setup(t)

	var created invite
	e.runJSON(&created, "ocm", "invite", "create")
	if _, stderr, code := e.runAs(e.partner(),
		"ocm", "invite", "accept", created.Token, "--provider", "revad"); code != 0 {
		t.Fatalf("accepting the invitation failed: %s", stderr)
	}

	target := e.remotePath("federated.txt")
	e.mustRun("put", e.writeLocal("federated.txt", []byte("across the mesh")), target)

	address := partnerUser + "@" + partnerDomain
	stdout, stderr, code := e.run("share", "create", target, "--with-remote", address, "--role", "viewer")
	if code != 0 {
		t.Fatalf("creating the federated share failed (%d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	var received []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Owner string `json:"owner"`
	}
	e.runJSONAs(e.partner(), &received, "ocm", "received")
	for _, s := range received {
		if strings.Contains(s.Name, "federated.txt") {
			return
		}
	}
	t.Errorf("the share did not arrive at the partner: %+v", received)
}

func TestOCMContactRemoval(t *testing.T) {
	requirePartner(t)
	e := setup(t)

	var created invite
	e.runJSON(&created, "ocm", "invite", "create")
	if _, stderr, code := e.runAs(e.partner(),
		"ocm", "invite", "accept", created.Token, "--provider", "revad"); code != 0 {
		t.Fatalf("accepting the invitation failed: %s", stderr)
	}

	var contacts []contact
	e.runJSON(&contacts, "ocm", "contacts")
	if !containsUser(contacts, partnerUser) {
		t.Skip("the contact was not established, so there is nothing to remove")
	}

	address := partnerUser + "@" + partnerDomain
	e.mustRun("ocm", "contacts", "--remove", address)

	e.runJSON(&contacts, "ocm", "contacts")
	if containsUser(contacts, partnerUser) {
		t.Errorf("%s survived removal: %+v", address, contacts)
	}
}

// TestOCMAcceptRejectsAnUnknownProvider: the providers file is the trust
// boundary, and a domain that is not in it must be refused.
func TestOCMAcceptRejectsAnUnknownProvider(t *testing.T) {
	requirePartner(t)
	e := setup(t)

	_, _, code := e.run("ocm", "invite", "accept", "some-token", "--provider", "not-a-partner.invalid")
	if code == 0 {
		t.Error("accepting an invitation from an unknown provider should fail")
	}
}

func TestOCMInviteAcceptNeedsAProvider(t *testing.T) {
	e := setup(t)
	_, stderr, code := e.run("ocm", "invite", "accept", "some-token")
	if code != 2 {
		t.Errorf("exit code = %d, want 2 for a missing provider", code)
	}
	if !strings.Contains(stderr, "--provider") {
		t.Errorf("the error should name the missing flag:\n%s", stderr)
	}
}

func containsUser(contacts []contact, user string) bool {
	for _, c := range contacts {
		if c.UserID == user {
			return true
		}
	}
	return false
}
