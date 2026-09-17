package cli

import (
	"runtime"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newSpaceCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "space",
		Short: "List and inspect your spaces",
	}
	cmd.AddCommand(newSpaceListCmd(app), newSpaceInfoCmd(app))
	return cmd
}

func newSpaceListCmd(app *App) *cobra.Command {
	var spaceType string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the spaces you can reach",
		Long: "List your spaces. The ALIAS column shows what you can type as a path\n" +
			"prefix: 'home:Documents', 'project/cernbox:data'.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			spaces, err := app.client.Spaces(ctx)
			if err != nil {
				return err
			}
			if spaceType != "" {
				filtered := spaces[:0]
				for _, s := range spaces {
					if s.Type == spaceType {
						filtered = append(filtered, s)
					}
				}
				spaces = filtered
			}

			table := output.Table{Headers: []string{"ALIAS", "TYPE", "PATH", "USED", "QUOTA"}, Items: spaces}
			for _, s := range spaces {
				table.Rows = append(table.Rows, []string{
					client.SpaceAlias(s), s.Type, s.Path,
					output.HumanSize(s.QuotaUsed), quotaColumn(s.QuotaTotal),
				})
			}
			return app.out.Render(table)
		},
	}

	cmd.Flags().StringVar(&spaceType, "type", "", "only show spaces of this type: personal or project")
	return cmd
}

func newSpaceInfoCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "info ALIAS",
		Short: "Show details for one space",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			spaces, err := app.client.Spaces(ctx)
			if err != nil {
				return err
			}
			root, err := app.client.ResolveSpace(ctx, args[0])
			if err != nil {
				return err
			}
			for _, s := range spaces {
				if s.Path != root {
					continue
				}
				return app.out.Object(s,
					output.Field{Name: "Alias", Value: client.SpaceAlias(s)},
					output.Field{Name: "Name", Value: s.Name},
					output.Field{Name: "Type", Value: s.Type},
					output.Field{Name: "Path", Value: s.Path},
					output.Field{Name: "ID", Value: s.ID},
					output.Field{Name: "Used", Value: output.HumanSize(s.QuotaUsed)},
					output.Field{Name: "Quota", Value: quotaColumn(s.QuotaTotal)},
					output.Field{Name: "Remaining", Value: output.HumanSize(s.QuotaRemaining)},
				)
			}
			return cberr.New(cberr.KindNotFound, "describe space", args[0], "no such space")
		},
	}
}

func quotaColumn(total int64) string {
	if total <= 0 {
		return "unlimited"
	}
	return output.HumanSize(total)
}

// ── app tokens ───────────────────────────────────────────────────────────────

func newTokenCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage app tokens for batch jobs and cron",
		Long: "App tokens are long-lived credentials for automation, where no Kerberos\n" +
			"ticket is available.\n\n" +
			"Scope them: a token limited to one path and read access is a much smaller\n" +
			"problem if it leaks than one with full account access.",
	}
	cmd.AddCommand(newTokenListCmd(app), newTokenRevokeCmd(app), newTokenCreateCmd(app))
	return cmd
}

func newTokenListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the app tokens on your account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			clients, err := app.client.ListConnectedClients(ctx)
			if err != nil {
				return err
			}
			table := output.Table{Headers: []string{"ID", "NAME", "CLIENT", "CREATED", "LAST SEEN"}, Items: clients}
			for _, c := range clients {
				table.Rows = append(table.Rows, []string{
					c.ID, orDash(c.Name), orDash(c.Description), orDash(c.CreatedAt), orDash(c.LastSeenAt),
				})
			}
			return app.out.Render(table)
		},
	}
}

func newTokenRevokeCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke ID",
		Short: "Revoke an app token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if err := app.client.RevokeConnectedClient(ctx, args[0]); err != nil {
				return err
			}
			app.out.Msg("Revoked app token %s", args[0])
			return nil
		},
	}
}

func newTokenCreateCmd(app *App) *cobra.Command {
	var scopePath, permission, expiry, label string
	var unlimited bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an app token",
		Example: "  cernbox token create --path /eos/project/c/cernbox/data --permission read --expiry 2026-12-31\n" +
			"  cernbox token create --all --label 'nightly backup'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if scopePath == "" && !unlimited {
				return cberr.Usagef(
					"pass --path to scope the token to one directory, or --all to create an " +
						"unscoped token with full access to your account")
			}
			if _, err := parseExpiry(expiry); err != nil {
				return err
			}
			switch permission {
			case "read", "write", "":
			default:
				return cberr.Usagef("unknown permission %q: want read or write", permission)
			}

			// CERNBox exposes listing and revocation of app tokens over OCS,
			// but minting one goes through the browser enrolment flow rather
			// than a public endpoint. Say so plainly instead of failing with a
			// 404 the user cannot act on.
			return cberr.New(cberr.KindOther, "create an app token", "",
				"CERNBox does not expose app-token creation over its public API.\n"+
					"Create one in the web interface under Settings, then set CERNBOX_APP_TOKEN\n"+
					"or pass --app-token-file. Use 'cernbox token list' and 'cernbox token revoke'\n"+
					"to manage the tokens you already have.")
		},
	}

	cmd.Flags().StringVar(&scopePath, "path", "", "limit the token to this path")
	cmd.Flags().StringVar(&permission, "permission", "read", "read or write")
	cmd.Flags().StringVar(&expiry, "expiry", "", "expiry date, YYYY-MM-DD")
	cmd.Flags().StringVar(&label, "label", "", "label to identify the token later")
	cmd.Flags().BoolVar(&unlimited, "all", false, "create an unscoped token with full account access")
	return cmd
}

// ── version and completion ───────────────────────────────────────────────────

func newVersionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the client version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v := versionInfo{
				Version:   Version,
				Commit:    Commit,
				BuildDate: BuildDate,
				Go:        runtime.Version(),
				Platform:  runtime.GOOS + "/" + runtime.GOARCH,
			}
			return app.out.Object(v,
				output.Field{Name: "Version", Value: v.Version},
				output.Field{Name: "Commit", Value: v.Commit},
				output.Field{Name: "Built", Value: v.BuildDate},
				output.Field{Name: "Go", Value: v.Go},
				output.Field{Name: "Platform", Value: v.Platform},
			)
		},
	}
}

type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Go        string `json:"go"`
	Platform  string `json:"platform"`
}

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish]",
		Short: "Print a shell completion script",
		Long: "Print a shell completion script.\n\n" +
			"  bash:  source <(cernbox completion bash)\n" +
			"  zsh:   cernbox completion zsh > \"${fpath[1]}/_cernbox\"\n" +
			"  fish:  cernbox completion fish | source",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(out, true)
			case "zsh":
				return cmd.Root().GenZshCompletion(out)
			case "fish":
				return cmd.Root().GenFishCompletion(out, true)
			default:
				return cberr.Usagef("unknown shell %q: want bash, zsh, or fish", args[0])
			}
		},
	}
	return cmd
}
