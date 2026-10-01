package client

import (
	"context"
	"net/http"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// ServerTime returns the time the server believes it is, read from the Date
// header that every HTTP response carries.
//
// The request is unauthenticated and accepts any status code, both on purpose.
// A clock is what a credential depends on — Kerberos refuses a ticket more than
// five minutes out of step, and every bearer token has an expiry — so the clock
// has to be readable before a credential is, and a 401 answers this question as
// well as a 200 does.
func (c *Client) ServerTime(ctx context.Context) (time.Time, error) {
	resp, err := c.do(ctx, request{
		method:    http.MethodHead,
		url:       c.URL("/"),
		op:        "read the server clock",
		noAuth:    true,
		anyStatus: true,
	})
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()

	v := resp.Header.Get("Date")
	if v == "" {
		return time.Time{}, cberr.New(cberr.KindOther, "read the server clock", "",
			"the server sent no Date header")
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return time.Time{}, cberr.Wrap(cberr.KindOther, "read the server clock", "", err)
	}
	return t, nil
}
