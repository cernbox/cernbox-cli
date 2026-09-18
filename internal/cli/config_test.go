package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
