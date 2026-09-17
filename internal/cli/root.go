// Package cli implements the cernbox command tree.
package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/auth"
	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/cernbox/cernbox-cli/pkg/pathspec"
	"github.com/cernbox/cernbox-cli/pkg/transfer"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Version information, stamped in at build time by cmd/cernbox.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// globalFlags holds the flags every command accepts.
type globalFlags struct {
	endpoint     string
	configPath   string
	outputFormat string
	quiet        bool
	debug        bool
	stream       bool

	token        string
	method       string
	user         string
	appTokenFile string
	kerberosMode string
	kerberosSPN  string

	insecure   bool
	skipVerify bool
	timeout    time.Duration
}

// App carries everything a command needs, built once in PersistentPreRunE.
type App struct {
	cfg   *Config
	flags *globalFlags
	out   *output.Writer

	// stdout and stderr are fields rather than direct references to os.Stdout
	// and os.Stderr so that tests can drive the whole command tree and read
	// back exactly what a user would see.
	stdout io.Writer
	stderr io.Writer

	client *client.Client
	chain  *auth.Chain
}

// Client returns the HTTP client, which is built lazily so that commands which
// need no server (version, completion) never touch the network or the
// configuration.
func (a *App) Client() *client.Client { return a.client }

// Out returns the output writer.
func (a *App) Out() *output.Writer { return a.out }

// Chain returns the credential chain.
func (a *App) Chain() *auth.Chain { return a.chain }

// Execute builds and runs the command tree.
func Execute() int {
	app := &App{flags: &globalFlags{}, stdout: os.Stdout, stderr: os.Stderr}
	root := newRootCmd(app)

	// Errors are printed here rather than by cobra, so that the exit code and
	// the message come from the same place and --debug can add detail.
	root.SilenceErrors = true
	root.SilenceUsage = true

	err := root.Execute()
	if err == nil {
		return cberr.ExitOK
	}

	printError(app, err)
	return cberr.ExitCode(err)
}

func printError(app *App, err error) {
	w := io.Writer(os.Stderr)
	if app != nil && app.stderr != nil {
		w = app.stderr
	}
	fmt.Fprintf(w, "cernbox: %v\n", err)
	if app.flags != nil && app.flags.debug {
		var ce *cberr.Error
		if ok := asCberr(err, &ce); ok {
			fmt.Fprintf(w, "  kind=%s status=%d op=%q path=%q\n", ce.Kind, ce.Status, ce.Op, ce.Path)
			if ce.Err != nil {
				fmt.Fprintf(w, "  cause: %v\n", ce.Err)
			}
		}
	}
}

func newRootCmd(app *App) *cobra.Command {
	f := app.flags

	cmd := &cobra.Command{
		Use:   "cernbox",
		Short: "Command-line client for CERNBox",
		Long: "Command-line client for CERNBox.\n\n" +
			"On lxplus there is nothing to configure and nothing to log into: the CLI\n" +
			"picks up your Kerberos ticket, exactly like the eos command.\n\n" +
			"Commands that only touch CERNBox take bare paths (/eos/user/g/gdelmont).\n" +
			"Commands that move data between local and remote need the remote side\n" +
			"marked with cb:, because on lxplus /eos is also a local mount.",
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return app.setup(cmd)
		},
	}

	pf := cmd.PersistentFlags()
	pf.StringVar(&f.endpoint, "endpoint", "", "CERNBox base URL")
	pf.StringVar(&f.configPath, "config", "", "configuration file to use instead of the defaults")
	pf.StringVarP(&f.outputFormat, "output", "o", "table", "output format: table, json, or csv")
	pf.BoolVarP(&f.quiet, "quiet", "q", false, "suppress headers and informational messages")
	pf.BoolVar(&f.debug, "debug", false, "show underlying errors and request detail")
	pf.BoolVar(&f.stream, "stream", false, "with --output json, emit newline-delimited JSON")

	pf.StringVar(&f.token, "token", "", "use this token instead of authenticating")
	pf.StringVar(&f.method, "method", "", "authentication method: kerberos, device, app-token, basic, token")
	pf.StringVar(&f.user, "user", "", "username, for app-token and basic authentication")
	pf.StringVar(&f.appTokenFile, "app-token-file", "", "file holding a CERNBox app token")
	pf.StringVar(&f.kerberosMode, "kerberos-mode", "", "how Kerberos is used: spnego, sso, or auto")
	pf.StringVar(&f.kerberosSPN, "kerberos-spn", "",
		"service principal to request a ticket for, when it differs from HTTP/<endpoint host>")

	pf.BoolVar(&f.insecure, "insecure", false, "allow plain HTTP (development instances only)")
	pf.BoolVar(&f.skipVerify, "skip-verify", false, "do not verify the server certificate (development instances only)")
	pf.DurationVar(&f.timeout, "timeout", 0, "overall timeout for the command, 0 for none")

	cmd.AddCommand(
		newLoginCmd(app),
		newLogoutCmd(app),
		newStatusCmd(app),
		newWhoamiCmd(app),

		newLsCmd(app),
		newStatCmd(app),
		newFindCmd(app),
		newDuCmd(app),
		newCatCmd(app),
		newMkdirCmd(app),
		newTouchCmd(app),
		newRmCmd(app),
		newMvCmd(app),

		newCpCmd(app),
		newGetCmd(app),
		newPutCmd(app),
		newSyncCmd(app),

		newShareCmd(app),
		newLinkCmd(app),
		newSpaceCmd(app),
		newTokenCmd(app),
		newTrashCmd(app),
		newVersionsCmd(app),
		newOpenCmd(app),
		newAppsCmd(app),
		newOCMCmd(app),

		newVersionCmd(app),
		newCompletionCmd(),
		newCommandsCmd(),
	)
	return cmd
}

// setup builds the configuration, the output writer, the credential chain and
// the client. It runs before every command.
func (a *App) setup(cmd *cobra.Command) error {
	f := a.flags

	format, err := output.ParseFormat(f.outputFormat)
	if err != nil {
		return cberr.Usagef("%v", err)
	}
	if a.stdout == nil {
		a.stdout = os.Stdout
	}
	if a.stderr == nil {
		a.stderr = os.Stderr
	}
	a.out = output.New(a.stdout, format,
		output.Quiet(f.quiet),
		output.Stream(f.stream),
		output.Stderr(a.stderr),
		output.Color(a.stdout == os.Stdout && output.IsTerminal(os.Stdout)),
	)

	// Commands that need neither configuration nor the network stop here.
	if noServerNeeded(cmd) {
		return nil
	}

	cfg, err := LoadConfig(f.configPath)
	if err != nil {
		return cberr.Usagef("%v", err)
	}
	if f.endpoint != "" {
		cfg.Endpoint = f.endpoint
	}
	if f.method != "" {
		cfg.Auth.Method = f.method
	}
	if f.kerberosMode != "" {
		cfg.Auth.Kerberos.Mode = f.kerberosMode
	}
	if f.kerberosSPN != "" {
		cfg.Auth.Kerberos.ServicePrincipal = f.kerberosSPN
	}
	if f.insecure {
		cfg.Insecure = true
	}
	a.cfg = cfg

	if f.skipVerify || cfg.Insecure {
		a.warnInsecure(cfg.Endpoint)
	}

	hc := &http.Client{
		Timeout: f.timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: f.skipVerify},
			Proxy:           http.ProxyFromEnvironment,
		},
	}

	chain, err := a.buildChain(cfg, hc)
	if err != nil {
		return err
	}
	a.chain = chain

	c, err := client.New(cfg.Endpoint,
		client.WithHTTPClient(hc),
		client.WithCredentials(chain),
		client.WithUserAgent(userAgent()),
	)
	if err != nil {
		return err
	}
	a.client = c
	return nil
}

// warnInsecure refuses to disable certificate checks against a CERN host. A
// flag meant for a laptop instance must not become a habit that silently
// applies to production.
func (a *App) warnInsecure(endpoint string) {
	if strings.Contains(endpoint, "cern.ch") {
		a.out.Warn("refusing to disable transport security for %s", endpoint)
		return
	}
	a.out.Warn("transport security checks are disabled for %s", endpoint)
}

func (a *App) buildChain(cfg *Config, hc *http.Client) (*auth.Chain, error) {
	mode, err := auth.ParseKerberosMode(cfg.Auth.Kerberos.Mode)
	if err != nil {
		return nil, err
	}

	sso := &auth.SSOConfig{
		Issuer:           cfg.Auth.SSO.Issuer,
		ClientID:         cfg.Auth.SSO.ClientID,
		Audience:         cfg.Auth.SSO.Audience,
		Scopes:           cfg.Auth.SSO.Scopes,
		RedirectURI:      cfg.Auth.SSO.RedirectURI,
		ServicePrincipal: cfg.Auth.SSO.ServicePrincipal,
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))

	providers := []auth.Provider{
		&auth.TokenProvider{Value: a.flags.token},
		&auth.KerberosProvider{
			Mode:             mode,
			Endpoint:         cfg.Endpoint,
			SPNEGOPath:       cfg.Auth.Kerberos.Path,
			ServicePrincipal: cfg.Auth.Kerberos.ServicePrincipal,
			CCachePath:       cfg.Auth.Kerberos.CCache,
			SSO:              sso,
			HTTPClient:       hc,
		},
		&auth.AppTokenProvider{Username: a.flags.user, TokenFile: a.flags.appTokenFile},
		&auth.DeviceProvider{
			SSO:         sso,
			HTTPClient:  hc,
			Interactive: interactive,
			Prompt:      a.devicePrompt,
		},
	}

	// Basic authentication only ever applies when asked for by name. It exists
	// for development instances, and reaching it automatically would mean
	// prompting for a password on a server that expects Kerberos.
	if cfg.Auth.Method == auth.MethodBasic {
		providers = append(providers, &auth.BasicProvider{
			Username: a.flags.user,
			Prompt:   promptPassword,
		})
	}

	opts := []auth.ChainOption{auth.WithCache(auth.NewCache(cfg.Auth.TokenCache))}
	if cfg.Auth.Method != "" {
		opts = append(opts, auth.WithMethod(cfg.Auth.Method))
	}
	return auth.NewChain(cfg.Endpoint, providers, opts...), nil
}

func (a *App) devicePrompt(verificationURI, userCode string, expiresIn time.Duration) {
	fmt.Fprintf(os.Stderr, "\nTo sign in, open:\n\n    %s\n\nand enter the code:  %s\n\n",
		verificationURI, userCode)
	if expiresIn > 0 {
		fmt.Fprintf(os.Stderr, "The code expires in %s. Waiting...\n", expiresIn.Round(time.Second))
	}
}

func promptPassword(user string) (string, error) {
	fmt.Fprintf(os.Stderr, "Password for %s: ", user)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// noServerNeeded reports whether a command can run without configuration or a
// network connection.
func noServerNeeded(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "version", "completion", "help":
			return true
		}
	}
	return false
}

func userAgent() string {
	return fmt.Sprintf("cernbox-cli/%s (rev-%s)", Version, Commit)
}

// resolve turns a remote path argument into an absolute CERNBox path.
func (a *App) resolve(ctx context.Context, arg string) (string, error) {
	spec, err := pathspec.ParseRemote(arg)
	if err != nil {
		return "", cberr.Usagef("%v", err)
	}
	return spec.Resolve(ctx, a.client)
}

// resolveSpec turns a parsed spec into an absolute CERNBox path.
func (a *App) resolveSpec(ctx context.Context, spec pathspec.Spec) (string, error) {
	return spec.Resolve(ctx, a.client)
}

// transferEngine builds the transfer engine from configuration and flags.
func (a *App) transferEngine(o transferFlags) (*transfer.Engine, error) {
	chunk, err := ParseSize(a.cfg.Transfer.ChunkSize)
	if err != nil {
		return nil, cberr.Usagef("%v", err)
	}
	jobs := a.cfg.Transfer.Jobs
	if o.jobs > 0 {
		jobs = o.jobs
	}

	return transfer.New(a.client, transfer.Options{
		Jobs:      jobs,
		ChunkSize: chunk,
		DryRun:    o.dryRun,
		Overwrite: o.force,
		Verify:    o.verify || a.cfg.Transfer.Verify,
		Archive:   a.cfg.Transfer.ArchiveEnabled() && !o.noArchive,
		Progress:  a.progressFunc(),
	}), nil
}

// progressFunc renders progress only when stderr is a terminal: a progress bar
// in a log file is noise, and under --output json it would corrupt nothing but
// still waste the reader's attention.
func (a *App) progressFunc() transfer.ProgressFunc {
	if a.out.IsQuiet() || !output.IsTerminal(os.Stderr) || a.out.Format() == output.FormatJSON {
		return nil
	}
	return func(ev transfer.Event) {
		if !ev.Done {
			return
		}
		if ev.Err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", ev.Path, ev.Err)
			return
		}
		fmt.Fprintf(os.Stderr, "  %s (%s)\n", ev.Path, output.HumanSize(ev.Transferred))
	}
}

// ctx returns the command's context, honouring --timeout.
func (a *App) ctx(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	base := cmd.Context()
	if base == nil {
		base = context.Background()
	}
	if a.flags.timeout > 0 {
		return context.WithTimeout(base, a.flags.timeout)
	}
	return base, func() {}
}
