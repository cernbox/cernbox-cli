package cli

import (
	"context"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

const expiryLayout = "2006-01-02"

func parseExpiry(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(expiryLayout, s)
	if err != nil {
		return nil, cberr.Usagef("invalid expiry %q: want a date like 2026-12-31", s)
	}
	// End of the given day, so "--expiry 2026-12-31" keeps the share usable
	// throughout that date rather than expiring it at midnight.
	t = t.Add(24*time.Hour - time.Second)
	return &t, nil
}

func newShareCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Share files with users and groups",
	}
	cmd.AddCommand(
		newShareCreateCmd(app),
		newShareListCmd(app),
		newShareUpdateCmd(app),
		newShareRemoveCmd(app),
		newShareReceivedCmd(app),
	)
	return cmd
}

func newShareCreateCmd(app *App) *cobra.Command {
	var with []string
	var groups []string
	var remotes []string
	var role, expiry string

	cmd := &cobra.Command{
		Use:     "create PATH",
		Short:   "Share a file or directory",
		Example: "  cernbox share create /eos/user/g/gdelmont/Documents --with marie --role editor",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if len(with) == 0 && len(groups) == 0 && len(remotes) == 0 {
				return cberr.Usagef("pass --with USER, --with-group GROUP, or --with-remote USER@PROVIDER")
			}
			for _, r := range remotes {
				if !strings.Contains(r, "@") {
					return cberr.Usagef(
						"--with-remote takes an address like user@their-provider.org, got %q.\n"+
							"Run 'cernbox ocm contacts' to see the addresses you can share with.", r)
				}
			}
			roleID, err := client.RoleID(role)
			if err != nil {
				return err
			}
			exp, err := parseExpiry(expiry)
			if err != nil {
				return err
			}

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}

			recipients := make([]client.Recipient, 0, len(with)+len(groups))
			for _, u := range with {
				recipients = append(recipients, client.Recipient{ID: u, Type: "user"})
			}
			for _, g := range groups {
				recipients = append(recipients, client.Recipient{ID: g, Type: "group"})
			}
			for _, r := range remotes {
				recipients = append(recipients, client.Recipient{ID: r, Type: client.RecipientRemote})
			}

			perms, err := app.client.Share(ctx, info.ID, recipients, roleID, exp)
			if err != nil {
				return err
			}
			return app.renderPermissions(perms)
		},
	}

	cmd.Flags().StringSliceVar(&with, "with", nil, "username to share with (repeatable)")
	cmd.Flags().StringSliceVar(&groups, "with-group", nil, "group to share with (repeatable)")
	cmd.Flags().StringSliceVar(&remotes, "with-remote", nil,
		"federated user to share with, as user@their-provider.org (repeatable)")
	cmd.Flags().StringVar(&role, "role", "viewer", "viewer, editor, collab, or denied")
	cmd.Flags().StringVar(&expiry, "expiry", "", "expiry date, YYYY-MM-DD")
	return cmd
}

func newShareListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list [PATH]",
		Short: "List shares on a path, or everything you have shared",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if len(args) == 0 {
				items, err := app.client.SharedByMe(ctx)
				if err != nil {
					return err
				}
				return app.renderDriveItems(items, "SHARED WITH")
			}

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			perms, err := app.client.ListPermissions(ctx, info.ID)
			if err != nil {
				return err
			}
			return app.renderPermissions(perms)
		},
	}
}

func newShareUpdateCmd(app *App) *cobra.Command {
	var role, expiry string

	cmd := &cobra.Command{
		Use:   "update PATH SHARE_ID",
		Short: "Change the role or expiry of a share",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			roleID := ""
			if role != "" {
				var err error
				roleID, err = client.RoleID(role)
				if err != nil {
					return err
				}
			}
			exp, err := parseExpiry(expiry)
			if err != nil {
				return err
			}

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			perm, err := app.client.UpdatePermission(ctx, info.ID, args[1], roleID, exp)
			if err != nil {
				return err
			}
			return app.renderPermissions([]client.Permission{*perm})
		},
	}

	cmd.Flags().StringVar(&role, "role", "", "new role: viewer, editor, collab, or denied")
	cmd.Flags().StringVar(&expiry, "expiry", "", "new expiry date, YYYY-MM-DD")
	return cmd
}

func newShareRemoveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove PATH SHARE_ID",
		Short: "Remove a share",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			if err := app.client.RemovePermission(ctx, info.ID, args[1]); err != nil {
				return err
			}
			app.out.Msg("Removed share %s", args[1])
			return nil
		},
	}
}

func newShareReceivedCmd(app *App) *cobra.Command {
	var accept, decline string

	cmd := &cobra.Command{
		Use:   "received",
		Short: "List, accept or decline shares other people made with you",
		Long: "List what other people have shared with you.\n\n" +
			"Declining takes a share out of your listings; accepting puts it back.\n" +
			"Use the id from this listing for either.",
		Example: "  cernbox share received\n" +
			"  cernbox share received --decline SHARE_ID",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			switch {
			case accept != "" && decline != "":
				return cberr.Usagef("pass either --accept or --decline, not both")
			case accept != "":
				if err := app.client.SetReceivedShareState(ctx, accept, true); err != nil {
					return err
				}
				app.out.Msg("Accepted share %s", accept)
				return nil
			case decline != "":
				if err := app.client.SetReceivedShareState(ctx, decline, false); err != nil {
					return err
				}
				app.out.Msg("Declined share %s", decline)
				return nil
			}

			items, err := app.client.SharedWithMe(ctx)
			if err != nil {
				return err
			}
			return app.renderDriveItems(items, "SHARED BY")
		},
	}

	cmd.Flags().StringVar(&accept, "accept", "", "accept the share with this id")
	cmd.Flags().StringVar(&decline, "decline", "", "decline the share with this id")
	return cmd
}

// ── links ────────────────────────────────────────────────────────────────────

func newLinkCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Create and manage public links",
	}
	cmd.AddCommand(
		newLinkCreateCmd(app),
		newLinkListCmd(app),
		newLinkUpdateCmd(app),
		newLinkRemoveCmd(app),
		newLinkPasswordCmd(app),
	)
	return cmd
}

func newLinkCreateCmd(app *App) *cobra.Command {
	var role, name, expiry string
	var withPassword bool

	cmd := &cobra.Command{
		Use:     "create PATH",
		Short:   "Create a public link",
		Example: "  cernbox link create /eos/user/g/gdelmont/report.pdf --role viewer --expiry 2026-12-31",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			linkType, err := linkTypeOf(role)
			if err != nil {
				return err
			}

			exp, err := parseExpiry(expiry)
			if err != nil {
				return err
			}

			password := ""
			if withPassword {
				// Read the password rather than accepting it as a flag: a
				// flag value ends up in the shell history and in ps output.
				password, err = promptPassword("the link")
				if err != nil {
					return cberr.Usagef("reading the password: %v", err)
				}
			}

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			perm, err := app.client.CreateLink(ctx, info.ID, client.LinkOptions{
				Type: linkType, DisplayName: name, Password: password, Expiry: exp,
			})
			if err != nil {
				return err
			}

			// The link table, not the share table: a link has no grantee, and
			// its role lives in the link type, so the share columns showed the
			// URL under "GRANTED TO" and left ROLE empty. The URL was also
			// printed separately to stderr, so it appeared twice.
			return app.renderLinks([]client.Permission{*perm})
		},
	}

	cmd.Flags().StringVar(&role, "role", "viewer", "viewer or editor")
	cmd.Flags().StringVar(&name, "name", "", "label shown next to the link")
	cmd.Flags().StringVar(&expiry, "expiry", "", "expiry date, YYYY-MM-DD")
	cmd.Flags().BoolVar(&withPassword, "password", false, "protect the link with a password, prompted for")
	return cmd
}

// linkTypeOf maps what a user types for --role onto the two link types the
// server knows. The spellings are accepted rather than corrected because
// "--role read" and "--role view" are the same intention.
func linkTypeOf(role string) (string, error) {
	switch role {
	case "viewer", "view", "read":
		return "view", nil
	case "editor", "edit", "write":
		return "edit", nil
	default:
		return "", cberr.Usagef("unknown link role %q: want viewer or editor", role)
	}
}

func newLinkUpdateCmd(app *App) *cobra.Command {
	var role, name, expiry string
	var noExpiry bool

	cmd := &cobra.Command{
		Use:   "update PATH LINK_ID",
		Short: "Change the role, name or expiry of a public link",
		Long: "Change a public link that already exists, leaving its address alone.\n\n" +
			"Anybody holding the link keeps the same address, so use this to tighten\n" +
			"a link rather than replacing one you have already sent out. Use 'link\n" +
			"password' for the password.",
		Example: "  cernbox link update /eos/user/g/gdelmont/report.pdf link-1 --role viewer\n" +
			"  cernbox link update /eos/user/g/gdelmont/report.pdf link-1 --expiry 2026-12-31\n" +
			"  cernbox link update /eos/user/g/gdelmont/report.pdf link-1 --no-expiry",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			update := client.LinkUpdate{DisplayName: name, ClearExpiry: noExpiry}
			if role != "" {
				var err error
				if update.Type, err = linkTypeOf(role); err != nil {
					return err
				}
			}
			exp, err := parseExpiry(expiry)
			if err != nil {
				return err
			}
			update.Expiry = exp

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			perm, err := app.client.UpdateLink(ctx, info.ID, args[1], update)
			if err != nil {
				return err
			}
			return app.renderLinks([]client.Permission{*perm})
		},
	}

	cmd.Flags().StringVar(&role, "role", "", "new role: viewer or editor")
	cmd.Flags().StringVar(&name, "name", "", "new label shown next to the link")
	cmd.Flags().StringVar(&expiry, "expiry", "", "new expiry date, YYYY-MM-DD")
	cmd.Flags().BoolVar(&noExpiry, "no-expiry", false, "let the link stay usable indefinitely")
	return cmd
}

func newLinkListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list PATH",
		Short: "List the public links on a path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			perms, err := app.client.ListPermissions(ctx, info.ID)
			if err != nil {
				return err
			}

			links := make([]client.Permission, 0, len(perms))
			for _, p := range perms {
				if p.Link != nil {
					links = append(links, p)
				}
			}

			return app.renderLinks(links)
		},
	}
}

func newLinkRemoveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove PATH LINK_ID",
		Short: "Remove a public link",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			if err := app.client.RemovePermission(ctx, info.ID, args[1]); err != nil {
				return err
			}
			app.out.Msg("Removed link %s", args[1])
			return nil
		},
	}
}

func newLinkPasswordCmd(app *App) *cobra.Command {
	var clear bool

	cmd := &cobra.Command{
		Use:   "password PATH LINK_ID",
		Short: "Set or clear the password on a public link",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			password := ""
			if !clear {
				var err error
				password, err = promptPassword("the link")
				if err != nil {
					return cberr.Usagef("reading the password: %v", err)
				}
			}

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}
			if err := app.client.SetLinkPassword(ctx, info.ID, args[1], password); err != nil {
				return err
			}
			if clear {
				app.out.Msg("Removed the password from link %s", args[1])
			} else {
				app.out.Msg("Set the password on link %s", args[1])
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&clear, "clear", false, "remove the password instead of setting one")
	return cmd
}

// ── shared rendering ─────────────────────────────────────────────────────────

// statResolved resolves a path argument and stats it, which is how the CLI
// obtains the resource id every sharing call needs.
func (a *App) statResolved(ctx context.Context, arg string) (*client.ResourceInfo, error) {
	p, err := a.resolve(ctx, arg)
	if err != nil {
		return nil, err
	}
	info, err := a.client.Stat(ctx, p)
	if err != nil {
		return nil, err
	}
	if info.ID == "" {
		return nil, cberr.New(cberr.KindOther, "share", p,
			"the server did not report a resource id for this path")
	}
	return info, nil
}

// renderLinks renders public links. They get their own columns because a link
// has no grantee to name and carries its role as the link type, so the share
// table left one column empty and repeated the URL in another.
func (a *App) renderLinks(links []client.Permission) error {
	table := output.Table{Headers: []string{"ID", "NAME", "TYPE", "PASSWORD", "EXPIRES", "URL"}, Items: links}
	for _, p := range links {
		kind, url, name, locked := "-", "-", "-", false
		if p.Link != nil {
			kind = orDash(p.Link.Type)
			url = orDash(p.Link.URL)
			name = orDash(p.Link.Name)
			locked = p.Link.HasPassword
		}
		table.Rows = append(table.Rows, []string{
			p.ID, name, kind, yesNo(locked), expiresColumn(p.ExpiresAt), url,
		})
	}
	return a.out.Render(table)
}

func (a *App) renderPermissions(perms []client.Permission) error {
	table := output.Table{Headers: []string{"ID", "ROLE", "GRANTED TO", "TYPE", "EXPIRES"}, Items: perms}
	for _, p := range perms {
		grantee, kind := "-", "-"
		switch {
		case p.GrantedTo != nil:
			grantee = firstNonEmpty(p.GrantedTo.DisplayName, p.GrantedTo.ID)
			kind = p.GrantedTo.Type
		case p.Link != nil:
			grantee = p.Link.URL
			kind = "link"
		}
		// A public link carries its role as the link type ("view", "edit")
		// rather than as a unified role id, so reading only Role left the
		// column empty for every link.
		role := p.Role
		if role == "" && p.Link != nil {
			role = p.Link.Type
		}
		table.Rows = append(table.Rows, []string{p.ID, orDash(role), grantee, kind, expiresColumn(p.ExpiresAt)})
	}
	return a.out.Render(table)
}

func (a *App) renderDriveItems(items []client.DriveItem, peerHeader string) error {
	table := output.Table{Headers: []string{"ID", "NAME", "ROLE", peerHeader, "ACCEPTED"}, Items: items}
	for _, it := range items {
		peer := "-"
		if it.SharedBy != nil {
			peer = firstNonEmpty(it.SharedBy.DisplayName, it.SharedBy.ID)
		}
		table.Rows = append(table.Rows, []string{it.ID, it.Name, orDash(it.Role), peer, yesNo(it.Accepted)})
	}
	return a.out.Render(table)
}

func expiresColumn(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format(expiryLayout)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "-"
}
