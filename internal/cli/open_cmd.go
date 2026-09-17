package cli

import (
	"os/exec"
	"runtime"
	"sort"

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
		Short: "Print the link to open a file in the browser",
		Long: "Print the URL that opens a file in CERNBox.\n\n" +
			"By default this is an application link — the online editor for the file's\n" +
			"type. With --web it is the file's page in the CERNBox interface.\n\n" +
			"Nothing is launched unless you pass --launch. On lxplus there is usually\n" +
			"no browser to launch, and printing the link so you can paste it into your\n" +
			"own is more useful than an error about a missing display.",
		Example: "  cernbox open /eos/user/g/gdelmont/report.docx\n" +
			"  cernbox open --web /eos/user/g/gdelmont/Documents\n" +
			"  cernbox open --app Collabora --view-mode write /eos/user/g/gdelmont/notes.odt",
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

			mode := ""
			switch viewMode {
			case "", "read":
				mode = client.ViewModeRead
			case "write", "edit":
				mode = client.ViewModeWrite
			default:
				return cberr.Usagef("unknown view mode %q: want read or write", viewMode)
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
	cmd.Flags().StringVar(&viewMode, "view-mode", "read", "read or write")
	cmd.Flags().BoolVar(&web, "web", false, "print the CERNBox web interface link instead of an application link")
	cmd.Flags().BoolVar(&launch, "launch", false, "also open the link in a browser")
	return cmd
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
		Short: "List the file types this server can open in a web application",
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
