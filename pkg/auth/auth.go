// Package auth resolves the credential the CLI presents to CERNBox.
//
// The design goal is that a user logged into lxplus runs "cernbox ls" and it
// works: no login step, no configuration, no secret on disk. That is what the
// Kerberos providers deliver. Everything else in this package exists so the
// same binary still works when Kerberos is not available — on a laptop, in a
// batch job, for an account that has no Kerberos principal at all.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
)

// refreshWindow is how long before expiry a token is treated as already
// expired. It covers clock skew and the round trip of the request the token is
// about to be used for.
const refreshWindow = 60 * time.Second

// Token is a credential together with what is needed to manage its lifetime.
type Token struct {
	// Header is the HTTP header name to send it in.
	Header string `json:"header"`
	// Value is the complete header value, for example "Bearer eyJ...".
	Value string `json:"value"`
	// Expiry is when the token stops being valid. A zero value means unknown,
	// which is treated as "does not expire" — an app password, for instance.
	Expiry time.Time `json:"expiry,omitempty"`
	// RefreshToken renews Value without re-authenticating, when the issuer
	// supplied one.
	RefreshToken string `json:"refresh_token,omitempty"`
	// Provider names the provider that produced this token, for "cernbox
	// status" and for deciding how to refresh it.
	Provider string `json:"provider"`
	// Subject is the identity the token represents, used for display and as
	// part of the cache key.
	Subject string `json:"subject,omitempty"`
}

// Valid reports whether the token can still be used, leaving a margin for the
// request it is about to authenticate.
func (t *Token) Valid() bool {
	if t == nil || t.Value == "" {
		return false
	}
	if t.Expiry.IsZero() {
		return true
	}
	return time.Now().Add(refreshWindow).Before(t.Expiry)
}

// Credential converts the token into what the HTTP client sends.
func (t *Token) Credential() client.Credential {
	return client.Credential{Header: t.Header, Value: t.Value}
}

// Provider obtains a token by one specific method.
type Provider interface {
	// Name identifies the provider in "cernbox status" and in --method.
	Name() string
	// Available reports whether this provider has what it needs to be tried.
	// It must be cheap and must not touch the network: the chain calls it on
	// every provider to decide what to attempt.
	Available(ctx context.Context) bool
	// Token authenticates and returns a token.
	Token(ctx context.Context) (*Token, error)
}

// Refresher is implemented by providers that can renew a token without
// re-authenticating from scratch.
type Refresher interface {
	Refresh(ctx context.Context, tok *Token) (*Token, error)
}

// IdentityHinter is implemented by providers that can name the identity they
// would authenticate as without actually authenticating. The chain uses it to
// key the token cache, so that a user who runs kinit as a different principal
// gets a different cache entry instead of silently reusing the previous
// identity's token.
type IdentityHinter interface {
	IdentityHint() string
}

// Chain resolves a credential by trying providers in order. It is safe for
// concurrent use, which matters because the transfer engine asks for a
// credential from many goroutines at once.
type Chain struct {
	providers []Provider
	cache     *Cache
	endpoint  string

	mu      sync.Mutex
	current *Token
	// forced restricts the chain to a single provider, set by --method.
	forced string
}

// ChainOption configures a Chain.
type ChainOption func(*Chain)

// WithCache attaches a token cache. Without one, every invocation
// re-authenticates, which is correct but slow in a shell loop.
func WithCache(c *Cache) ChainOption { return func(ch *Chain) { ch.cache = c } }

// WithMethod restricts the chain to the named provider. An unknown name is
// reported when the chain is first used rather than at construction, so that
// "--method" errors come out through the normal error path.
func WithMethod(name string) ChainOption {
	return func(ch *Chain) { ch.forced = strings.ToLower(strings.TrimSpace(name)) }
}

// NewChain builds a chain for the given endpoint.
func NewChain(endpoint string, providers []Provider, opts ...ChainOption) *Chain {
	ch := &Chain{endpoint: endpoint, providers: providers}
	for _, o := range opts {
		o(ch)
	}
	return ch
}

// Providers returns the configured providers, in order.
func (ch *Chain) Providers() []Provider { return ch.providers }

// Credential implements client.CredentialSource.
func (ch *Chain) Credential(ctx context.Context) (client.Credential, error) {
	tok, err := ch.Token(ctx)
	if err != nil {
		return client.Credential{}, err
	}
	return tok.Credential(), nil
}

// Token returns a usable token, obtaining or refreshing one if needed.
func (ch *Chain) Token(ctx context.Context) (*Token, error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if ch.current.Valid() {
		return ch.current, nil
	}

	candidates, err := ch.candidates()
	if err != nil {
		return nil, err
	}

	// A cached token from a previous invocation avoids re-authenticating on
	// every command in a shell loop.
	if tok := ch.fromCache(ctx, candidates); tok != nil {
		ch.current = tok
		return tok, nil
	}

	// An expired token with a refresh token is cheaper to renew than to
	// replace, and on lxplus it avoids a round trip to the SSO server.
	if refreshed := ch.refresh(ctx, candidates); refreshed != nil {
		ch.store(refreshed)
		return refreshed, nil
	}

	var attempted []string
	var firstErr error
	for _, p := range candidates {
		if !p.Available(ctx) {
			continue
		}
		attempted = append(attempted, p.Name())
		tok, err := p.Token(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", p.Name(), err)
			}
			continue
		}
		tok.Provider = p.Name()
		ch.store(tok)
		return tok, nil
	}

	if firstErr != nil {
		// At least one provider was available and failed. That error is far
		// more useful than a generic "no credentials", because it names the
		// thing that went wrong.
		return nil, cberr.Authf("%v", firstErr)
	}
	return nil, ch.noCredentialsError(attempted)
}

// candidates returns the providers to try, honouring --method.
func (ch *Chain) candidates() ([]Provider, error) {
	if ch.forced == "" {
		return ch.providers, nil
	}
	for _, p := range ch.providers {
		if p.Name() == ch.forced {
			return []Provider{p}, nil
		}
	}
	names := make([]string, 0, len(ch.providers))
	for _, p := range ch.providers {
		names = append(names, p.Name())
	}
	return nil, cberr.Usagef("unknown sign-in method %q: use one of %s",
		ch.forced, strings.Join(names, ", "))
}

func (ch *Chain) fromCache(ctx context.Context, candidates []Provider) *Token {
	if ch.cache == nil {
		return nil
	}
	tok, ok := ch.cache.Get(ch.cacheKey(candidates))
	if !ok || !tok.Valid() {
		return nil
	}
	// A cached token from a provider that --method excluded must not be used,
	// or "--method device" would silently reuse a Kerberos session.
	if ch.forced != "" && tok.Provider != ch.forced {
		return nil
	}
	return tok
}

// refresh tries to renew an expired cached token rather than authenticating
// again.
func (ch *Chain) refresh(ctx context.Context, candidates []Provider) *Token {
	if ch.cache == nil {
		return nil
	}
	tok, ok := ch.cache.Get(ch.cacheKey(candidates))
	if !ok || tok.RefreshToken == "" {
		return nil
	}
	for _, p := range candidates {
		if p.Name() != tok.Provider {
			continue
		}
		r, ok := p.(Refresher)
		if !ok {
			return nil
		}
		refreshed, err := r.Refresh(ctx, tok)
		if err != nil {
			// A refresh token that the issuer has expired is not an error
			// worth reporting: fall through and authenticate normally, which
			// on lxplus is silent anyway.
			return nil
		}
		refreshed.Provider = p.Name()
		return refreshed
	}
	return nil
}

func (ch *Chain) store(tok *Token) {
	ch.current = tok
	if ch.cache != nil {
		// A cache write failure must not fail the command: the token in hand
		// is still good, the next invocation will just re-authenticate.
		_ = ch.cache.Put(ch.cacheKey(ch.providers), tok)
	}
}

// cacheKey identifies the cache entry for this endpoint and identity.
//
// The identity component is what stops a user with both a personal and a
// service principal from picking up the wrong session after kinit.
func (ch *Chain) cacheKey(candidates []Provider) string {
	hint := ""
	for _, p := range candidates {
		h, ok := p.(IdentityHinter)
		if !ok {
			continue
		}
		if v := h.IdentityHint(); v != "" {
			hint = v
			break
		}
	}
	return ch.endpoint + "\x00" + hint
}

func (ch *Chain) noCredentialsError(attempted []string) error {
	var advice strings.Builder
	advice.WriteString("no usable credentials found")
	if len(attempted) > 0 {
		advice.WriteString(" (tried: " + strings.Join(attempted, ", ") + ")")
	}
	advice.WriteString("\n")
	advice.WriteString("Run 'cernbox login' to sign in, or 'kinit' if you use Kerberos.")
	return cberr.Authf("%s", advice.String())
}

// Forget clears the in-memory and cached tokens for this endpoint.
func (ch *Chain) Forget() error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.current = nil
	if ch.cache == nil {
		return nil
	}
	return ch.cache.DeletePrefix(ch.endpoint + "\x00")
}

// ErrNoProvider is returned when a chain is built with no providers at all.
var ErrNoProvider = errors.New("no authentication providers configured")
