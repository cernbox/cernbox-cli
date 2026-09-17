package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SiteConfigPath is the deployment-wide configuration file. Shipping it with
// the RPM is what makes the lxplus experience zero-configuration: a user runs
// "cernbox ls" on a fresh login and it works, because the endpoint and the SSO
// client are already set here.
const SiteConfigPath = "/etc/cernbox/config.yaml"

// Config is the CLI's configuration, assembled from the site file, the user
// file, the environment, and flags, in increasing order of precedence.
type Config struct {
	// Endpoint is the CERNBox base URL.
	Endpoint string `yaml:"endpoint"`

	Auth     AuthConfig     `yaml:"auth"`
	Transfer TransferConfig `yaml:"transfer"`

	// Insecure disables transport security checks. Only for development
	// instances; the CLI warns on every use.
	Insecure bool `yaml:"insecure"`
}

// AuthConfig configures how credentials are obtained.
type AuthConfig struct {
	// Method pins a single provider. Empty means the whole chain is tried.
	Method string `yaml:"method"`
	// TokenCache overrides where tokens are stored between invocations.
	TokenCache string `yaml:"token_cache"`

	Kerberos KerberosConfig `yaml:"kerberos"`
	SSO      SSOConfig      `yaml:"sso"`
}

// KerberosConfig configures the Kerberos provider.
type KerberosConfig struct {
	// Mode is sso, spnego, or auto.
	Mode string `yaml:"mode"`
	// ServicePrincipal overrides the SPN derived from the endpoint host. It is
	// needed when the endpoint is a DNS alias whose keytab entry differs.
	ServicePrincipal string `yaml:"service_principal"`
	// Path is the endpoint that accepts a SPNEGO token.
	Path string `yaml:"path"`
	// CCache overrides the Kerberos credential cache location.
	CCache string `yaml:"ccache"`
}

// SSOConfig configures the OIDC client used against CERN SSO.
type SSOConfig struct {
	Issuer      string   `yaml:"issuer"`
	ClientID    string   `yaml:"client_id"`
	Audience    string   `yaml:"audience"`
	Scopes      []string `yaml:"scopes"`
	RedirectURI string   `yaml:"redirect_uri"`
	// ServicePrincipal overrides the SPN of the SSO server.
	ServicePrincipal string `yaml:"service_principal"`
}

// TransferConfig configures the transfer engine.
type TransferConfig struct {
	// Jobs is how many files move concurrently.
	Jobs int `yaml:"jobs"`
	// ChunkSize accepts a suffixed size such as "8M".
	ChunkSize string `yaml:"chunk_size"`
	// Verify computes and checks checksums by default.
	Verify bool `yaml:"verify"`
	// Archive allows recursive downloads to use the archiver.
	Archive *bool `yaml:"archive"`
}

// DefaultConfig returns the built-in defaults, which target production
// CERNBox so that the CLI is useful with no configuration at all.
func DefaultConfig() *Config {
	archive := true
	return &Config{
		Endpoint: "https://cernbox.cern.ch",
		Auth: AuthConfig{
			Kerberos: KerberosConfig{
				// Native Kerberos by default: the ticket goes straight to
				// CERNBox, with no identity provider in between and nothing to
				// be unavailable but CERNBox itself.
				Mode: "spnego",
				Path: "/graph/v1.0/me",
			},
			SSO: SSOConfig{
				Issuer:   "https://auth.cern.ch/auth/realms/cern",
				ClientID: "cernbox-cli",
				Audience: "cernbox",
			},
		},
		Transfer: TransferConfig{
			ChunkSize: "8M",
			Archive:   &archive,
		},
	}
}

// UserConfigPath returns the per-user configuration file.
func UserConfigPath() string {
	if p := os.Getenv("CERNBOX_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "cernbox", "config.yaml")
}

// LoadConfig assembles the configuration. A missing file at any level is not an
// error; a malformed one is, because silently ignoring it would leave the user
// wondering why their setting had no effect.
func LoadConfig(explicitPath string) (*Config, error) {
	cfg := DefaultConfig()

	paths := []string{SiteConfigPath, UserConfigPath()}
	if explicitPath != "" {
		paths = []string{explicitPath}
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := mergeFile(cfg, p, explicitPath != ""); err != nil {
			return nil, err
		}
	}

	applyEnv(cfg)
	return cfg, nil
}

func mergeFile(cfg *Config, path string, required bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return nil
		}
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	return nil
}

// applyEnv overlays CERNBOX_* variables, which sit between the config files and
// the command line.
func applyEnv(cfg *Config) {
	if v := os.Getenv("CERNBOX_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("CERNBOX_AUTH_METHOD"); v != "" {
		cfg.Auth.Method = v
	}
	if v := os.Getenv("CERNBOX_KERBEROS_MODE"); v != "" {
		cfg.Auth.Kerberos.Mode = v
	}
	if v := os.Getenv("CERNBOX_TOKEN_CACHE"); v != "" {
		cfg.Auth.TokenCache = v
	}
	if v := os.Getenv("CERNBOX_SSO_ISSUER"); v != "" {
		cfg.Auth.SSO.Issuer = v
	}
	if v := os.Getenv("CERNBOX_SSO_CLIENT_ID"); v != "" {
		cfg.Auth.SSO.ClientID = v
	}
	if v := os.Getenv("CERNBOX_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Transfer.Jobs = n
		}
	}
}

// ParseSize converts a suffixed size such as "8M" or "512K" to bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		mult, s = 1<<10, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1<<20, s[:len(s)-1]
	case 'g', 'G':
		mult, s = 1<<30, s[:len(s)-1]
	case 'b', 'B':
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: want a number, optionally suffixed with K, M, or G", s)
	}
	return n * mult, nil
}

// ArchiveEnabled reports whether recursive downloads may use the archiver,
// defaulting to true when unset.
func (c *TransferConfig) ArchiveEnabled() bool {
	return c.Archive == nil || *c.Archive
}
