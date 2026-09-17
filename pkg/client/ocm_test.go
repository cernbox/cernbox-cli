package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

func TestGenerateInvite(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, ocmGenerateInvite, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"abc123","description":"joint analysis","expiration":1767225600,
		  "invite_link":"https://cernbox.test/ocm/invite?token=abc123"}`)
	})

	invite, err := f.client().GenerateInvite(context.Background(), "joint analysis", "")
	if err != nil {
		t.Fatal(err)
	}
	if invite.Token != "abc123" {
		t.Errorf("Token = %q", invite.Token)
	}
	if invite.Link == "" {
		t.Error("the invite link is what the user has to pass on, and it was dropped")
	}
	if invite.Expiration == nil || !invite.Expiration.Equal(time.Unix(1767225600, 0)) {
		t.Errorf("Expiration = %v", invite.Expiration)
	}
}

func TestGenerateInviteOmitsEmptyFields(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, ocmGenerateInvite, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"abc123"}`)
	})

	if _, err := f.client().GenerateInvite(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	body := f.lastRequest(http.MethodPost).Body
	var sent map[string]string
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, body)
	}
	// Sending an empty recipient would ask the server to mail nobody.
	if _, present := sent["recipient"]; present {
		t.Errorf("recipient should be omitted when unset: %s", body)
	}
	if _, present := sent["description"]; present {
		t.Errorf("description should be omitted when unset: %s", body)
	}
}

func TestGenerateInviteWithoutToken(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, ocmGenerateInvite, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	})

	_, err := f.client().GenerateInvite(context.Background(), "", "")
	if err == nil || !strings.Contains(err.Error(), "no invitation token") {
		t.Errorf("got %v, want a clear error", err)
	}
}

// TestOCMUnavailableIsExplained: the sciencemesh service is optional, and a
// bare 404 reads as "you have no invitations" rather than "this deployment does
// not do federated sharing".
func TestOCMUnavailableIsExplained(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, ocmGenerateInvite, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	_, err := f.client().GenerateInvite(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "does not expose federated sharing") {
		t.Errorf("got %v, want an explanation rather than a bare 404", err)
	}
}

func TestListInvites(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodGet, ocmListInvite, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"token":"abc","description":"one"},{"token":"def"}]`)
	})

	invites, err := f.client().ListInvites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(invites) != 2 || invites[0].Token != "abc" {
		t.Errorf("invites = %+v", invites)
	}
	if invites[1].Expiration != nil {
		t.Error("an absent expiration should stay nil rather than become the epoch")
	}
}

func TestAcceptInvite(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, ocmAcceptInvite, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})

	if err := f.client().AcceptInvite(context.Background(), "abc123", "other-lab.org"); err != nil {
		t.Fatal(err)
	}
	body := f.lastRequest(http.MethodPost).Body
	// The field is providerDomain, not provider: reva decodes it strictly.
	if !strings.Contains(body, `"providerDomain":"other-lab.org"`) {
		t.Errorf("body = %s", body)
	}
	if !strings.Contains(body, `"token":"abc123"`) {
		t.Errorf("body = %s", body)
	}
}

func TestAcceptInviteRequiresBothParts(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()

	if err := c.AcceptInvite(context.Background(), "abc", ""); cberr.KindOf(err) != cberr.KindUsage {
		t.Errorf("got %v, want a usage error for a missing provider", err)
	}
	if err := c.AcceptInvite(context.Background(), "", "other-lab.org"); cberr.KindOf(err) != cberr.KindUsage {
		t.Errorf("got %v, want a usage error for a missing token", err)
	}
}

func TestAcceptedUsers(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodGet, ocmFindAccepted, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"display_name":"Alice","idp":"https://other-lab.org","user_id":"alice","mail":"alice@other-lab.org"}]`)
	})

	users, err := f.client().AcceptedUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("got %d users", len(users))
	}
	// The address is what gets passed to --with-remote, so the scheme must be
	// stripped: "alice@https://other-lab.org" is not a recipient.
	if got := users[0].Address(); got != "alice@other-lab.org" {
		t.Errorf("Address() = %q, want alice@other-lab.org", got)
	}
}

func TestRemoteUserAddress(t *testing.T) {
	tests := []struct {
		user RemoteUser
		want string
	}{
		{RemoteUser{UserID: "alice", IDP: "https://other-lab.org"}, "alice@other-lab.org"},
		{RemoteUser{UserID: "alice", IDP: "http://other-lab.org"}, "alice@other-lab.org"},
		{RemoteUser{UserID: "alice", IDP: "other-lab.org"}, "alice@other-lab.org"},
		{RemoteUser{UserID: "alice"}, "alice"},
	}
	for _, tt := range tests {
		if got := tt.user.Address(); got != tt.want {
			t.Errorf("Address(%+v) = %q, want %q", tt.user, got, tt.want)
		}
	}
}

func TestRemoveAcceptedUser(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodDelete, ocmDeleteAccepted, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})

	if err := f.client().RemoveAcceptedUser(context.Background(), "other-lab.org", "alice"); err != nil {
		t.Fatal(err)
	}
	body := f.lastRequest(http.MethodDelete).Body
	if !strings.Contains(body, `"user_id":"alice"`) || !strings.Contains(body, `"idp":"other-lab.org"`) {
		t.Errorf("body = %s", body)
	}
}

func TestRemoveAcceptedUserRequiresBothParts(t *testing.T) {
	f := newFakeServer(t)
	if err := f.client().RemoveAcceptedUser(context.Background(), "", "alice"); cberr.KindOf(err) != cberr.KindUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

func TestListProviders(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodGet, ocmListProviders, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"OtherLab","full_name":"The Other Laboratory","domain":"other-lab.org",
		  "homepage":"https://other-lab.org"}]`)
	})

	providers, err := f.client().ListProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].Domain != "other-lab.org" {
		t.Errorf("providers = %+v", providers)
	}
}

func TestReceivedFederatedShares(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodGet, ocsRemoteShares, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":[
		  {"id":"7","name":"shared-data","displayname_owner":"Alice","permissions":1,"state":0,"mountpoint":"/ocm/shared-data"},
		  {"id":"8","name":"pending-data","uid_owner":"bob","permissions":15,"state":1}
		]}}`)
	})

	shares, err := f.client().ReceivedFederatedShares(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 2 {
		t.Fatalf("got %d shares", len(shares))
	}

	if shares[0].Owner != "Alice" {
		t.Errorf("owner = %q, want the display name when there is one", shares[0].Owner)
	}
	if !shares[0].Accepted {
		t.Error("state 0 means accepted")
	}
	if shares[0].Permissions != "viewer" {
		t.Errorf("permissions = %q, want viewer for a read-only mask", shares[0].Permissions)
	}

	if shares[1].Accepted {
		t.Error("a non-zero state means the share is not accepted")
	}
	if shares[1].Owner != "bob" {
		t.Errorf("owner = %q, want the uid when there is no display name", shares[1].Owner)
	}
	if shares[1].Permissions != "editor" {
		t.Errorf("permissions = %q, want editor for a write mask", shares[1].Permissions)
	}
}

func TestOCSPermissionString(t *testing.T) {
	tests := map[int]string{
		0:  "",
		1:  "viewer",
		15: "editor",
		19: "collab",
		31: "collab",
	}
	for mask, want := range tests {
		if got := ocsPermissionString(mask); got != want {
			t.Errorf("ocsPermissionString(%d) = %q, want %q", mask, got, want)
		}
	}
}

// TestNumericState covers both shapes OCS uses for the same field.
func TestNumericState(t *testing.T) {
	tests := []struct {
		in   any
		want int64
	}{
		{float64(0), 0},
		{float64(15), 15},
		{"0", 0},
		{"15", 15},
		{"not a number", -1},
		{nil, -1},
	}
	for _, tt := range tests {
		if got := numericState(tt.in); got != tt.want {
			t.Errorf("numericState(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
