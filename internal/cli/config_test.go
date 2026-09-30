package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultConfigTargetsProduction(t *testing.T) {
	cfg := DefaultConfig()

	// The defaults are what make the CLI useful with no configuration at all,
	// which is the whole lxplus story.
	if cfg.Endpoint != "https://cernbox.cern.ch" {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
	// Native Kerberos: the ticket goes straight to CERNBox, so a login depends
	// on nothing but CERNBox being reachable.
	if cfg.Auth.Kerberos.Path == "" {
		t.Error("no path configured for the Kerberos token exchange")
	}
	if cfg.Auth.SSO.Issuer == "" || cfg.Auth.SSO.ClientID == "" {
		t.Errorf("SSO defaults are incomplete: %+v", cfg.Auth.SSO)
	}
	if !cfg.Transfer.ArchiveEnabled() {
		t.Error("recursive downloads should use the archiver by default")
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "config.yaml", `
endpoint: https://cernbox-test.cern.ch
auth:
  method: device
  kerberos:
    service_principal: HTTP/real.cern.ch
  sso:
    client_id: my-client
transfer:
  jobs: 12
  chunk_size: 32M
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://cernbox-test.cern.ch" {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
	if cfg.Auth.Method != "device" {
		t.Errorf("Method = %q", cfg.Auth.Method)
	}
	if cfg.Auth.Kerberos.ServicePrincipal != "HTTP/real.cern.ch" {
		t.Errorf("SPN = %q", cfg.Auth.Kerberos.ServicePrincipal)
	}
	if cfg.Transfer.Jobs != 12 {
		t.Errorf("Jobs = %d", cfg.Transfer.Jobs)
	}

	// Values the file does not mention keep their defaults rather than being
	// zeroed, so a user who sets one thing does not lose everything else.
	if cfg.Auth.SSO.Issuer == "" {
		t.Error("the SSO issuer default was lost when the file overrode client_id")
	}
}

func TestLoadConfigRejectsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "config.yaml", "endpoint: [this is not a string")

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("a malformed config should be an error, not silently ignored")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error should name the file, got %q", err)
	}
}

func TestLoadConfigMissingExplicitFileIsAnError(t *testing.T) {
	// A missing default file is fine; a missing file the user named is not,
	// because they would otherwise wonder why their settings had no effect.
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("an explicitly named missing config should be an error")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "config.yaml", "endpoint: https://from-file.cern.ch\n")

	t.Setenv("CERNBOX_ENDPOINT", "https://from-env.cern.ch")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://from-env.cern.ch" {
		t.Errorf("Endpoint = %q, want the environment to win over the file", cfg.Endpoint)
	}
}

func TestUserConfigPathOverride(t *testing.T) {
	t.Setenv("CERNBOX_CONFIG", "/somewhere/config.yaml")
	if got := UserConfigPath(); got != "/somewhere/config.yaml" {
		t.Errorf("UserConfigPath() = %q", got)
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"1024", 1024},
		{"1K", 1024},
		{"1k", 1024},
		{"8M", 8 << 20},
		{"2G", 2 << 30},
		{"512B", 512},
		{" 16M ", 16 << 20},
	}
	for _, tt := range tests {
		got, err := ParseSize(tt.in)
		if err != nil {
			t.Errorf("ParseSize(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}

	for _, bad := range []string{"big", "8X", "1.5M", "--"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should fail", bad)
		}
	}
}

func TestArchiveEnabledDefaultsTrue(t *testing.T) {
	var cfg TransferConfig
	if !cfg.ArchiveEnabled() {
		t.Error("an unset archive setting should default to enabled")
	}

	off := false
	cfg.Archive = &off
	if cfg.ArchiveEnabled() {
		t.Error("an explicit false should disable the archiver")
	}
}

// TestDocumentedConfigMatchesTheCode reads the example out of
// docs/configuration.md and holds it against the struct it is meant to describe.
//
// A configuration reference that has drifted is worse than none: it sends people
// to set a key that does nothing. This catches drift in both directions, and both
// checks are needed.
//
// Decoding strictly, into a zero value, catches a key the documentation still
// shows after it was removed or renamed in the code — and, because nothing is
// pre-filled, a field left zero means the documented key no longer maps to it. An
// earlier version of this test compared against LoadConfig, which starts from the
// defaults; every field with a default looked populated whether its key mapped or
// not, so renaming a tag passed.
func TestDocumentedConfigMatchesTheCode(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}

	example := firstYAMLBlock(string(doc))
	if example == "" {
		t.Fatal("docs/configuration.md has no yaml example to check")
	}

	var documented Config
	dec := yaml.NewDecoder(strings.NewReader(example))
	dec.KnownFields(true)
	if err := dec.Decode(&documented); err != nil {
		t.Fatalf("the documented example does not match the configuration struct: %v", err)
	}

	// Every one of these is set in the example, so a zero here means the key it is
	// written under no longer reaches the field.
	checks := map[string]bool{
		"endpoint":            documented.Endpoint != "",
		"auth.kerberos.path":  documented.Auth.Kerberos.Path != "",
		"auth.sso.issuer":     documented.Auth.SSO.Issuer != "",
		"auth.sso.client_id":  documented.Auth.SSO.ClientID != "",
		"transfer.chunk_size": documented.Transfer.ChunkSize != "",
		"transfer.archive":    documented.Transfer.Archive != nil,
		"edit.folder":         documented.Edit.Folder != "",
		"outbox":              len(documented.Outbox) > 0,
	}
	for key, ok := range checks {
		if !ok {
			t.Errorf("the documented %q does not reach the field it describes", key)
		}
	}

	if len(documented.Outbox) > 0 {
		first := documented.Outbox[0]
		if first.Local == "" || first.Remote == "" || first.Layout == "" || first.After == "" {
			t.Errorf("a documented outbox folder does not load fully: %+v", first)
		}
	}

	// And the whole thing is something LoadConfig will take, which is what a user
	// copying it out of the documentation will do.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CERNBOX_CONFIG", "")
	if _, err := LoadConfig(path); err != nil {
		t.Errorf("the documented example does not load: %v", err)
	}
}

// firstYAMLBlock returns the contents of the first fenced yaml block.
func firstYAMLBlock(doc string) string {
	_, rest, ok := strings.Cut(doc, "```yaml\n")
	if !ok {
		return ""
	}
	body, _, ok := strings.Cut(rest, "```")
	if !ok {
		return ""
	}
	return body
}
