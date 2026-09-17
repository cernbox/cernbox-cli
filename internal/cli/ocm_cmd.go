package cli

import (
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newOCMCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ocm",
		Aliases: []string{"federated"},
		Short:   "Share with people at other institutions",
		Long: "Federated sharing through Open Cloud Mesh.\n\n" +
			"Sharing across institutions is a two-step affair: first the two people\n" +
			"establish a link by exchanging an invitation, then they share as usual.\n" +
			"Use 'ocm invite create' to start, and 'ocm contacts' to see who you can\n" +
			"already share with.\n\n" +
			"Once a contact is accepted, share with them using\n" +
			"'cernbox share create PATH --with-remote user@their-provider.org'.",
	}
	cmd.AddCommand(
		newOCMInviteCmd(app),
		newOCMContactsCmd(app),
		newOCMProvidersCmd(app),
		newOCMReceivedCmd(app),
	)
	return cmd
}

// ── invitations ──────────────────────────────────────────────────────────────

func newOCMInviteCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Create, list and accept federated sharing invitations",
	}
	cmd.AddCommand(newOCMInviteCreateCmd(app), newOCMInviteListCmd(app), newOCMInviteAcceptCmd(app))
	return cmd
}

func newOCMInviteCreateCmd(app *App) *cobra.Command {
	var description, recipient string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an invitation to send to someone at another institution",
		Long: "Create an invitation.\n\n" +
			"Send the link to the person you want to share with; they accept it at\n" +
			"their own provider. With --recipient the server mails it for you, if it\n" +
			"is configured to send mail.",
		Example: "  cernbox ocm invite create --description 'joint analysis'\n" +
			"  cernbox ocm invite create --recipient alice@other-lab.org",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			invite, err := app.client.GenerateInvite(ctx, description, recipient)
			if err != nil {
				return err
			}

			if recipient != "" {
				app.out.Msg("Invitation sent to %s.", recipient)
			}
			// The link is the thing the user has to pass on, so print it where
			// it can be copied or piped.
			if invite.Link != "" && app.out.Format() != output.FormatJSON {
				if _, err := app.stdout.Write([]byte(invite.Link + "\n")); err != nil {
					return err
				}
			}
			return app.out.Object(invite,
				output.Field{Name: "Token", Value: invite.Token},
				output.Field{Name: "Link", Value: orDash(invite.Link)},
				output.Field{Name: "Description", Value: orDash(invite.Description)},
				output.Field{Name: "Expires", Value: expiresColumn(invite.Expiration)},
			)
		},
	}

	cmd.Flags().StringVar(&description, "description", "", "note to remind you who this invitation was for")
	cmd.Flags().StringVar(&recipient, "recipient", "", "mail address to send the invitation to")
	return cmd
}

func newOCMInviteListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the invitations you have created",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			invites, err := app.client.ListInvites(ctx)
			if err != nil {
				return err
			}
			if len(invites) == 0 {
				app.out.Msg("You have no open invitations.")
			}

			table := output.Table{Headers: []string{"TOKEN", "DESCRIPTION", "EXPIRES"}, Items: invites}
			for _, inv := range invites {
				table.Rows = append(table.Rows, []string{
					inv.Token, orDash(inv.Description), expiresColumn(inv.Expiration),
				})
			}
			return app.out.Render(table)
		},
	}
}

func newOCMInviteAcceptCmd(app *App) *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "accept TOKEN",
		Short: "Accept an invitation from another institution",
		Long: "Accept an invitation.\n\n" +
			"You need the token and the provider it came from. If you were sent a\n" +
			"link, both are in it: pass the link instead of the token and the CLI\n" +
			"will take them apart.",
		Example: "  cernbox ocm invite accept abc123 --provider other-lab.org\n" +
			"  cernbox ocm invite accept 'https://other-lab.org/ocm/invite?token=abc123'",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			token, domain := splitInvite(args[0])
			if provider != "" {
				domain = provider
			}
			if domain == "" {
				return cberr.Usagef(
					"pass --provider with the domain the invitation came from, " +
						"or give the whole invitation link instead of just the token")
			}

			if err := app.client.AcceptInvite(ctx, token, domain); err != nil {
				return err
			}
			app.out.Msg("Accepted the invitation from %s. You can now share with them.", domain)
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "domain of the provider the invitation came from")
	return cmd
}

// splitInvite takes an invitation apart. A link carries both the token and the
// provider, and making the user retype either of them from a URL they were
// already sent is needless friction.
func splitInvite(arg string) (token, domain string) {
	if !strings.Contains(arg, "://") {
		return arg, ""
	}
	rest := arg
	if _, after, ok := strings.Cut(rest, "://"); ok {
		rest = after
	}
	host, query, _ := strings.Cut(rest, "/")
	domain = host

	if _, q, ok := strings.Cut(query, "?"); ok {
		for pair := range strings.SplitSeq(q, "&") {
			key, value, _ := strings.Cut(pair, "=")
			switch key {
			case "token":
				token = value
			case "providerDomain", "provider":
				domain = value
			}
		}
	}
	return token, domain
}

// ── contacts and providers ───────────────────────────────────────────────────

func newOCMContactsCmd(app *App) *cobra.Command {
	var remove string

	cmd := &cobra.Command{
		Use:     "contacts",
		Aliases: []string{"users"},
		Short:   "List the people at other institutions you can share with",
		Long: "List accepted federated contacts.\n\n" +
			"The ADDRESS column is what you pass to 'share create --with-remote'.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if remove != "" {
				user, idp, ok := strings.Cut(remove, "@")
				if !ok {
					return cberr.Usagef("--remove takes an address like user@their-provider.org")
				}
				if err := app.client.RemoveAcceptedUser(ctx, idp, user); err != nil {
					return err
				}
				app.out.Msg("Removed %s.", remove)
				return nil
			}

			users, err := app.client.AcceptedUsers(ctx)
			if err != nil {
				return err
			}
			if len(users) == 0 {
				app.out.Msg("You have no federated contacts yet. Start with 'cernbox ocm invite create'.")
			}

			table := output.Table{Headers: []string{"ADDRESS", "NAME", "MAIL", "PROVIDER"}, Items: users}
			for _, u := range users {
				table.Rows = append(table.Rows, []string{
					u.Address(), orDash(u.DisplayName), orDash(u.Mail), u.IDP,
				})
			}
			return app.out.Render(table)
		},
	}

	cmd.Flags().StringVar(&remove, "remove", "", "remove this contact, given as user@their-provider.org")
	return cmd
}

func newOCMProvidersCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List the institutions this server federates with",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			providers, err := app.client.ListProviders(ctx)
			if err != nil {
				return err
			}

			table := output.Table{Headers: []string{"DOMAIN", "NAME", "HOMEPAGE"}, Items: providers}
			for _, p := range providers {
				table.Rows = append(table.Rows, []string{
					orDash(p.Domain), orDash(firstNonEmpty(p.FullName, p.Name)), orDash(p.Homepage),
				})
			}
			return app.out.Render(table)
		},
	}
}

func newOCMReceivedCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "received",
		Short: "List shares you have received from other institutions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			shares, err := app.client.ReceivedFederatedShares(ctx)
			if err != nil {
				return err
			}
			if len(shares) == 0 {
				app.out.Msg("You have no federated shares.")
			}

			table := output.Table{Headers: []string{"ID", "NAME", "SHARED BY", "PROVIDER", "ROLE", "ACCEPTED"}, Items: shares}
			for _, s := range shares {
				table.Rows = append(table.Rows, []string{
					s.ID, orDash(s.Name), orDash(s.Owner), orDash(s.Provider),
					orDash(s.Role), yesNo(s.Accepted),
				})
			}
			return app.out.Render(table)
		},
	}
}
