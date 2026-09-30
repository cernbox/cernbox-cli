package client

import "testing"

// TestRoleOfCallerPicksTheCallersOwnGrant: taking the first permission with a
// role was wrong as soon as a space reported more than one — the answer then
// depended on the order the server sent them, and could be somebody else's role.
func TestRoleOfCallerPicksTheCallersOwnGrant(t *testing.T) {
	me := &User{ID: "u-123", Username: "gdelmont"}

	perms := []Permission{
		{Role: "viewer", GrantedTo: &Identity{ID: "someone-else", Type: "user"}},
		{Role: "collab", GrantedTo: &Identity{ID: "gdelmont", Type: "user"}},
		{Role: "editor", GrantedTo: &Identity{ID: "a-third", Type: "user"}},
	}
	if got := roleOfCaller(perms, me); got != "collab" {
		t.Errorf("got %q, want collab: the caller's own grant is not the first one", got)
	}

	// Matching on the opaque id works too, since a server may identify a user
	// either way.
	byID := []Permission{{Role: "collab", GrantedTo: &Identity{ID: "u-123"}}}
	if got := roleOfCaller(byID, me); got != "collab" {
		t.Errorf("got %q, want collab when identified by id", got)
	}
}

// TestRoleOfCallerReadsALoneUnattributedGrant is the shape a project root has
// when the server names nobody, which is what the dev instance returns.
func TestRoleOfCallerReadsALoneUnattributedGrant(t *testing.T) {
	perms := []Permission{{Role: "editor"}}
	if got := roleOfCaller(perms, &User{Username: "gdelmont"}); got != "editor" {
		t.Errorf("got %q, want editor", got)
	}
	// And with no idea who is asking, a single grant is still the only candidate.
	if got := roleOfCaller(perms, nil); got != "editor" {
		t.Errorf("got %q with no caller, want editor", got)
	}
}

// TestRoleOfCallerSaysNothingRatherThanGuess: several grants, none of them ours,
// is not an invitation to report the first.
func TestRoleOfCallerSaysNothingRatherThanGuess(t *testing.T) {
	me := &User{Username: "gdelmont"}
	perms := []Permission{
		{Role: "viewer", GrantedTo: &Identity{ID: "someone-else"}},
		{Role: "editor", GrantedTo: &Identity{ID: "a-third"}},
	}
	if got := roleOfCaller(perms, me); got != "" {
		t.Errorf("got %q, want nothing: none of these grants are ours", got)
	}

	// Two unattributed roles are equally unidentifiable.
	if got := roleOfCaller([]Permission{{Role: "viewer"}, {Role: "editor"}}, nil); got != "" {
		t.Errorf("got %q, want nothing when two grants name nobody", got)
	}
}

// TestRoleOfCallerIgnoresLinksAndRolelessGrants: a public link on a space root
// carries no role of ours and must not be mistaken for one.
func TestRoleOfCallerIgnoresLinksAndRolelessGrants(t *testing.T) {
	me := &User{Username: "gdelmont"}
	perms := []Permission{
		{Link: &Link{Type: "view"}},
		{Role: "collab", GrantedTo: &Identity{ID: "gdelmont"}},
	}
	if got := roleOfCaller(perms, me); got != "collab" {
		t.Errorf("got %q, want collab", got)
	}
}
