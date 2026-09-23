package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// DeviceProvider authenticates through the OAuth device authorization grant
// (RFC 8628): the CLI prints a URL and a short code, the user completes the
// login in a browser, and the CLI polls until it succeeds.
//
// This is the path for laptops, and for accounts that have no Kerberos
// principal at all — lightweight and external accounts — which is why it stays
// in the chain even on lxplus.
type DeviceProvider struct {
	// SSO configures the OIDC client.
	SSO *SSOConfig
	// HTTPClient is used for discovery and polling.
	HTTPClient *http.Client
	// Interactive reports whether a user is present to complete the flow. The
	// CLI wires it to a TTY check, so a script does not hang for fifteen
	// minutes waiting for a login nobody is performing.
	Interactive bool
	// Prompt displays the verification URL and user code.
	Prompt func(verificationURI, userCode string, expiresIn time.Duration)
	// MaxWait bounds the polling. Zero uses the interval the server advertises.
	MaxWait time.Duration
}

// Polling pace. The floor applies even when the server advertises no interval
// at all.
const (
	defaultPollInterval = 5 * time.Second
	minPollInterval     = 10 * time.Millisecond
)

// Name implements Provider.
func (p *DeviceProvider) Name() string { return MethodDevice }

// Available implements Provider. A device flow needs someone to complete it, so
// it is unavailable in a non-interactive context.
func (p *DeviceProvider) Available(context.Context) bool {
	return p.Interactive && p.SSO != nil && p.SSO.Issuer != "" && p.SSO.ClientID != ""
}

type deviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	// Interval is a pointer so that "absent" and "explicitly zero" stay
	// distinguishable: RFC 8628 says an absent interval means five seconds,
	// while a server that sends zero is saying it has no rate limit.
	Interval *int64 `json:"interval"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Token implements Provider.
func (p *DeviceProvider) Token(ctx context.Context) (*Token, error) {
	hc := p.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	meta, err := discover(ctx, hc, p.SSO.Issuer)
	if err != nil {
		return nil, err
	}
	if meta.DeviceAuthorizationEndpoint == "" {
		return nil, cberr.Authf("this identity provider does not support the device flow; " +
			"use 'cernbox login --method kerberos' or an app token instead")
	}

	auth, err := p.startDeviceAuth(ctx, hc, meta)
	if err != nil {
		return nil, err
	}

	verificationURI := auth.VerificationURIComplete
	if verificationURI == "" {
		verificationURI = auth.VerificationURI
	}
	if p.Prompt != nil {
		p.Prompt(verificationURI, auth.UserCode, time.Duration(auth.ExpiresIn)*time.Second)
	}

	return p.poll(ctx, hc, meta, auth)
}

// Refresh implements Refresher, renewing the access token from the refresh
// token that offline_access yielded.
//
// This is what keeps a CERN SSO session usable: the access token lives twenty
// minutes, and without this the user is sent back to the browser every twenty
// minutes even though the issuer handed over a refresh token specifically so
// that they would not be.
//
// It needs no terminal, so a cron job with a cached refresh token renews
// silently — the chain offers every provider a chance to refresh regardless of
// whether it could start a fresh login.
func (p *DeviceProvider) Refresh(ctx context.Context, tok *Token) (*Token, error) {
	if tok == nil || tok.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token to renew with")
	}
	if p.SSO == nil || p.SSO.Issuer == "" {
		return nil, fmt.Errorf("no SSO issuer configured")
	}

	hc := p.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	meta, err := discover(ctx, hc, p.SSO.Issuer)
	if err != nil {
		return nil, err
	}

	tr, err := postForm(ctx, hc, meta.TokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {p.SSO.ClientID},
	})
	if err != nil {
		return nil, err
	}

	out := tr.toToken(MethodDevice)
	if out.RefreshToken == "" {
		// Not every issuer rotates the refresh token; keep the one that still
		// works rather than losing the ability to refresh again.
		out.RefreshToken = tok.RefreshToken
	}
	out.Subject = tok.Subject
	return out, nil
}

func (p *DeviceProvider) startDeviceAuth(ctx context.Context, hc *http.Client, meta *providerMetadata) (*deviceAuthResponse, error) {
	form := url.Values{
		"client_id": {p.SSO.ClientID},
		"scope":     {joinScopes(p.SSO.scopes())},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		meta.DeviceAuthorizationEndpoint, stringReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("starting the device login: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var auth deviceAuthResponse
	if err := json.Unmarshal(body, &auth); err != nil {
		return nil, fmt.Errorf("the device endpoint returned %s with an unparseable body", resp.Status)
	}
	if auth.Error != "" {
		msg := auth.Error
		if auth.ErrorDescription != "" {
			msg += ": " + auth.ErrorDescription
		}
		return nil, cberr.Authf("starting the device login: %s", msg)
	}
	if auth.DeviceCode == "" || auth.UserCode == "" {
		return nil, cberr.Authf("the device endpoint returned an incomplete response")
	}
	return &auth, nil
}

// poll waits for the user to complete the login.
//
// The two OAuth "keep waiting" codes are handled distinctly: authorization_pending
// means carry on at the current rate, slow_down means the server wants a longer
// interval and ignoring it gets the client rate-limited.
func (p *DeviceProvider) poll(ctx context.Context, hc *http.Client, meta *providerMetadata, auth *deviceAuthResponse) (*Token, error) {
	interval := defaultPollInterval
	if auth.Interval != nil {
		interval = time.Duration(*auth.Interval) * time.Second
	}
	// Never poll faster than the floor, even if the server says zero: a tight
	// loop against the token endpoint is how a client gets rate-limited.
	interval = max(interval, minPollInterval)

	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	if auth.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}
	if p.MaxWait > 0 {
		if earlier := time.Now().Add(p.MaxWait); earlier.Before(deadline) {
			deadline = earlier
		}
	}

	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {auth.DeviceCode},
		"client_id":   {p.SSO.ClientID},
	}

	// The first poll happens immediately: the user may have completed the
	// login while the prompt was still being printed, and making them wait a
	// full interval for a login that already succeeded feels broken.
	first := true
	for {
		if !first {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(interval):
			}
		}
		first = false

		if time.Now().After(deadline) {
			return nil, cberr.Authf("the login was not completed in time")
		}

		tr, err := postForm(ctx, hc, meta.TokenEndpoint, form)
		if err == nil {
			return tr.toToken(MethodDevice), nil
		}
		if tr == nil {
			return nil, err
		}
		switch tr.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied":
			return nil, cberr.Authf("the login was refused")
		case "expired_token":
			return nil, cberr.Authf("the login code expired before it was used")
		default:
			return nil, cberr.Authf("device login failed: %s", oauthErrorMessage(tr))
		}
	}
}

func joinScopes(scopes []string) string {
	out := ""
	for i, s := range scopes {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

func stringReader(s string) io.Reader {
	return &stringReadCloser{s: s}
}

type stringReadCloser struct {
	s string
	i int
}

func (r *stringReadCloser) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}
