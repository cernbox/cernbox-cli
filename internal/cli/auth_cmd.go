package cli

import (
	"errors"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/auth"
	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newLoginCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate to CERNBox",
		Long: "Authenticate to CERNBox and cache the session.\n\n" +
			"You rarely need this. On lxplus the CLI uses your Kerberos ticket\n" +
			"automatically; run login only to pick a specific method with --method,\n" +
			"or to sign in from a machine with no ticket.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			tok, err := app.chain.Token(ctx)
			if err != nil {
				return err
			}
			me, err := app.client.Me(ctx)
			if err != nil {
				return err
			}

			app.out.Msg("Signed in as %s (%s) using %s.", me.Username, me.DisplayName, tok.Provider)
			return app.out.Object(loginResult{
				User:     me.Username,
				Provider: tok.Provider,
				Expires:  expiryString(tok),
			},
				output.Field{Name: "User", Value: me.Username},
				output.Field{Name: "Provider", Value: tok.Provider},
				output.Field{Name: "Expires", Value: expiryString(tok)},
			)
		},
	}
}

type loginResult struct {
	User     string `json:"user"`
	Provider string `json:"provider"`
	Expires  string `json:"expires,omitempty"`
}

func newLogoutCmd(app *App) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget the cached session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				if err := auth.NewCache(app.cfg.Auth.TokenCache).Clear(); err != nil {
					return cberr.Wrap(cberr.KindOther, "clear the token cache", "", err)
				}
				app.out.Msg("Cleared all cached sessions.")
				return nil
			}
			if err := app.chain.Forget(); err != nil {
				return cberr.Wrap(cberr.KindOther, "clear the token cache", "", err)
			}
			app.out.Msg("Signed out of %s.", app.cfg.Endpoint)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "forget sessions for every endpoint, not just this one")
	return cmd
}

func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the endpoint, credentials and server capabilities",
		Long: "Show which credential the CLI would use, what the server supports, and\n" +
			"where the token cache lives. Run this first when something is not working.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			st := statusResult{
				Endpoint:   app.cfg.Endpoint,
				TokenCache: auth.NewCache(app.cfg.Auth.TokenCache).Path(),
				Version:    Version,
			}

			// Report which providers could be used before authenticating, so
			// that status still says something useful when nothing works.
			for _, p := range app.chain.Providers() {
				if p.Available(ctx) {
					st.Available = append(st.Available, p.Name())
				}
			}

			tok, tokErr := app.chain.Token(ctx)
			if tokErr == nil {
				st.Provider = tok.Provider
				st.Expires = expiryString(tok)
				if me, err := app.client.Me(ctx); err == nil {
					st.User = me.Username
					st.DisplayName = me.DisplayName
				}
			} else {
				st.Error = tokErr.Error()
			}

			if caps, err := app.client.Capabilities(ctx); err == nil {
				st.Server = caps.ProductName
				st.ServerVersion = caps.ServerVersion
				st.Tus = caps.TusSupported
				st.Archiver = caps.ArchiverEnabled
			}

			fields := []output.Field{
				{Name: "Endpoint", Value: st.Endpoint},
				{Name: "User", Value: orDash(st.User)},
				{Name: "Provider", Value: orDash(st.Provider)},
				{Name: "Expires", Value: orDash(st.Expires)},
				{Name: "Available", Value: orDash(joinOrDash(st.Available))},
				{Name: "Token cache", Value: st.TokenCache},
				{Name: "Server", Value: orDash(st.ServerVersion)},
				{Name: "Client", Value: Version},
			}
			if st.Error != "" {
				fields = append(fields, output.Field{Name: "Problem", Value: st.Error})
			}
			return app.out.Object(st, fields...)
		},
	}
}

type statusResult struct {
	Endpoint      string   `json:"endpoint"`
	User          string   `json:"user,omitempty"`
	DisplayName   string   `json:"display_name,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	Expires       string   `json:"expires,omitempty"`
	Available     []string `json:"available_methods,omitempty"`
	TokenCache    string   `json:"token_cache"`
	Server        string   `json:"server,omitempty"`
	ServerVersion string   `json:"server_version,omitempty"`
	Tus           bool     `json:"tus_supported"`
	Archiver      bool     `json:"archiver_enabled"`
	Version       string   `json:"client_version"`
	Error         string   `json:"error,omitempty"`
}

func newWhoamiCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the authenticated identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			me, err := app.client.Me(ctx)
			if err != nil {
				return err
			}
			return app.out.Object(me,
				output.Field{Name: "Username", Value: me.Username},
				output.Field{Name: "Display name", Value: me.DisplayName},
				output.Field{Name: "Mail", Value: me.Mail},
				output.Field{Name: "ID", Value: me.ID},
			)
		},
	}
}

func expiryString(tok *auth.Token) string {
	if tok == nil || tok.Expiry.IsZero() {
		return ""
	}
	remaining := time.Until(tok.Expiry).Round(time.Second)
	if remaining < 0 {
		return "expired"
	}
	return tok.Expiry.Local().Format(time.RFC3339) + " (in " + remaining.String() + ")"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinOrDash(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for _, s := range items[1:] {
		out += ", " + s
	}
	return out
}

// asCberr is errors.As specialised to *cberr.Error, kept here so root.go does
// not need the errors import twice.
func asCberr(err error, target **cberr.Error) bool {
	return errors.As(err, target)
}
