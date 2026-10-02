package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

func adminJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, body)
}

func TestIsAdmin(t *testing.T) {
	for _, want := range []bool{true, false} {
		f := newFakeServer(t)
		f.on(http.MethodGet, adminStatusPath, func(w http.ResponseWriter, r *http.Request) {
			adminJSON(w, http.StatusOK, fmt.Sprintf(`{"admin":%v}`, want))
		})
		got, err := f.client().IsAdmin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("IsAdmin = %v, want %v", got, want)
		}
	}
}

func TestImpersonate(t *testing.T) {
	f := newFakeServer(t)
	f.on(http.MethodPost, adminImpersonatePath, func(w http.ResponseWriter, r *http.Request) {
		adminJSON(w, http.StatusOK, `{"token":"marie-token"}`)
	})

	tok, err := f.client().Impersonate(context.Background(), "marie")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "marie-token" {
		t.Errorf("token = %q", tok)
	}

	req := f.lastRequest(http.MethodPost)
	var body impersonateRequest
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("request body %q: %v", req.Body, err)
	}
	if body.User != "marie" {
		t.Errorf("request body = %+v", body)
	}
	// The request is the admin's own: it carries the signed-in credential.
	if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Errorf("Authorization = %q", got)
	}
}

// TestImpersonateErrors pins the exit codes scripts branch on, and the two
// meanings of a 404.
func TestImpersonateErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		kind    cberr.Kind
		msg     string
	}{
		{"not an admin", func(w http.ResponseWriter, r *http.Request) {
			adminJSON(w, http.StatusForbidden, `{"message":"You are not an administrator of this server."}`)
		}, cberr.KindPermission, "You are not an administrator of this server."},
		{"not identified", func(w http.ResponseWriter, r *http.Request) {
			adminJSON(w, http.StatusUnauthorized, `{"message":"The server could not identify you."}`)
		}, cberr.KindAuth, "The server could not identify you."},
		{"no such user", func(w http.ResponseWriter, r *http.Request) {
			adminJSON(w, http.StatusNotFound, `{"message":"No such user."}`)
		}, cberr.KindNotFound, "no such user"},
		{"no admin service", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "404 page not found", http.StatusNotFound)
		}, cberr.KindOther, "this server does not offer admin features"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.on(http.MethodPost, adminImpersonatePath, tc.handler)
			_, err := f.client().Impersonate(context.Background(), "marie")
			ce, ok := errors.AsType[*cberr.Error](err)
			if !ok {
				t.Fatalf("err = %v, want a *cberr.Error", err)
			}
			if ce.Kind != tc.kind || ce.Msg != tc.msg {
				t.Errorf("got kind %v msg %q, want kind %v msg %q", ce.Kind, ce.Msg, tc.kind, tc.msg)
			}
		})
	}
}
