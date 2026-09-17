package auth

import (
	"context"
	"encoding/base64"
	"os"
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// Provider names, also accepted by --method.
const (
	MethodToken    = "token"
	MethodAppToken = "app-token"
	MethodBasic    = "basic"
	MethodKerberos = "kerberos"
	MethodDevice   = "device"
)

// TokenProvider presents a token supplied directly by the caller, through
// --token or $CERNBOX_TOKEN. It is the escape hatch for CI and for debugging.
type TokenProvider struct {
	// Value is the raw token. When empty, $CERNBOX_TOKEN is consulted.
	Value string
}

// Name implements Provider.
func (p *TokenProvider) Name() string { return MethodToken }

// Available implements Provider.
func (p *TokenProvider) Available(context.Context) bool { return p.token() != "" }

// Token implements Provider.
func (p *TokenProvider) Token(context.Context) (*Token, error) {
	tok := p.token()
	if tok == "" {
		return nil, cberr.Authf("no token supplied")
	}
	return &Token{
		Header:   "Authorization",
		Value:    "Bearer " + tok,
		Provider: MethodToken,
		// No expiry is asserted: the caller supplied an opaque string and we
		// have no basis to claim when it stops working.
	}, nil
}

func (p *TokenProvider) token() string {
	if p.Value != "" {
		return p.Value
	}
	return os.Getenv("CERNBOX_TOKEN")
}

// AppTokenProvider presents a CERNBox app password. This is the credential for
// batch jobs and cron, where no Kerberos ticket is forwarded.
//
// App tokens can be created with a path and permission scope, and the CLI
// documents the scoped form as the default, so an automated job carries a
// credential limited to what it actually needs rather than full account access.
type AppTokenProvider struct {
	// Username is the account the token belongs to. When empty, $CERNBOX_USER
	// is consulted.
	Username string
	// Secret is the app password. When empty, $CERNBOX_APP_TOKEN is consulted,
	// then the file named by TokenFile.
	Secret string
	// TokenFile is read when Token is empty. Keeping the secret in a file
	// rather than an environment variable keeps it out of /proc and out of
	// process listings.
	TokenFile string
}

// Name implements Provider.
func (p *AppTokenProvider) Name() string { return MethodAppToken }

// Available implements Provider.
func (p *AppTokenProvider) Available(context.Context) bool {
	user, token := p.credentials()
	return user != "" && token != ""
}

// Token implements Provider.
func (p *AppTokenProvider) Token(context.Context) (*Token, error) {
	user, token := p.credentials()
	if token == "" {
		return nil, cberr.Authf("no app token supplied: set CERNBOX_APP_TOKEN or pass --app-token-file")
	}
	if user == "" {
		return nil, cberr.Authf("an app token needs a username: set CERNBOX_USER or pass --user")
	}
	return &Token{
		Header:   "Authorization",
		Value:    "Basic " + basicValue(user, token),
		Provider: MethodAppToken,
		Subject:  user,
	}, nil
}

// IdentityHint implements IdentityHinter.
func (p *AppTokenProvider) IdentityHint() string {
	user, _ := p.credentials()
	return user
}

func (p *AppTokenProvider) credentials() (user, token string) {
	user = p.Username
	if user == "" {
		user = os.Getenv("CERNBOX_USER")
	}
	token = p.Secret
	if token == "" {
		token = os.Getenv("CERNBOX_APP_TOKEN")
	}
	if token == "" && p.TokenFile != "" {
		if b, err := os.ReadFile(p.TokenFile); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	return user, token
}

// BasicProvider presents a username and password. It exists for development
// instances, which have no Kerberos and no SSO, and is never reached
// automatically: the chain only includes it when --method basic is given.
type BasicProvider struct {
	Username string
	Password string
	// Prompt is called when no password is configured. The CLI wires it to a
	// terminal prompt; it is nil in non-interactive contexts, which makes the
	// provider unavailable rather than hanging.
	Prompt func(user string) (string, error)
}

// Name implements Provider.
func (p *BasicProvider) Name() string { return MethodBasic }

// Available implements Provider.
func (p *BasicProvider) Available(context.Context) bool {
	if p.Username == "" && os.Getenv("CERNBOX_USERNAME") == "" {
		return false
	}
	return p.Password != "" || os.Getenv("CERNBOX_PASSWORD") != "" || p.Prompt != nil
}

// Token implements Provider.
func (p *BasicProvider) Token(context.Context) (*Token, error) {
	user := p.Username
	if user == "" {
		user = os.Getenv("CERNBOX_USERNAME")
	}
	if user == "" {
		return nil, cberr.Authf("no username for basic authentication")
	}

	password := p.Password
	if password == "" {
		password = os.Getenv("CERNBOX_PASSWORD")
	}
	if password == "" {
		if p.Prompt == nil {
			return nil, cberr.Authf("no password for basic authentication")
		}
		var err error
		password, err = p.Prompt(user)
		if err != nil {
			return nil, cberr.Authf("reading password: %v", err)
		}
	}

	return &Token{
		Header:   "Authorization",
		Value:    "Basic " + basicValue(user, password),
		Provider: MethodBasic,
		Subject:  user,
	}, nil
}

// IdentityHint implements IdentityHinter.
func (p *BasicProvider) IdentityHint() string {
	if p.Username != "" {
		return p.Username
	}
	return os.Getenv("CERNBOX_USERNAME")
}

func basicValue(user, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
}
