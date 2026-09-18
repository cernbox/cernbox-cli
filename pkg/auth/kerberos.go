package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	krb5client "github.com/jcmturner/gokrb5/v8/client"
	krb5config "github.com/jcmturner/gokrb5/v8/config"
	"github.com/jcmturner/gokrb5/v8/credentials"
	"github.com/jcmturner/gokrb5/v8/spnego"
)

// SPNEGOFunc sets a SPNEGO Authorization header on a request for the given
// service principal.
//
// It is a function rather than a method so that tests can substitute one
// without a KDC: generating a real AP-REQ needs a live Kerberos realm, and the
// logic worth testing here — mode selection, fallback, error messages — is not
// the cryptography.
type SPNEGOFunc func(req *http.Request, spn string) error

// Ticket describes the Kerberos credential the CLI found.
type Ticket struct {
	// Principal is the full client principal, "gdelmont@CERN.CH".
	Principal string
	// Realm is the principal's realm.
	Realm string
	// CachePath is the credential cache the ticket came from.
	CachePath string
}

// Username is the principal without its realm, which is what CERNBox knows the
// user as.
func (t Ticket) Username() string {
	name, _, _ := strings.Cut(t.Principal, "@")
	return name
}

// KerberosProvider turns a Kerberos ticket into a CERNBox credential.
type KerberosProvider struct {
	// Endpoint is the CERNBox base URL, used by SPNEGO mode.
	Endpoint string
	// SPNEGOPath is the endpoint that accepts a SPNEGO token and returns a
	// reva token.
	SPNEGOPath string
	// ServicePrincipal is the SPN to request a ticket for. When empty it is
	// derived from the endpoint host as HTTP/<host>.
	ServicePrincipal string
	// HTTPClient is used for both legs.
	HTTPClient *http.Client
	// CCachePath overrides the credential cache location. Empty means
	// $KRB5CCNAME, then the conventional /tmp/krb5cc_<uid>.
	CCachePath string
	// ConfigPath overrides /etc/krb5.conf.
	ConfigPath string
	// SPNEGO overrides the SPNEGO header generator. Tests set it.
	SPNEGO SPNEGOFunc

	once   sync.Once
	ticket *Ticket
	tkErr  error
}

// Name implements Provider.
func (p *KerberosProvider) Name() string { return MethodKerberos }

// Available implements Provider. It only inspects the local credential cache,
// so it is cheap and never blocks on the network.
func (p *KerberosProvider) Available(context.Context) bool {
	t, err := p.Ticket()
	return err == nil && t.Principal != ""
}

// IdentityHint implements IdentityHinter, returning the Kerberos principal so
// that switching principal with kinit selects a different cache entry rather
// than reusing the previous identity's token.
func (p *KerberosProvider) IdentityHint() string {
	t, err := p.Ticket()
	if err != nil {
		return ""
	}
	return t.Principal
}

// Ticket returns the Kerberos credential found in the local cache.
func (p *KerberosProvider) Ticket() (*Ticket, error) {
	p.once.Do(func() {
		p.ticket, p.tkErr = loadTicket(p.ccachePath())
	})
	return p.ticket, p.tkErr
}

// Token implements Provider.
func (p *KerberosProvider) Token(ctx context.Context) (*Token, error) {
	ticket, err := p.Ticket()
	if err != nil {
		return nil, err
	}

	return p.spnegoToken(ctx, ticket)
}

// spnegoToken presents the ticket to CERNBox and reads back a reva token.
func (p *KerberosProvider) spnegoToken(ctx context.Context, ticket *Ticket) (*Token, error) {
	endpoint := strings.TrimSuffix(p.Endpoint, "/") + p.spnegoPath()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	spn, err := p.spn()
	if err != nil {
		return nil, err
	}
	setHeader := p.SPNEGO
	if setHeader == nil {
		setHeader = p.defaultSPNEGO
	}
	if err := setHeader(req, spn); err != nil {
		return nil, spnegoAdvice(err, spn)
	}

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	drainBody(resp)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("this server does not accept Kerberos directly (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}

	// reva's token writer puts the issued token in x-access-token.
	value := resp.Header.Get("x-access-token")
	if value == "" {
		return nil, fmt.Errorf("the server accepted the ticket but issued no token")
	}

	return &Token{
		Header:   "Authorization",
		Value:    "Bearer " + value,
		Provider: MethodKerberos,
		Subject:  ticket.Username(),
		Expiry:   jwtExpiry(value),
	}, nil
}

func (p *KerberosProvider) spnegoPath() string {
	if p.SPNEGOPath != "" {
		return p.SPNEGOPath
	}
	return "/graph/v1.0/me"
}

// spn derives the service principal name from the endpoint host.
func (p *KerberosProvider) spn() (string, error) {
	if p.ServicePrincipal != "" {
		return p.ServicePrincipal, nil
	}
	u, err := url.Parse(p.Endpoint)
	if err != nil {
		return "", cberr.Usagef("cannot derive a service principal from %q: %v", p.Endpoint, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", cberr.Usagef("cannot derive a service principal from %q", p.Endpoint)
	}
	return "HTTP/" + host, nil
}

func (p *KerberosProvider) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return http.DefaultClient
}

func (p *KerberosProvider) ccachePath() string {
	if p.CCachePath != "" {
		return p.CCachePath
	}
	return DefaultCCachePath()
}

// defaultSPNEGO builds a real SPNEGO token from the credential cache.
//
// Mutual authentication is deliberately not requested. reva's HTTP frontend
// does not hold the service keytab — the ticket is forwarded over gRPC to the
// auth provider, which is a different process, and the CS3 Authenticate RPC has
// no field to carry a negotiation response back. Asking for mutual auth would
// therefore fail against a correctly configured server. The channel is TLS with
// a verified certificate, which is how most SPNEGO-over-HTTPS deployments run.
func (p *KerberosProvider) defaultSPNEGO(req *http.Request, spn string) error {
	cfg, err := p.krb5Config()
	if err != nil {
		return err
	}
	cc, err := credentials.LoadCCache(p.ccachePath())
	if err != nil {
		return fmt.Errorf("reading the Kerberos credential cache %s: %w", p.ccachePath(), err)
	}
	cl, err := krb5client.NewFromCCache(cc, cfg, krb5client.DisablePAFXFAST(true))
	if err != nil {
		return fmt.Errorf("building a Kerberos client: %w", err)
	}
	return spnego.SetSPNEGOHeader(cl, req, spn)
}

func (p *KerberosProvider) krb5Config() (*krb5config.Config, error) {
	path := p.ConfigPath
	if path == "" {
		path = os.Getenv("KRB5_CONFIG")
	}
	if path == "" {
		path = "/etc/krb5.conf"
	}
	cfg, err := krb5config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return cfg, nil
}

// spnegoAdvice turns a raw GSS failure into something actionable.
//
// The single most likely deployment problem is an SPN mismatch: cernbox.cern.ch
// is an alias in front of several nodes, and a client that canonicalises the
// host through reverse DNS asks the KDC for a ticket for the node name, which
// is not in the service keytab. The raw error for that is opaque, so name it.
func spnegoAdvice(err error, spn string) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "KDC_ERR_S_PRINCIPAL_UNKNOWN"),
		strings.Contains(msg, "Server not found in Kerberos database"):
		return fmt.Errorf("the KDC does not know the service principal %s.\n"+
			"If the endpoint is a DNS alias, your krb5.conf may be canonicalising it to a node name; "+
			"setting 'rdns = false' in the [libdefaults] section usually fixes this.\n"+
			"Underlying error: %w", spn, err)
	case strings.Contains(msg, "KRB_AP_ERR_TKT_EXPIRED"),
		strings.Contains(msg, "expired"):
		return fmt.Errorf("your Kerberos ticket has expired: run 'kinit'.\nUnderlying error: %w", err)
	case strings.Contains(msg, "no credentials"), strings.Contains(msg, "no such file"):
		return fmt.Errorf("no Kerberos ticket found: run 'kinit'.\nUnderlying error: %w", err)
	default:
		return fmt.Errorf("building a Kerberos token for %s: %w", spn, err)
	}
}

// DefaultCCachePath returns the Kerberos credential cache location, honouring
// KRB5CCNAME and its "FILE:" prefix.
func DefaultCCachePath() string {
	if v := os.Getenv("KRB5CCNAME"); v != "" {
		if rest, ok := strings.CutPrefix(v, "FILE:"); ok {
			return rest
		}
		// Other cache types (DIR:, KEYRING:, KCM:) are not files and cannot be
		// read directly. Returning the raw value lets loadTicket produce a
		// clear error naming what it found.
		return v
	}
	return filepath.Join(os.TempDir(), "krb5cc_"+strconv.Itoa(os.Getuid()))
}

// loadTicket reads the principal from a credential cache.
func loadTicket(path string) (*Ticket, error) {
	if path == "" {
		return nil, cberr.Authf("no Kerberos credential cache found")
	}
	if i := strings.Index(path, ":"); i > 1 {
		return nil, cberr.Authf("the Kerberos credential cache %q is not a file cache; "+
			"this client can only read FILE: caches", path)
	}
	if _, err := os.Stat(path); err != nil {
		return nil, cberr.Authf("no Kerberos ticket at %s: run 'kinit'", path)
	}
	cc, err := credentials.LoadCCache(path)
	if err != nil {
		return nil, cberr.Authf("reading the Kerberos credential cache %s: %v", path, err)
	}
	principal := cc.GetClientPrincipalName()
	realm := cc.GetClientRealm()
	full := principal.PrincipalNameString()
	if realm != "" {
		full += "@" + realm
	}
	return &Ticket{Principal: full, Realm: realm, CachePath: path}, nil
}

func drainBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		if _, err := resp.Body.Read(buf); err != nil {
			return
		}
	}
}
