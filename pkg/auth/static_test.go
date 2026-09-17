package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodeBasic(t *testing.T, value string) (user, secret string) {
	t.Helper()
	raw, ok := strings.CutPrefix(value, "Basic ")
	if !ok {
		t.Fatalf("value %q is not a Basic credential", value)
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("value is not base64: %v", err)
	}
	user, secret, _ = strings.Cut(string(decoded), ":")
	return user, secret
}

func TestTokenProvider(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit value", func(t *testing.T) {
		p := &TokenProvider{Value: "abc"}
		if !p.Available(ctx) {
			t.Fatal("should be available")
		}
		tok, err := p.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if tok.Value != "Bearer abc" {
			t.Errorf("value = %q", tok.Value)
		}
		// An opaque token carries no expiry claim we can trust, so none is
		// asserted and it is used until the server rejects it.
		if !tok.Expiry.IsZero() {
			t.Errorf("expiry = %v, want zero for an opaque token", tok.Expiry)
		}
	})

	t.Run("from environment", func(t *testing.T) {
		t.Setenv("CERNBOX_TOKEN", "from-env")
		p := &TokenProvider{}
		if !p.Available(ctx) {
			t.Fatal("should be available from the environment")
		}
		tok, _ := p.Token(ctx)
		if tok.Value != "Bearer from-env" {
			t.Errorf("value = %q", tok.Value)
		}
	})

	t.Run("flag beats environment", func(t *testing.T) {
		t.Setenv("CERNBOX_TOKEN", "from-env")
		tok, _ := (&TokenProvider{Value: "from-flag"}).Token(ctx)
		if tok.Value != "Bearer from-flag" {
			t.Errorf("value = %q, want the explicit value to win", tok.Value)
		}
	})

	t.Run("unavailable when unset", func(t *testing.T) {
		t.Setenv("CERNBOX_TOKEN", "")
		if (&TokenProvider{}).Available(ctx) {
			t.Error("should be unavailable with nothing configured")
		}
	})
}

func TestAppTokenProvider(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit", func(t *testing.T) {
		p := &AppTokenProvider{Username: "einstein", Secret: "app-password"}
		if !p.Available(ctx) {
			t.Fatal("should be available")
		}
		tok, err := p.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		user, secret := decodeBasic(t, tok.Value)
		if user != "einstein" || secret != "app-password" {
			t.Errorf("credential = %q:%q", user, secret)
		}
		if tok.Subject != "einstein" {
			t.Errorf("subject = %q", tok.Subject)
		}
		if p.IdentityHint() != "einstein" {
			t.Errorf("identity hint = %q", p.IdentityHint())
		}
	})

	t.Run("from environment", func(t *testing.T) {
		t.Setenv("CERNBOX_USER", "marie")
		t.Setenv("CERNBOX_APP_TOKEN", "env-password")
		p := &AppTokenProvider{}
		tok, err := p.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		user, secret := decodeBasic(t, tok.Value)
		if user != "marie" || secret != "env-password" {
			t.Errorf("credential = %q:%q", user, secret)
		}
	})

	// A token in a file stays out of /proc and out of process listings, which
	// is why the file form exists alongside the environment variable.
	t.Run("from file", func(t *testing.T) {
		t.Setenv("CERNBOX_APP_TOKEN", "")
		path := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(path, []byte("  file-password\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		p := &AppTokenProvider{Username: "einstein", TokenFile: path}
		tok, err := p.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, secret := decodeBasic(t, tok.Value)
		if secret != "file-password" {
			t.Errorf("secret = %q, want the surrounding whitespace trimmed", secret)
		}
	})

	t.Run("missing username is explained", func(t *testing.T) {
		t.Setenv("CERNBOX_USER", "")
		_, err := (&AppTokenProvider{Secret: "x"}).Token(ctx)
		if err == nil || !strings.Contains(err.Error(), "CERNBOX_USER") {
			t.Errorf("got %v, want it to name the variable to set", err)
		}
	})

	t.Run("missing token is explained", func(t *testing.T) {
		t.Setenv("CERNBOX_APP_TOKEN", "")
		_, err := (&AppTokenProvider{Username: "einstein"}).Token(ctx)
		if err == nil || !strings.Contains(err.Error(), "CERNBOX_APP_TOKEN") {
			t.Errorf("got %v, want it to name the variable to set", err)
		}
	})

	t.Run("unavailable with only half the credential", func(t *testing.T) {
		t.Setenv("CERNBOX_USER", "")
		t.Setenv("CERNBOX_APP_TOKEN", "")
		if (&AppTokenProvider{Username: "einstein"}).Available(ctx) {
			t.Error("a username without a token is not a usable credential")
		}
		if (&AppTokenProvider{Secret: "x"}).Available(ctx) {
			t.Error("a token without a username is not a usable credential")
		}
	})
}

func TestBasicProvider(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit", func(t *testing.T) {
		p := &BasicProvider{Username: "einstein", Password: "relativity"}
		tok, err := p.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		user, secret := decodeBasic(t, tok.Value)
		if user != "einstein" || secret != "relativity" {
			t.Errorf("credential = %q:%q", user, secret)
		}
	})

	t.Run("prompts when no password is configured", func(t *testing.T) {
		t.Setenv("CERNBOX_PASSWORD", "")
		prompted := false
		p := &BasicProvider{
			Username: "einstein",
			Prompt: func(user string) (string, error) {
				prompted = true
				if user != "einstein" {
					t.Errorf("prompt got user %q", user)
				}
				return "typed-password", nil
			},
		}
		tok, err := p.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !prompted {
			t.Error("the prompt was not used")
		}
		_, secret := decodeBasic(t, tok.Value)
		if secret != "typed-password" {
			t.Errorf("secret = %q", secret)
		}
	})

	// Without a prompt and without a configured password there is no way to
	// proceed, and hanging would be worse than failing.
	t.Run("unavailable without a password or a prompt", func(t *testing.T) {
		t.Setenv("CERNBOX_PASSWORD", "")
		if (&BasicProvider{Username: "einstein"}).Available(ctx) {
			t.Error("should be unavailable with no way to obtain a password")
		}
	})

	t.Run("unavailable without a username", func(t *testing.T) {
		t.Setenv("CERNBOX_USERNAME", "")
		if (&BasicProvider{Password: "x"}).Available(ctx) {
			t.Error("should be unavailable with no username")
		}
	})

	t.Run("prompt failure is reported", func(t *testing.T) {
		t.Setenv("CERNBOX_PASSWORD", "")
		p := &BasicProvider{
			Username: "einstein",
			Prompt:   func(string) (string, error) { return "", errors.New("not a terminal") },
		}
		if _, err := p.Token(ctx); err == nil {
			t.Error("expected the prompt failure to surface")
		}
	})
}

func TestProviderNames(t *testing.T) {
	names := map[string]Provider{
		MethodToken:    &TokenProvider{},
		MethodAppToken: &AppTokenProvider{},
		MethodBasic:    &BasicProvider{},
		MethodKerberos: &KerberosProvider{},
		MethodDevice:   &DeviceProvider{},
	}
	for want, p := range names {
		if got := p.Name(); got != want {
			t.Errorf("%T.Name() = %q, want %q", p, got, want)
		}
	}
}

func TestJWTExpiry(t *testing.T) {
	// {"exp":1767225600} — a real JWT payload, unsigned parts elided.
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1767225600}`))
	token := "header." + payload + ".signature"

	got := jwtExpiry(token)
	if got.Unix() != 1767225600 {
		t.Errorf("jwtExpiry = %v, want the exp claim", got)
	}
	if got := jwtExpiry("Bearer " + token); got.Unix() != 1767225600 {
		t.Errorf("jwtExpiry should tolerate the Bearer prefix, got %v", got)
	}

	// An unparseable token reads as non-expiring, which is the safe direction:
	// it gets used until the server says otherwise, and the 401 path
	// re-authenticates.
	for _, bad := range []string{"", "not-a-jwt", "a.b", "a.!!!.c", "a." + base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".c"} {
		if got := jwtExpiry(bad); !got.IsZero() {
			t.Errorf("jwtExpiry(%q) = %v, want zero", bad, got)
		}
	}
}
