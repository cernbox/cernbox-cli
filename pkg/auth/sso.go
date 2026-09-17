package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ssoFlow performs the Kerberos-authenticated authorization code exchange
// against CERN SSO.
//
// The shape is: GET the authorization endpoint carrying a SPNEGO header, let
// the identity provider authenticate the Kerberos ticket and redirect back with
// a code, then exchange that code at the token endpoint with PKCE. This is
// mechanically what auth-get-sso-token does on lxplus, implemented natively so
// the binary also works where that RPM is absent — and because the SPNEGO code
// is the same code the direct-to-CERNBox mode needs.
type ssoFlow struct {
	cfg        *SSOConfig
	httpClient *http.Client
	spnego     SPNEGOFunc
}

func (f *ssoFlow) authenticate(ctx context.Context) (*Token, error) {
	meta, err := discover(ctx, f.httpClient, f.cfg.Issuer)
	if err != nil {
		return nil, err
	}
	if meta.AuthorizationEndpoint == "" {
		return nil, fmt.Errorf("the identity provider at %s advertises no authorization endpoint", f.cfg.Issuer)
	}

	verifier, err := newPKCE()
	if err != nil {
		return nil, err
	}
	state, err := randomState()
	if err != nil {
		return nil, err
	}

	code, err := f.authorizationCode(ctx, meta, verifier, state)
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {f.cfg.ClientID},
		"redirect_uri":  {f.cfg.redirectURI()},
		"code_verifier": {verifier.Verifier},
	}
	if f.cfg.Audience != "" {
		form.Set("audience", f.cfg.Audience)
	}

	tr, err := postForm(ctx, f.httpClient, meta.TokenEndpoint, form)
	if err != nil {
		return nil, fmt.Errorf("exchanging the authorization code: %w", err)
	}
	return tr.toToken(MethodKerberos), nil
}

// authorizationCode performs the Kerberos leg and extracts the code from the
// redirect.
func (f *ssoFlow) authorizationCode(ctx context.Context, meta *providerMetadata, verifier *pkce, state string) (string, error) {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.cfg.ClientID},
		"redirect_uri":          {f.cfg.redirectURI()},
		"scope":                 {strings.Join(f.cfg.scopes(), " ")},
		"state":                 {state},
		"code_challenge":        {verifier.Challenge},
		"code_challenge_method": {"S256"},
	}
	if f.cfg.Audience != "" {
		q.Set("audience", f.cfg.Audience)
	}

	authURL := meta.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return "", err
	}
	if f.spnego == nil {
		return "", fmt.Errorf("no Kerberos token generator configured")
	}
	spn := f.cfg.spn()
	if spn == "" {
		return "", fmt.Errorf("cannot derive a service principal from the issuer %q", f.cfg.Issuer)
	}
	if err := f.spnego(req, spn); err != nil {
		return "", spnegoAdvice(err, spn)
	}

	// The redirect carries the code, so it must not be followed: the redirect
	// target is typically out-of-band and following it would either 404 or
	// leak the code to whatever is listening there.
	client := *f.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("contacting the identity provider: %w", err)
	}
	defer resp.Body.Close()
	drainBody(resp)

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("the identity provider rejected the Kerberos ticket (HTTP 401); " +
			"your ticket may have expired, try 'kinit'")
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("the identity provider returned %s without a redirect; "+
			"Kerberos authentication may not be enabled for this client", resp.Status)
	}

	loc, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("the identity provider returned an unparseable redirect: %w", err)
	}
	params := loc.Query()
	if errCode := params.Get("error"); errCode != "" {
		msg := errCode
		if desc := params.Get("error_description"); desc != "" {
			msg += ": " + desc
		}
		return "", fmt.Errorf("the identity provider refused the request: %s", msg)
	}

	// The state check closes the door on a redirect that did not originate from
	// the request just made.
	if got := params.Get("state"); got != "" && got != state {
		return "", fmt.Errorf("the identity provider returned a mismatched state parameter")
	}

	code := params.Get("code")
	if code == "" {
		return "", fmt.Errorf("the identity provider's redirect carried no authorization code")
	}
	return code, nil
}

// refresh exchanges a refresh token for a new access token.
func (f *ssoFlow) refresh(ctx context.Context, refreshToken string) (*Token, error) {
	meta, err := discover(ctx, f.httpClient, f.cfg.Issuer)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {f.cfg.ClientID},
	}
	if f.cfg.Audience != "" {
		form.Set("audience", f.cfg.Audience)
	}

	tr, err := postForm(ctx, f.httpClient, meta.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	tok := tr.toToken(MethodKerberos)
	if tok.RefreshToken == "" {
		// Not every provider rotates refresh tokens; keep the existing one so
		// the next refresh still has something to present.
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}
