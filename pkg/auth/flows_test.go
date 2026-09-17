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

	"github.com/cernbox/cernbox-cli/pkg/cberr"
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
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("access-token-%d", n),
			"token_type":    "Bearer",
			"expires_in":    idp.expiresIn,
			"refresh_token": "refresh-me",
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

func failingSPNEGO(msg string) SPNEGOFunc {
	return func(*http.Request, string) error { return fmt.Errorf("%s", msg) }
}

func ssoConfig(idp *fakeIDP) *SSOConfig {
	return &SSOConfig{
		Issuer:           idp.ts.URL,
		ClientID:         "cernbox-cli",
		Audience:         "cernbox",
		ServicePrincipal: "HTTP/auth.cern.ch",
	}
}

// ── SSO flow ─────────────────────────────────────────────────────────────────

func TestSSOFlow(t *testing.T) {
	idp := newFakeIDP(t)
	flow := &ssoFlow{cfg: ssoConfig(idp), httpClient: idp.ts.Client(), spnego: stubSPNEGO("ticket")}

	tok, err := flow.authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if tok.Value != "Bearer access-token-1" {
		t.Errorf("token = %q", tok.Value)
	}
	if tok.RefreshToken != "refresh-me" {
		t.Errorf("refresh token = %q, want one so a shell loop can renew cheaply", tok.RefreshToken)
	}
	if tok.Expiry.IsZero() {
		t.Error("expiry was not set from expires_in")
	}
	if !strings.HasPrefix(idp.gotNegotiate, "Negotiate ") {
		t.Errorf("the authorization request carried %q, want a Negotiate header", idp.gotNegotiate)
	}
	if idp.gotChallenge == "" || idp.gotVerifier == "" {
		t.Error("PKCE was not used")
	}
	if idp.gotAudience != "cernbox" {
		t.Errorf("audience = %q, want the token scoped to CERNBox", idp.gotAudience)
	}
	if !strings.Contains(idp.gotScope, "offline_access") {
		t.Errorf("scope = %q, want offline_access so a refresh token is issued", idp.gotScope)
	}
}

// TestSSOFlowPKCEMatches verifies the challenge really is the SHA-256 of the
// verifier: sending an unrelated pair would pass a lax server and fail a strict
// one, which is the worst way to find out.
func TestSSOFlowPKCEMatches(t *testing.T) {
	idp := newFakeIDP(t)
	flow := &ssoFlow{cfg: ssoConfig(idp), httpClient: idp.ts.Client(), spnego: stubSPNEGO("ticket")}
	if _, err := flow.authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}

	p := &pkce{Verifier: idp.gotVerifier}
	recomputed, err := newPKCEFrom(p.Verifier)
	if err != nil {
		t.Fatal(err)
	}
	if recomputed.Challenge != idp.gotChallenge {
		t.Errorf("challenge %q is not S256(verifier %q)", idp.gotChallenge, idp.gotVerifier)
	}
}

func TestSSOFlowRejectedTicket(t *testing.T) {
	idp := newFakeIDP(t)
	idp.rejectNegotiate = true
	flow := &ssoFlow{cfg: ssoConfig(idp), httpClient: idp.ts.Client(), spnego: stubSPNEGO("ticket")}

	_, err := flow.authenticate(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "kinit") {
		t.Errorf("a 401 from the IdP should suggest kinit, got %q", err)
	}
}

func TestSSOFlowNoRedirect(t *testing.T) {
	idp := newFakeIDP(t)
	idp.noRedirect = true
	flow := &ssoFlow{cfg: ssoConfig(idp), httpClient: idp.ts.Client(), spnego: stubSPNEGO("ticket")}

	_, err := flow.authenticate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Kerberos authentication may not be enabled") {
		t.Errorf("got %v, want a hint that the client is not Kerberos-enabled", err)
	}
}

// TestSSOFlowRejectsMismatchedState closes the door on a redirect that did not
// come from the request just made.
func TestSSOFlowRejectsMismatchedState(t *testing.T) {
	idp := newFakeIDP(t)
	idp.mismatchedState = true
	flow := &ssoFlow{cfg: ssoConfig(idp), httpClient: idp.ts.Client(), spnego: stubSPNEGO("ticket")}

	_, err := flow.authenticate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mismatched state") {
		t.Errorf("got %v, want the state mismatch to be rejected", err)
	}
}

func TestSSOFlowAuthorizationError(t *testing.T) {
	idp := newFakeIDP(t)
	idp.authError = "unauthorized_client"
	flow := &ssoFlow{cfg: ssoConfig(idp), httpClient: idp.ts.Client(), spnego: stubSPNEGO("ticket")}

	_, err := flow.authenticate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unauthorized_client") {
		t.Errorf("got %v, want the IdP's error surfaced", err)
	}
}

func TestSSOFlowSPNEGOFailureIsExplained(t *testing.T) {
	idp := newFakeIDP(t)
	flow := &ssoFlow{
		cfg: ssoConfig(idp), httpClient: idp.ts.Client(),
		spnego: failingSPNEGO("KDC_ERR_S_PRINCIPAL_UNKNOWN: server not found"),
	}

	_, err := flow.authenticate(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "rdns = false") {
		t.Errorf("an unknown SPN should point at the DNS alias problem, got %q", err)
	}
}

func TestSSORefresh(t *testing.T) {
	idp := newFakeIDP(t)
	flow := &ssoFlow{cfg: ssoConfig(idp), httpClient: idp.ts.Client()}

	tok, err := flow.refresh(context.Background(), "refresh-me")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value == "" {
		t.Error("refresh produced no token")
	}
	if idp.gotGrantType != "refresh_token" {
		t.Errorf("grant type = %q", idp.gotGrantType)
	}
	if idp.gotRefreshWith != "refresh-me" {
		t.Errorf("refresh token sent = %q", idp.gotRefreshWith)
	}
}

func TestSSORefreshRejected(t *testing.T) {
	idp := newFakeIDP(t)
	flow := &ssoFlow{cfg: ssoConfig(idp), httpClient: idp.ts.Client()}

	_, err := flow.refresh(context.Background(), "stale")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("got %v, want the OAuth error surfaced", err)
	}
}

// ── Kerberos provider ────────────────────────────────────────────────────────

// fakeCERNBox serves the direct SPNEGO endpoint.
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
		Mode: KerberosSPNEGO, Endpoint: box.URL,
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
		Mode: KerberosSPNEGO, Endpoint: box.URL,
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
		Mode: KerberosSPNEGO, Endpoint: box.URL,
		HTTPClient: box.Client(), SPNEGO: stubSPNEGO("ticket"),
	}
	_, err := p.Token(withStubTicket(t, p))
	if err == nil || !strings.Contains(err.Error(), "issued no token") {
		t.Errorf("got %v", err)
	}
}

// TestKerberosAutoFallsBackToSSO is the resilience property the two-mode design
// exists for: a server without the Kerberos auth provider still works.
func TestKerberosAutoFallsBackToSSO(t *testing.T) {
	idp := newFakeIDP(t)
	box := fakeCERNBox(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no kerberos here", http.StatusNotFound)
	})

	p := &KerberosProvider{
		Mode: KerberosAuto, Endpoint: box.URL,
		SSO: ssoConfig(idp), HTTPClient: box.Client(), SPNEGO: stubSPNEGO("ticket"),
	}
	tok, err := p.Token(withStubTicket(t, p))
	if err != nil {
		t.Fatalf("auto mode should have fallen back to SSO: %v", err)
	}
	if tok.Value != "Bearer access-token-1" {
		t.Errorf("token = %q, want the SSO-issued one", tok.Value)
	}
}

func TestKerberosAutoPrefersSPNEGO(t *testing.T) {
	idp := newFakeIDP(t)
	box := fakeCERNBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-access-token", "reva-jwt-value")
		w.WriteHeader(http.StatusOK)
	})

	p := &KerberosProvider{
		Mode: KerberosAuto, Endpoint: box.URL,
		SSO: ssoConfig(idp), HTTPClient: box.Client(), SPNEGO: stubSPNEGO("ticket"),
	}
	tok, err := p.Token(withStubTicket(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "Bearer reva-jwt-value" {
		t.Errorf("token = %q, want the direct SPNEGO result", tok.Value)
	}
	if idp.issued.Load() != 0 {
		t.Error("the SSO server was contacted even though SPNEGO succeeded")
	}
}

func TestKerberosAutoReportsBothFailures(t *testing.T) {
	idp := newFakeIDP(t)
	idp.rejectNegotiate = true
	box := fakeCERNBox(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no kerberos here", http.StatusNotFound)
	})

	p := &KerberosProvider{
		Mode: KerberosAuto, Endpoint: box.URL,
		SSO: ssoConfig(idp), HTTPClient: box.Client(), SPNEGO: stubSPNEGO("ticket"),
	}
	_, err := p.Token(withStubTicket(t, p))
	if err == nil {
		t.Fatal("expected an error")
	}
	// In auto mode the user cannot tell which leg to investigate unless both
	// are reported.
	if !strings.Contains(err.Error(), "spnego:") || !strings.Contains(err.Error(), "sso:") {
		t.Errorf("got %q, want both legs reported", err)
	}
}

func TestKerberosSSOMode(t *testing.T) {
	idp := newFakeIDP(t)
	p := &KerberosProvider{
		Mode: KerberosSSO, Endpoint: "https://cernbox.test",
		SSO: ssoConfig(idp), HTTPClient: idp.ts.Client(), SPNEGO: stubSPNEGO("ticket"),
	}
	tok, err := p.Token(withStubTicket(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Provider != MethodKerberos {
		t.Errorf("provider = %q", tok.Provider)
	}
	if tok.Subject != "einstein" {
		t.Errorf("subject = %q, want the principal without its realm", tok.Subject)
	}
}

func TestKerberosRefresh(t *testing.T) {
	idp := newFakeIDP(t)
	p := &KerberosProvider{
		Mode: KerberosSSO, Endpoint: "https://cernbox.test",
		SSO: ssoConfig(idp), HTTPClient: idp.ts.Client(),
	}

	tok, err := p.Refresh(context.Background(), &Token{RefreshToken: "refresh-me", Subject: "einstein"})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Subject != "einstein" {
		t.Errorf("subject = %q, want it carried across the refresh", tok.Subject)
	}
}

// TestKerberosRefreshKeepsRefreshToken: not every provider rotates them, and
// dropping it would break the next refresh.
func TestKerberosRefreshKeepsRefreshToken(t *testing.T) {
	idp := newFakeIDP(t)
	flow := &ssoFlow{cfg: ssoConfig(idp), httpClient: idp.ts.Client()}

	tok, err := flow.refresh(context.Background(), "refresh-me")
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken == "" {
		t.Error("the refresh token was dropped")
	}
}

func TestParseKerberosMode(t *testing.T) {
	for in, want := range map[string]KerberosMode{
		"":       KerberosSSO,
		"sso":    KerberosSSO,
		"SSO":    KerberosSSO,
		"spnego": KerberosSPNEGO,
		"auto":   KerberosAuto,
	} {
		got, err := ParseKerberosMode(in)
		if err != nil {
			t.Errorf("ParseKerberosMode(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseKerberosMode(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParseKerberosMode("magic"); cberr.KindOf(err) != cberr.KindUsage {
		t.Errorf("an unknown mode should be a usage error, got %v", err)
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
