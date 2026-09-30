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

func newQuotaCmd(app *App) *cobra.Command {
	var all, versions bool

	cmd := &cobra.Command{
		Use:   "quota [SPACE]",
		Short: "Show how much space you have, and what is using it",
		Long: "Show how much of a space's quota is used.\n\n" +
			"Without an argument this is your own space; name any other you can reach,\n" +
			"by alias or by path, or pass --all for every one of them. A project's quota\n" +
			"is the project's, shared by everybody in it.\n\n" +
			"--versions additionally splits what is used into the files listings show and\n" +
			"the earlier versions they do not, which is the usual answer to a quota that\n" +
			"looks larger than anything you can find. It walks the whole space, so it is\n" +
			"much slower than the summary.",
		Example: "  cernbox quota\n" +
			"  cernbox quota project/cernbox\n" +
			"  cernbox quota --all\n" +
			"  cernbox quota --versions",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			if all && len(args) > 0 {
				return cberr.Usagef("--all covers every space, so it takes no argument")
			}

			spaces, err := app.client.Spaces(ctx)
			if err != nil {
				return err
			}

			if all {
				if versions {
					// One full walk per space, which on an account with several
					// projects is a long wait nobody asked for by writing --all.
					return cberr.Usagef("--versions measures one space at a time; name the one you mean")
				}
				return app.renderQuotaTable(spaces)
			}

			want := "home"
			if len(args) == 1 {
				want = args[0]
			}
			s, err := findSpace(ctx, app.client, spaces, want)
			if err != nil {
				return err
			}
			return app.renderQuota(ctx, *s, versions)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "report every space you can reach")
	cmd.Flags().BoolVar(&versions, "versions", false,
		"also split what is used into files and their earlier versions")
	return cmd
}

// findSpace accepts either form a user might reasonably type: the alias that
// "space list" prints, or the space's own path.
func findSpace(ctx context.Context, c *client.Client, spaces []client.Space, want string) (*client.Space, error) {
	if root, err := c.ResolveSpace(ctx, strings.TrimSuffix(want, ":")); err == nil {
		for i := range spaces {
			if spaces[i].Path == root {
				return &spaces[i], nil
			}
		}
	}
	// Not an alias, so try it as a path. Cleaned, so a trailing slash does not
	// decide whether a space is found.
	p := path.Clean("/" + strings.Trim(want, "/"))
	for i := range spaces {
		if path.Clean(spaces[i].Path) == p {
			return &spaces[i], nil
		}
	}
	return nil, cberr.New(cberr.KindNotFound, "report quota", want,
		"no such space — 'cernbox space list' shows the ones you can reach")
}

func (a *App) renderQuotaTable(spaces []client.Space) error {
	// Built from the same struct the single-space report uses, so that --output
	// json says "home" where the table says "home". Handing the raw spaces to
	// Items instead would emit the drive alias, and the two forms of the same
	// command would disagree about what a space is called.
	reports := make([]quotaReport, 0, len(spaces))
	for _, s := range spaces {
		reports = append(reports, quotaReport{
			Alias:     client.SpaceAlias(s),
			Type:      s.Type,
			Path:      s.Path,
			Total:     s.QuotaTotal,
			Used:      s.QuotaUsed,
			Remaining: s.QuotaRemaining,
		})
	}

	table := output.Table{Headers: []string{"ALIAS", "TYPE", "USED", "QUOTA", "USE%"}, Items: reports}
	for i, r := range reports {
		table.Rows = append(table.Rows, []string{
			r.Alias, r.Type,
			output.HumanSize(r.Used), quotaColumn(r.Total),
			usePercent(spaces[i]),
		})
	}
	return a.out.Render(table)
}

// quotaReport is what one space's numbers come to. It is a type so that
// --output json has something with named fields rather than a rendered block.
type quotaReport struct {
	Alias     string `json:"alias"`
	Type      string `json:"type"`
	Path      string `json:"path"`
	Total     int64  `json:"quota_total"`
	Used      int64  `json:"quota_used"`
	Remaining int64  `json:"quota_remaining"`

	// InTree is what the space's own directory is charged for: the files in it
	// and their earlier versions together. Set whenever it could be read.
	InTree int64 `json:"in_tree,omitempty"`
	// Files and Versions split InTree, and are only measured with --versions.
	Files    int64 `json:"files,omitempty"`
	Versions int64 `json:"versions,omitempty"`
	// Unaccounted is what the space's tree does not explain. It is normally zero;
	// when it is not, something outside the tree is being charged — a recycle bin
	// sharing the quota node, for instance — and saying so is better than
	// quietly presenting a breakdown that does not add up.
	Unaccounted int64 `json:"unaccounted,omitempty"`
}

func (a *App) renderQuota(ctx context.Context, s client.Space, versions bool) error {
	rep := quotaReport{
		Alias:     client.SpaceAlias(s),
		Type:      s.Type,
		Path:      s.Path,
		Total:     s.QuotaTotal,
		Used:      s.QuotaUsed,
		Remaining: s.QuotaRemaining,
	}

	// What the space's own tree is charged for. One request, and it is what makes
	// the rest of the report possible: the difference from Used is everything
	// being charged from outside the tree.
	if info, err := a.client.Stat(ctx, s.Path); err == nil {
		rep.InTree = info.Size
		if d := s.QuotaUsed - info.Size; d > 0 {
			rep.Unaccounted = d
		}
	}

	if versions {
		t, err := a.measureHidden(ctx, s.Path)
		if err != nil {
			return err
		}
		rep.InTree = t.charged[t.root]
		rep.Versions = t.unlisted[t.root]
		rep.Files = rep.InTree - rep.Versions
		if d := s.QuotaUsed - rep.InTree; d > 0 {
			rep.Unaccounted = d
		} else {
			rep.Unaccounted = 0
		}
	}

	fields := []output.Field{
		{Name: "Space", Value: rep.Alias},
		{Name: "Type", Value: rep.Type},
		{Name: "Path", Value: rep.Path},
		{Name: "Quota", Value: quotaColumn(rep.Total)},
		{Name: "Used", Value: fmt.Sprintf("%s (%s)", output.HumanSize(rep.Used), usePercent(s))},
		{Name: "Remaining", Value: output.HumanSize(rep.Remaining)},
	}
	if versions {
		fields = append(fields,
			output.Field{Name: "Files", Value: output.HumanSize(rep.Files)},
			output.Field{Name: "Versions", Value: output.HumanSize(rep.Versions)})
	} else if rep.InTree > 0 {
		fields = append(fields,
			output.Field{Name: "Files and versions", Value: output.HumanSize(rep.InTree)})
	}
	if rep.Unaccounted > 0 {
		fields = append(fields,
			output.Field{Name: "Outside the tree", Value: output.HumanSize(rep.Unaccounted)})
	}

	if err := a.out.Object(rep, fields...); err != nil {
		return err
	}

	if versions && rep.Versions > 0 {
		a.out.Msg("%s is earlier versions of files. They appear in no listing and cannot "+
			"be deleted on their own; the space comes back when a file is deleted and then "+
			"purged from the trash.", output.HumanSize(rep.Versions))
	} else if !versions {
		a.out.Msg("'cernbox quota --versions' splits that into files and their earlier versions.")
	}
	if rep.Unaccounted > 0 {
		a.out.Msg("%s is charged to this space from outside its directory, which is usually "+
			"a recycle bin sharing the quota.", output.HumanSize(rep.Unaccounted))
	}
	if rep.InTree > rep.Used {
		// Not an error and not over quota: the quota and the directory total are
		// separate pieces of storage bookkeeping, updated at different moments, so
		// one lags the other after anything is written or deleted in bulk.
		a.out.Msg("The directory is reported as larger than the quota says is used. " +
			"The two figures are maintained separately, so one lags the other after " +
			"a lot of writing or deleting; both settle within a minute or so.")
	}
	return nil
}

// usePercent is the share of the quota in use, or "-" when there is no quota to
// take a share of.
func usePercent(s client.Space) string {
	if s.QuotaTotal <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", int64(float64(s.QuotaUsed)/float64(s.QuotaTotal)*100))
}
