package cli

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newShareAuditCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "audit PATH",
		Short: "Show everyone who can reach a file, and how",
		Long: "Show who can reach a path, including access granted further up.\n\n" +
			"Sharing a directory shares what is inside it, so the question 'who can see\n" +
			"this file' cannot be answered by looking at the file. This walks from the\n" +
			"space root down to the path and reports every grant it finds on the way,\n" +
			"saying which directory each one came from.\n\n" +
			"Unlike 'share list', which shows what you shared, this shows every\n" +
			"permission the server reports on those directories, whoever made it.",
		Example: "  cernbox share audit Documents/2026/report.pdf\n" +
			"  cernbox share audit /eos/project/c/cernbox/data",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			target, err := app.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			return app.auditAccess(ctx, target)
		},
	}
}

// accessGrant is one way somebody can reach the audited path.
type accessGrant struct {
	// Who is the username, group name, or the link's URL.
	Who string `json:"who"`
	// Kind is "user", "group" or "link".
	Kind string `json:"kind"`
	Role string `json:"role,omitempty"`
	// GrantedOn is the path carrying the permission, which is the audited path
	// itself or a directory above it.
	GrantedOn string `json:"granted_on"`
	// Inherited marks a grant that comes from a directory above the path, which
	// is the case people overlook.
	Inherited bool   `json:"inherited"`
	Expires   string `json:"expires,omitempty"`
	ID        string `json:"id,omitempty"`
}

func (a *App) auditAccess(ctx context.Context, target string) error {
	space, err := a.spaceContaining(ctx, target)
	if err != nil {
		return err
	}

	grants, err := a.grantsReaching(ctx, space.Path, target)
	if err != nil {
		return err
	}

	a.out.Msg("Path:  %s", target)
	a.out.Msg("Space: %s (%s)", client.SpaceAlias(*space), space.Type)

	table := output.Table{Headers: []string{"WHO", "ROLE", "KIND", "GRANTED ON"}, Items: grants}
	for _, g := range grants {
		on := g.GrantedOn
		if g.Inherited {
			on += "  (inherited)"
		}
		table.Rows = append(table.Rows, []string{g.Who, orDash(g.Role), g.Kind, on})
	}
	if err := a.out.Render(table); err != nil {
		return err
	}

	a.summariseAccess(grants, space)
	return nil
}

// summariseAccess says what the table means, on stderr so a pipe sees only the
// rows.
func (a *App) summariseAccess(grants []accessGrant, space *client.Space) {
	var people, groups, links, inherited int
	seen := map[string]int{}
	for _, g := range grants {
		switch g.Kind {
		case "group":
			groups++
		case "link":
			links++
		default:
			people++
		}
		if g.Inherited {
			inherited++
		}
		if g.Kind != "link" {
			seen[g.Who]++
		}
	}

	switch {
	case len(grants) == 0:
		a.out.Msg("Nothing grants access to this path on its own.")
	default:
		a.out.Msg("%s reach this path%s.", grantsPlural(people, groups, links),
			inheritedNote(inherited))
	}

	// A grant appearing twice for the same person is a nested share, which is
	// allowed and does work — which role wins is the storage's business, so this
	// points it out rather than guessing.
	for who, n := range seen {
		if n > 1 {
			a.out.Warn("%s is granted access at %d different levels; the storage decides "+
				"which role applies", who, n)
		}
	}

	// Membership of a project is access, and no share reports it. Leaving this
	// out would make an audit of a project path read as far more private than it is.
	if space.Type != "personal" {
		a.out.Msg("Everyone with access to the %s space can reach this as well, "+
			"whether or not anything above is shared.", client.SpaceAlias(*space))
	}
}

// grantsReaching collects the permissions on the path and on every directory
// between it and the space root.
//
// Downwards from the root rather than upwards from the path, so that the rows
// come out in the order somebody would explain them: the broadest grant first,
// the most specific last.
func (a *App) grantsReaching(ctx context.Context, root, target string) ([]accessGrant, error) {
	var out []accessGrant
	for _, p := range ancestorsFrom(root, target) {
		info, err := a.client.Stat(ctx, p)
		if err != nil {
			if cberr.KindOf(err) == cberr.KindNotFound {
				continue
			}
			// A directory above the path that cannot be read is not fatal: the
			// audit is still true about the levels it could see, and saying so
			// beats refusing to answer at all.
			a.out.Warn("cannot read %s, so any grant on it is not listed: %v", p, errLine(err))
			continue
		}

		perms, err := a.client.ListPermissions(ctx, info.ID)
		if err != nil {
			a.out.Warn("cannot list the shares on %s: %v", p, errLine(err))
			continue
		}
		for _, perm := range perms {
			out = append(out, toGrant(perm, p, p != target))
		}
	}
	return out, nil
}

func toGrant(p client.Permission, on string, inherited bool) accessGrant {
	g := accessGrant{
		Role:      p.Role,
		GrantedOn: on,
		Inherited: inherited,
		ID:        p.ID,
		Kind:      "user",
	}
	switch {
	case p.GrantedTo != nil:
		g.Who = firstNonEmpty(p.GrantedTo.ID, p.GrantedTo.DisplayName)
		if p.GrantedTo.Type != "" {
			g.Kind = p.GrantedTo.Type
		}
	case p.Link != nil:
		g.Who = firstNonEmpty(p.Link.URL, "(public link)")
		g.Kind = "link"
		// A link carries its role as the link type rather than as a role id.
		if g.Role == "" {
			g.Role = p.Link.Type
		}
	default:
		g.Who = "(unknown)"
	}
	if p.ExpiresAt != nil {
		g.Expires = p.ExpiresAt.Format(expiryLayout)
	}
	return g
}

// ancestorsFrom lists the space root, every directory between it and target, and
// target itself.
func ancestorsFrom(root, target string) []string {
	root = path.Clean(root)
	target = path.Clean(target)
	if root == target {
		return []string{root}
	}
	rel := strings.TrimPrefix(target, root+"/")
	if rel == target {
		// Not under the root at all, so the path is all there is to go on.
		return []string{target}
	}

	out := []string{root}
	at := root
	for _, seg := range strings.Split(rel, "/") {
		at = path.Join(at, seg)
		out = append(out, at)
	}
	return out
}

// spaceContaining finds the space a path belongs to, which is the one with the
// longest matching root: a project inside a home would otherwise match the home.
func (a *App) spaceContaining(ctx context.Context, target string) (*client.Space, error) {
	spaces, err := a.client.Spaces(ctx)
	if err != nil {
		return nil, err
	}

	var best *client.Space
	for i := range spaces {
		root := path.Clean(spaces[i].Path)
		if target != root && !strings.HasPrefix(target, root+"/") {
			continue
		}
		if best == nil || len(root) > len(path.Clean(best.Path)) {
			best = &spaces[i]
		}
	}
	if best == nil {
		return nil, cberr.New(cberr.KindNotFound, "audit access", target,
			"this path is not inside any space you can reach")
	}
	return best, nil
}

// grantsPlural counts the kinds of access in words.
//
// Not itemsPlural: that drops the number at one, which reads as "person, link
// reach this path", and it would pluralise person as persons.
func grantsPlural(people, groups, links int) string {
	var parts []string
	add := func(n int, one, many string) {
		switch n {
		case 0:
		case 1:
			parts = append(parts, "1 "+one)
		default:
			parts = append(parts, fmt.Sprintf("%d %s", n, many))
		}
	}
	add(people, "person", "people")
	add(groups, "group", "groups")
	add(links, "link", "links")

	switch len(parts) {
	case 0:
		return "Nothing"
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

func inheritedNote(n int) string {
	if n == 0 {
		return ""
	}
	if n == 1 {
		return ", 1 of them inherited from a directory above"
	}
	return fmt.Sprintf(", %d of them inherited from directories above", n)
}
