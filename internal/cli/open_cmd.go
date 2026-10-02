package cli

import (
	"context"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newOpenCmd(app *App) *cobra.Command {
	var appName, viewMode string
	var web, launch bool

	cmd := &cobra.Command{
		Use:   "open PATH",
		Short: "Print the link that opens a file in a browser",
		Long: "Print the URL that opens a file in CERNBox: the online editor for its type,\n" +
			"or with --web its page in the CERNBox interface.\n" +
			"\n" +
			"Like the web interface, it opens the editor when you can write to the file\n" +
			"and a read-only view otherwise; --view-mode read asks for the read-only one.\n" +
			"\n" +
			"The link is only printed unless you pass --launch.",
		Example: "  cernbox open /eos/user/g/gdelmont/report.docx\n" +
			"  cernbox open --web /eos/user/g/gdelmont/Documents\n" +
			"  cernbox open --app Collabora --view-mode read /eos/user/g/gdelmont/notes.odt",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			info, err := app.statResolved(ctx, args[0])
			if err != nil {
				return err
			}

			if web {
				if info.WebURL == "" {
					return cberr.New(cberr.KindOther, "open", info.Path,
						"this server does not report a web link for this resource")
				}
				return app.emitLink(info.WebURL, launch, openResult{Path: info.Path, URL: info.WebURL, Kind: "web"})
			}

			// No mode leaves it to the server, which is what the web interface
			// does: reva starts from read-write and downgrades to read-only when
			// the user cannot write. Asking for read by default sent CodiMD users
			// to a published, read-only copy of a note they could edit.
			mode := ""
			switch viewMode {
			case "":
			case "read":
				mode = client.ViewModeRead
			case "write", "edit":
				mode = client.ViewModeWrite
			default:
				return cberr.Usagef("unknown view mode %q: want read or write", viewMode)
			}

			if appName == "" {
				if appName, err = app.defaultApp(ctx, info); err != nil {
					return err
				}
			}

			session, err := app.client.OpenInApp(ctx, info.ID, appName, mode)
			if err != nil {
				return err
			}

			// A POST session cannot be opened by pasting a link: the editor
			// needs form parameters the browser would have to submit. Say so
			// rather than print a URL that will not work.
			if !session.OpenableInBrowser() {
				app.out.Warn("this application needs a %s request with form parameters, "+
					"so the link cannot be opened directly; use the web interface", session.Method)
			}

			return app.emitLink(session.URL, launch && session.OpenableInBrowser(), openResult{
				Path:           info.Path,
				URL:            session.URL,
				Kind:           "app",
				Method:         session.Method,
				FormParameters: session.FormParameters,
			})
		},
	}

	cmd.Flags().StringVar(&appName, "app", "", "application to open with (default: the server's choice for the file type)")
	cmd.Flags().StringVar(&viewMode, "view-mode", "", "read or write (default: write when you can, read otherwise)")
	cmd.Flags().BoolVar(&web, "web", false, "print the CERNBox web interface link instead of an application link")
	cmd.Flags().BoolVar(&launch, "launch", false, "also open the link in a browser")
	return cmd
}

// defaultApp picks the application to open a file with when none was asked
// for. The server only falls back to a default an administrator configured for
// the type, and fails with a 500 when there is none — which is how text/plain
// is on CERNBox, with CodiMD as its only application. The web interface then
// offers the one application there is, so this does the same. With several and
// no default, choosing would be a guess: reva itself refuses to, because its
// order of applications changes across restarts.
//
// It returns "" to leave the choice to the server, which is also the answer
// whenever the catalogue cannot settle it: the server's error says more.
func (a *App) defaultApp(ctx context.Context, info *client.ResourceInfo) (string, error) {
	if info.MimeType == "" {
		return "", nil
	}
	types, err := a.client.ListApps(ctx)
	if err != nil {
		return "", nil
	}
	for _, t := range types {
		if t.MimeType != info.MimeType {
			continue
		}
		switch {
		case t.Default != "":
			return "", nil
		case len(t.Apps) == 1:
			return t.Apps[0], nil
		case len(t.Apps) > 1:
			return "", cberr.Usagef("%s has no default application; choose one with --app: %s",
				info.MimeType, strings.Join(t.Apps, ", "))
		}
	}
	return "", nil
}

type openResult struct {
	Path           string            `json:"path"`
	URL            string            `json:"url"`
	Kind           string            `json:"kind"`
	Method         string            `json:"method,omitempty"`
	FormParameters map[string]string `json:"form_parameters,omitempty"`
}

// emitLink prints the URL on stdout so it can be piped, and optionally hands it
// to the platform's browser launcher.
func (a *App) emitLink(url string, launch bool, result openResult) error {
	if a.out.Format() == output.FormatJSON {
		if err := a.out.Object(result); err != nil {
			return err
		}
	} else {
		// The URL goes to stdout, not through Msg: the whole point is that it
		// can be piped into xargs or a clipboard tool.
		if _, err := a.stdout.Write([]byte(url + "\n")); err != nil {
			return err
		}
	}

	if !launch {
		return nil
	}
	if err := openInBrowser(url); err != nil {
		a.out.Warn("could not open a browser: %v", err)
	}
	return nil
}

// openInBrowser hands a URL to the platform's opener.
func openInBrowser(url string) error {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "rundll32"
	default:
		cmd = "xdg-open"
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return exec.Command(path, "url.dll,FileProtocolHandler", url).Start()
	}
	return exec.Command(path, url).Start()
}

func newAppsCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "apps",
		Short: "List the file types that can be opened in a browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()

			types, err := app.client.ListApps(ctx)
			if err != nil {
				return err
			}
			sort.Slice(types, func(i, j int) bool { return types[i].MimeType < types[j].MimeType })

			table := output.Table{Headers: []string{"EXTENSION", "MIME TYPE", "DEFAULT", "APPLICATIONS"}, Items: types}
			for _, t := range types {
				table.Rows = append(table.Rows, []string{
					orDash(t.Extension), t.MimeType, orDash(t.Default), joinOrDash(t.Apps),
				})
			}
			return app.out.Render(table)
		},
	}
}
