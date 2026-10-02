package cli

import (
	"errors"
	"slices"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/auth"
	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newLoginCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Sign in to CERNBox",
		Long: "Sign in and remember the session.\n\n" +
			"You usually do not need this. The CLI signs you in on its own when it\n" +
			"can. Use login to pick a method with --method, or on a computer where\n" +
			"that does not work.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			tok, err := app.chain.Token(ctx)
			if err != nil {
				return err
			}
			me, err := app.signedIn.Me(ctx)
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
		Short: "Show how you are signed in and what the server supports",
		Long: "Show the server, how you are signed in, and where the session is kept.\n" +
			"Run this first when something does not work.",
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
				if me, err := app.signedIn.Me(ctx); err == nil {
					st.User = me.Username
					st.DisplayName = me.DisplayName
				}
				st.ActingAs = app.flags.as
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
			if st.ActingAs != "" {
				fields = slices.Insert(fields, 2, output.Field{Name: "Acting as", Value: st.ActingAs})
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
	ActingAs      string   `json:"acting_as,omitempty"`
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
		Short: "Show who you are signed in as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			me, err := app.client.Me(ctx)
			if err != nil {
				return err
			}
			res := whoamiResult{User: *me}
			fields := []output.Field{
				{Name: "Username", Value: me.Username},
				{Name: "Display name", Value: me.DisplayName},
				{Name: "Mail", Value: me.Mail},
				{Name: "ID", Value: me.ID},
			}

			// A server without admin features is not an error here: whoami
			// answers who you are, and whether you are an admin is only known
			// when the server can say.
			if admin, err := app.client.IsAdmin(ctx); err == nil {
				res.Admin = &admin
				fields = append(fields, output.Field{Name: "Admin", Value: yesNo(admin)})
			}

			if app.flags.as != "" {
				signedIn, err := app.signedIn.Me(ctx)
				if err != nil {
					return err
				}
				res.ImpersonatedBy = signedIn.Username
				fields = append(fields, output.Field{Name: "Impersonated by", Value: signedIn.Username})
			}
			return app.out.Object(res, fields...)
		},
	}
}

type whoamiResult struct {
	client.User
	Admin          *bool  `json:"admin,omitempty"`
	ImpersonatedBy string `json:"impersonated_by,omitempty"`
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
