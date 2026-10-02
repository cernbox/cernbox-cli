package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

const (
	adminStatusPath      = "/admin/status"
	adminImpersonatePath = "/admin/impersonate"
)

type adminStatusResponse struct {
	Admin bool `json:"admin"`
}

// IsAdmin reports whether the signed-in user is an admin of the server, that
// is, whether they may impersonate other users. Asking does not elevate and
// leaves no audit trail.
func (c *Client) IsAdmin(ctx context.Context) (bool, error) {
	var res adminStatusResponse
	err := c.adminCall(ctx, request{
		method: http.MethodGet,
		url:    c.URL(adminStatusPath),
		op:     "check admin status",
	}, &res)
	return res.Admin, err
}

type impersonateRequest struct {
	User string `json:"user"`
}

type impersonateResponse struct {
	Token string `json:"token"`
}

// Impersonate obtains a token acting as user. It is an ordinary user token: the
// server treats every request made with it as made by that user. The server
// checks that the signed-in user is an admin and records the impersonation in
// its audit log.
func (c *Client) Impersonate(ctx context.Context, user string) (string, error) {
	const op = "act as"
	payload, err := json.Marshal(impersonateRequest{User: user})
	if err != nil {
		return "", cberr.Wrap(cberr.KindOther, op, user, err)
	}
	var res impersonateResponse
	err = c.adminCall(ctx, request{
		method: http.MethodPost,
		url:    c.URL(adminImpersonatePath),
		header: http.Header{"Content-Type": []string{"application/json"}},
		body:   bytesBody(payload),
		op:     op,
		path:   user,
	}, &res)
	if err != nil {
		return "", err
	}
	if res.Token == "" {
		return "", cberr.New(cberr.KindOther, op, user, "the server returned no token")
	}
	return res.Token, nil
}

// adminCall makes a request to the admin endpoints and decodes the answer.
//
// It tells apart the two 404s those endpoints can produce. The admin service
// answers in JSON, so a JSON 404 is its own — "no such user" — while any other
// 404 means the server has no admin service at all. The generic "no such file
// or directory" would be wrong for both.
func (c *Client) adminCall(ctx context.Context, r request, out any) error {
	r.header = cloneHeader(r.header)
	r.header.Set("Accept", "application/json")
	r.expects = []int{http.StatusOK, http.StatusNotFound}
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			return cberr.New(cberr.KindNotFound, r.op, r.path, "no such user")
		}
		return cberr.New(cberr.KindOther, r.op, r.path, "this server does not offer admin features")
	}
	return decodeJSON(resp.Body, out, r.op, r.path)
}

func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return http.Header{}
	}
	return h.Clone()
}
