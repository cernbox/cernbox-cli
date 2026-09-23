package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeIDP is a stand-in CERN SSO: a discovery document, an authorization
// endpoint that accepts a SPNEGO header, a token endpoint that enforces PKCE,
// and a device flow.
type fakeIDP struct {
	ts *httptest.Server

	// Behaviour switches.
	rejectNegotiate  bool
	noRedirect       bool
	mismatchedState  bool
	authError        string
	expiresIn        int64
	noDeviceEndpoint bool
	devicePendingFor int32
	// noNewRefreshToken makes the issuer omit a rotated refresh token, which
	// not every provider sends.
	noNewRefreshToken bool

	// Captured for assertions.
	gotNegotiate   string
	gotChallenge   string
	gotVerifier    string
	gotAudience    string
	gotScope       string
	gotGrantType   string
	gotRefreshWith string

	devicePolls atomic.Int32
	issued      atomic.Int32
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	idp := &fakeIDP{expiresIn: 1200}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]string{
			"issuer":                 idp.ts.URL,
			"authorization_endpoint": idp.ts.URL + "/authorize",
			"token_endpoint":         idp.ts.URL + "/token",
		}
		if !idp.noDeviceEndpoint {
			meta["device_authorization_endpoint"] = idp.ts.URL + "/device"
		}
		json.NewEncoder(w).Encode(meta)
	})

	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		idp.gotNegotiate = r.Header.Get("Authorization")
		q := r.URL.Query()
		idp.gotChallenge = q.Get("code_challenge")
		idp.gotAudience = q.Get("audience")
		idp.gotScope = q.Get("scope")

		if idp.rejectNegotiate || !strings.HasPrefix(idp.gotNegotiate, "Negotiate ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if idp.noRedirect {
			w.WriteHeader(http.StatusOK)
			return
		}

		redirect, _ := url.Parse(q.Get("redirect_uri"))
		if redirect == nil {
			redirect = &url.URL{Path: "/cb"}
		}
		params := url.Values{}
		if idp.authError != "" {
			params.Set("error", idp.authError)
			params.Set("error_description", "the client is not allowed to do that")
		} else {
			params.Set("code", "auth-code-123")
			state := q.Get("state")
			if idp.mismatchedState {
				state = "not-the-state-you-sent"
			}
			params.Set("state", state)
		}
		redirect.RawQuery = params.Encode()
		w.Header().Set("Location", redirect.String())
		w.WriteHeader(http.StatusFound)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		idp.gotGrantType = r.Form.Get("grant_type")

		switch idp.gotGrantType {
		case "authorization_code":
			idp.gotVerifier = r.Form.Get("code_verifier")
			if idp.gotVerifier == "" {
				writeOAuthError(w, http.StatusBadRequest, "invalid_request", "PKCE verifier missing")
				return
			}
			if r.Form.Get("code") != "auth-code-123" {
				writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown code")
				return
			}
		case "refresh_token":
			idp.gotRefreshWith = r.Form.Get("refresh_token")
			if idp.gotRefreshWith != "refresh-me" {
				writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token expired")
				return
			}
		case "urn:ietf:params:oauth:grant-type:device_code":
			if idp.devicePolls.Add(1) <= idp.devicePendingFor {
				writeOAuthError(w, http.StatusBadRequest, "authorization_pending", "keep waiting")
				return
			}
		default:
			writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", idp.gotGrantType)
			return
		}

		n := idp.issued.Add(1)
		refreshed := "refresh-me"
		if idp.noNewRefreshToken {
			refreshed = ""
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("access-token-%d", n),
			"token_type":    "Bearer",
			"expires_in":    idp.expiresIn,
			"refresh_token": refreshed,
		})
	})

	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "device-code-1",
			"user_code":        "WDJB-MJHT",
			"verification_uri": idp.ts.URL + "/activate",
			"expires_in":       600,
			"interval":         0, // no server-side rate limit; the client floor still applies
		})
	})

	idp.ts = httptest.NewServer(mux)
	t.Cleanup(idp.ts.Close)
	return idp
}

func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

// stubSPNEGO stands in for a real AP-REQ. Producing one needs a live KDC, and
// the logic worth testing here is mode selection and error handling, not the
// cryptography.
func stubSPNEGO(token string) SPNEGOFunc {
	return func(req *http.Request, spn string) error {
		req.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString([]byte(token+":"+spn)))
		return nil
	}
}

func ssoConfig(idp *fakeIDP) *SSOConfig {
	return &SSOConfig{
		Issuer:   idp.ts.URL,
		ClientID: "cernbox-cli",
	}
}

// ── Kerberos provider ────────────────────────────────────────────────────────

// fakeCERNBox serves the SPNEGO endpoint.
func fakeCERNBox(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestKerberosSPNEGOMode(t *testing.T) {
	var gotAuth string
	box := fakeCERNBox(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("x-access-token", "reva-jwt-value")
		w.WriteHeader(http.StatusOK)
	})

	p := &KerberosProvider{
		Endpoint:   box.URL,
		HTTPClient: box.Client(), SPNEGO: stubSPNEGO("ticket"),
	}
	tok, err := p.Token(withStubTicket(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "Bearer reva-jwt-value" {
		t.Errorf("token = %q, want the reva token from x-access-token", tok.Value)
	}
	if !strings.HasPrefix(gotAuth, "Negotiate ") {
		t.Errorf("request carried %q, want a Negotiate header", gotAuth)
	}
}

func TestKerberosSPNEGOModeNotSupportedByServer(t *testing.T) {
	box := fakeCERNBox(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such endpoint", http.StatusNotFound)
	})

	p := &KerberosProvider{
		Endpoint:   box.URL,
		HTTPClient: box.Client(), SPNEGO: stubSPNEGO("ticket"),
	}
	_, err := p.Token(withStubTicket(t, p))
	if err == nil || !strings.Contains(err.Error(), "does not accept Kerberos directly") {
		t.Errorf("got %v, want a clear 'server has no Kerberos provider' error", err)
	}
}

func TestKerberosSPNEGOModeWithoutToken(t *testing.T) {
	box := fakeCERNBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	p := &KerberosProvider{
		Endpoint:   box.URL,
		HTTPClient: box.Client(), SPNEGO: stubSPNEGO("ticket"),
	}
	_, err := p.Token(withStubTicket(t, p))
	if err == nil || !strings.Contains(err.Error(), "issued no token") {
		t.Errorf("got %v", err)
	}
}

func TestKerberosServicePrincipalDerivation(t *testing.T) {
	p := &KerberosProvider{Endpoint: "https://cernbox.cern.ch"}
	spn, err := p.spn()
	if err != nil {
		t.Fatal(err)
	}
	if spn != "HTTP/cernbox.cern.ch" {
		t.Errorf("spn = %q, want HTTP/cernbox.cern.ch", spn)
	}

	explicit := &KerberosProvider{Endpoint: "https://alias.cern.ch", ServicePrincipal: "HTTP/real.cern.ch"}
	if spn, _ := explicit.spn(); spn != "HTTP/real.cern.ch" {
		t.Errorf("an explicit SPN should win, got %q", spn)
	}
}

func TestTicketUsername(t *testing.T) {
	if got := (Ticket{Principal: "gdelmont@CERN.CH"}).Username(); got != "gdelmont" {
		t.Errorf("Username() = %q", got)
	}
	if got := (Ticket{Principal: "gdelmont"}).Username(); got != "gdelmont" {
		t.Errorf("Username() with no realm = %q", got)
	}
}

func TestDefaultCCachePathHonoursKRB5CCNAME(t *testing.T) {
	t.Setenv("KRB5CCNAME", "FILE:/tmp/krb5cc_custom")
	if got := DefaultCCachePath(); got != "/tmp/krb5cc_custom" {
		t.Errorf("DefaultCCachePath() = %q, want the FILE: prefix stripped", got)
	}

	t.Setenv("KRB5CCNAME", "")
	if got := DefaultCCachePath(); !strings.Contains(got, "krb5cc_") {
		t.Errorf("DefaultCCachePath() = %q", got)
	}
}

// TestLoadTicketRejectsNonFileCaches: KEYRING and KCM caches cannot be read as
// files, and a confusing parse error is worse than saying so.
func TestLoadTicketRejectsNonFileCaches(t *testing.T) {
	for _, path := range []string{"KEYRING:persistent:1000", "KCM:1000", "DIR:/run/user/1000/krb5cc"} {
		_, err := loadTicket(path)
		if err == nil {
			t.Errorf("loadTicket(%q) should fail", path)
			continue
		}
		if !strings.Contains(err.Error(), "FILE:") {
			t.Errorf("loadTicket(%q) error = %q, want it to explain the limitation", path, err)
		}
	}
}

func TestLoadTicketMissingCache(t *testing.T) {
	_, err := loadTicket(t.TempDir() + "/nope")
	if err == nil || !strings.Contains(err.Error(), "kinit") {
		t.Errorf("got %v, want advice to run kinit", err)
	}
}

func TestKerberosProviderUnavailableWithoutTicket(t *testing.T) {
	p := &KerberosProvider{CCachePath: t.TempDir() + "/nope"}
	if p.Available(context.Background()) {
		t.Error("the Kerberos provider should be unavailable with no ticket")
	}
	if p.IdentityHint() != "" {
		t.Error("no ticket means no identity hint")
	}
}

// ── device flow ──────────────────────────────────────────────────────────────

func TestDeviceFlow(t *testing.T) {
	idp := newFakeIDP(t)
	var promptedURI, promptedCode string

	p := &DeviceProvider{
		SSO: ssoConfig(idp), HTTPClient: idp.ts.Client(), Interactive: true,
		Prompt: func(uri, code string, _ time.Duration) {
			promptedURI, promptedCode = uri, code
		},
	}
	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Provider != MethodDevice {
		t.Errorf("provider = %q", tok.Provider)
	}
	if promptedCode != "WDJB-MJHT" {
		t.Errorf("the user code was not shown: %q", promptedCode)
	}
	if !strings.Contains(promptedURI, "/activate") {
		t.Errorf("the verification URL was not shown: %q", promptedURI)
	}
}

// TestDeviceFlowPolls checks the authorization_pending loop: the CLI must keep
// waiting while the user completes the login in a browser.
func TestDeviceFlowPolls(t *testing.T) {
	idp := newFakeIDP(t)
	idp.devicePendingFor = 2

	p := &DeviceProvider{SSO: ssoConfig(idp), HTTPClient: idp.ts.Client(), Interactive: true}
	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value == "" {
		t.Error("no token after polling")
	}
	if got := idp.devicePolls.Load(); got != 3 {
		t.Errorf("polled %d times, want 3 (two pending, then success)", got)
	}
}

// TestDeviceFlowUnavailableWithoutATTY: a cron job must fail fast rather than
// wait fifteen minutes for a login nobody is performing.
func TestDeviceFlowUnavailableWithoutATTY(t *testing.T) {
	idp := newFakeIDP(t)
	p := &DeviceProvider{SSO: ssoConfig(idp), HTTPClient: idp.ts.Client(), Interactive: false}
	if p.Available(context.Background()) {
		t.Error("the device flow should be unavailable in a non-interactive context")
	}
}

func TestDeviceFlowWithoutServerSupport(t *testing.T) {
	idp := newFakeIDP(t)
	idp.noDeviceEndpoint = true

	p := &DeviceProvider{SSO: ssoConfig(idp), HTTPClient: idp.ts.Client(), Interactive: true}
	_, err := p.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not support the device flow") {
		t.Errorf("got %v, want a clear explanation and an alternative", err)
	}
}

func TestDeviceFlowTimesOut(t *testing.T) {
	idp := newFakeIDP(t)
	idp.devicePendingFor = 1000

	p := &DeviceProvider{
		SSO: ssoConfig(idp), HTTPClient: idp.ts.Client(), Interactive: true,
		MaxWait: 10 * time.Millisecond,
	}
	_, err := p.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not completed in time") {
		t.Errorf("got %v, want a timeout", err)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// withStubTicket installs a Kerberos ticket on the provider without needing a
// KDC, and returns a context for the call.
func withStubTicket(t *testing.T, p *KerberosProvider) context.Context {
	t.Helper()
	p.once.Do(func() {
		p.ticket = &Ticket{Principal: "einstein@CERN.CH", Realm: "CERN.CH", CachePath: "/tmp/stub"}
	})
	return context.Background()
}

// ── refresh ──────────────────────────────────────────────────────────────────

// TestDeviceRefresh: a CERN SSO access token lives twenty minutes. Without
// honouring the refresh token that offline_access yields, the user is sent back
// to the browser every twenty minutes for no reason.
func TestDeviceRefresh(t *testing.T) {
	idp := newFakeIDP(t)
	p := &DeviceProvider{SSO: ssoConfig(idp), HTTPClient: idp.ts.Client()}

	got, err := p.Refresh(context.Background(), &Token{
		RefreshToken: "refresh-me", Subject: "gdelmont", Provider: MethodDevice,
	})
	if err != nil {
		t.Fatal(err)
	}
	if idp.gotGrantType != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", idp.gotGrantType)
	}
	if idp.gotRefreshWith != "refresh-me" {
		t.Errorf("the issuer was sent %q", idp.gotRefreshWith)
	}
	if got.Value == "" {
		t.Error("no access token came back")
	}
	if got.Subject != "gdelmont" {
		t.Errorf("Subject = %q, want it carried over", got.Subject)
	}
}

// TestDeviceRefreshKeepsTheRefreshToken: not every issuer rotates it, and
// dropping it costs the ability to refresh a second time.
func TestDeviceRefreshKeepsTheRefreshToken(t *testing.T) {
	idp := newFakeIDP(t)
	idp.noNewRefreshToken = true
	p := &DeviceProvider{SSO: ssoConfig(idp), HTTPClient: idp.ts.Client()}

	got, err := p.Refresh(context.Background(), &Token{RefreshToken: "refresh-me"})
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "refresh-me" {
		t.Errorf("RefreshToken = %q, want the original kept", got.RefreshToken)
	}
}

// TestDeviceRefreshRejected: an expired refresh token is an error here, and the
// chain then falls through to authenticating normally.
func TestDeviceRefreshRejected(t *testing.T) {
	idp := newFakeIDP(t)
	p := &DeviceProvider{SSO: ssoConfig(idp), HTTPClient: idp.ts.Client()}

	if _, err := p.Refresh(context.Background(), &Token{RefreshToken: "stale"}); err == nil {
		t.Fatal("a refresh token the issuer rejects should be an error")
	}
}

// TestDeviceRefreshWithoutAToken needs no network at all.
func TestDeviceRefreshWithoutAToken(t *testing.T) {
	p := &DeviceProvider{}
	if _, err := p.Refresh(context.Background(), &Token{}); err == nil {
		t.Error("refreshing without a refresh token should fail")
	}
	if _, err := p.Refresh(context.Background(), nil); err == nil {
		t.Error("refreshing a nil token should fail")
	}
}

// TestDeviceProviderIsARefresher pins the wiring: the chain only refreshes a
// provider that implements the interface, so losing it silently reinstates the
// twenty-minute re-login.
func TestDeviceProviderIsARefresher(t *testing.T) {
	var p any = &DeviceProvider{}
	if _, ok := p.(Refresher); !ok {
		t.Fatal("DeviceProvider must implement Refresher or the chain cannot renew a session")
	}
}
