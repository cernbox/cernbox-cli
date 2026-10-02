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
// client are already set here. On Windows it is %ProgramData%\cernbox\config.yaml.
var SiteConfigPath = siteConfigPath()

// Config is the CLI's configuration, assembled from the site file, the user
// file, the environment, and flags, in increasing order of precedence.
type Config struct {
	// Endpoint is the CERNBox base URL.
	Endpoint string `yaml:"endpoint"`

	Auth     AuthConfig     `yaml:"auth"`
	Transfer TransferConfig `yaml:"transfer"`
	Edit     EditConfig     `yaml:"edit"`

	// Outbox lists local folders whose contents are uploaded to CERNBox, so that
	// "cernbox outbox push" needs no arguments.
	Outbox []OutboxFolder `yaml:"outbox"`

	// Inbox lists CERNBox folders whose contents are downloaded locally, so that
	// "cernbox inbox pull" needs no arguments.
	Inbox []InboxFolder `yaml:"inbox"`

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
	Issuer   string   `yaml:"issuer"`
	ClientID string   `yaml:"client_id"`
	Scopes   []string `yaml:"scopes"`
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

// EditConfig configures the editor command.
type EditConfig struct {
	// Folder is where a bare file name goes, relative to the home space unless
	// it is absolute. The point of it is that "cernbox edit notes.txt" always
	// means the same file, whatever directory you happen to be in.
	Folder string `yaml:"folder"`
	// Command overrides VISUAL and EDITOR for this CLI only, which is useful
	// when the editor you want for a remote file is not the one you want for a
	// commit message.
	Command string `yaml:"command"`
}

// OutboxFolder is one local folder that is uploaded to CERNBox.
type OutboxFolder struct {
	// Local is the folder to watch. A leading ~ is expanded.
	Local string `yaml:"local"`
	// Remote is the CERNBox folder its contents go to.
	Remote string `yaml:"remote"`
	// Layout is "flat", or "date" to file each upload under YYYY/MM/DD taken from
	// the file's own timestamp.
	Layout string `yaml:"layout"`
	// After is what happens to the local file once it is safely up: "keep",
	// "move" to an .uploaded folder, or "delete". Empty means keep, because that
	// is the only choice that cannot lose anything.
	After string `yaml:"after"`
	// Link creates a public link for each upload and prints it.
	Link bool `yaml:"link"`
	// Exec is a command run over each file before it is uploaded, with the
	// file's path as its last argument. It may rewrite the file where it is; a
	// file it renames is not followed. Split on spaces and never handed to a
	// shell.
	Exec string `yaml:"exec"`
}

// InboxFolder is one CERNBox folder whose arrivals are downloaded locally. It
// is the outbox the other way round, and deliberately the same shape.
type InboxFolder struct {
	// Remote is the CERNBox folder to collect from.
	Remote string `yaml:"remote"`
	// Local is where its contents go. A leading ~ is expanded.
	Local string `yaml:"local"`
	// Layout is "flat", or "date" to file each arrival under YYYY/MM/DD taken
	// from the file's own timestamp.
	Layout string `yaml:"layout"`
	// After is what happens to the CERNBox copy once it is safely down: "keep",
	// "move" to a .collected folder, or "delete". Empty means keep, because that
	// is the only choice that cannot lose anything.
	After string `yaml:"after"`
	// Exec is a command run for each file collected, with the file's local path
	// as its last argument. It is split on spaces and never handed to a shell.
	Exec string `yaml:"exec"`
}

// DefaultConfig returns the built-in defaults, which target production
// CERNBox so that the CLI is useful with no configuration at all.
func DefaultConfig() *Config {
	archive := true
	return &Config{
		Endpoint: "https://cernbox.cern.ch",
		Auth: AuthConfig{
			Kerberos: KerberosConfig{
				// The ticket goes straight to CERNBox, which verifies it
				// against its own keytab: no identity provider in between, and
				// nothing to be unavailable but CERNBox itself.
				Path: "/graph/v1.0/me",
			},
			SSO: SSOConfig{
				Issuer:   "https://auth.cern.ch/auth/realms/cern",
				ClientID: "cernbox-cli",
			},
		},
		Transfer: TransferConfig{
			ChunkSize: "8M",
			Archive:   &archive,
		},
		Edit: EditConfig{
			Folder: DefaultEditFolder,
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
	if v := os.Getenv("CERNBOX_EDIT_FOLDER"); v != "" {
		cfg.Edit.Folder = v
	}
	// Deliberately not EDITOR: this overrides it, so it needs its own name, or
	// setting it would change every other tool on the machine too.
	if v := os.Getenv("CERNBOX_EDITOR"); v != "" {
		cfg.Edit.Command = v
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
