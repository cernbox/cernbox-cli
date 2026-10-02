// Command fakeoffice stands in for an office suite and the WOPI server in front
// of it, so that the dev environment has an application that can open files.
//
// reva ships two app drivers. demo advertises an empty mime type list, and the
// app provider offers the intersection of that with its configuration, so it
// can open nothing whatever it is configured for. wopi is what production runs,
// and needs two things from the outside: a /hosting/discovery document listing
// what the suite opens, fetched when revad starts, and the WOPI server's
// /wopi/iop/openinapp, which turns a file into an editor URL. This serves both,
// answering the way cs3org/wopiserver does: with an access token in the form
// parameters, so the session reva hands back is a POST, as Collabora's and
// MS365's are on CERNBox.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func main() {
	addr := flag.String("addr", ":8880", "address to listen on")
	public := flag.String("public-url", "http://fakeoffice:8880", "base of the editor URLs handed to users")
	probe := flag.Bool("probe", false, "check that a server is answering on addr and exit; the healthcheck, since the image has no shell")
	flag.Parse()

	if *probe {
		resp, err := http.Get("http://localhost" + *addr + "/hosting/discovery")
		if err != nil {
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	secret := os.Getenv("IOP_SECRET")
	if secret == "" {
		log.Fatal("IOP_SECRET is not set: it is the secret revad's wopi driver authenticates with")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /hosting/discovery", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, discovery(*public))
	})
	mux.HandleFunc("GET /wopi/iop/openinapp", func(w http.ResponseWriter, r *http.Request) {
		openInApp(w, r, secret)
	})

	log.Printf("fakeoffice listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// extensions are what the suite claims to open. They must match the mime types
// revad's app provider is configured for, which it intersects with these.
var extensions = []string{"txt", "md", "odt"}

// discovery is a WOPI discovery document. reva only reads actions in a net-zone
// whose name contains "external", and only the view, edit, editnew and
// embedview ones.
func discovery(public string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<wopi-discovery><net-zone name="external-http"><app name="FakeOffice">` + "\n")
	for _, ext := range extensions {
		for _, action := range []string{"view", "edit"} {
			fmt.Fprintf(&b, `<action name="%s" ext="%s" urlsrc="%s/%s?"/>`+"\n", action, ext, public, action)
		}
	}
	b.WriteString(`</app></net-zone></wopi-discovery>` + "\n")
	return b.String()
}

// openInApp answers reva's request for an editor session. Like the real WOPI
// server it rejects a request it could not act on, so a driver sending the
// wrong thing fails here rather than getting a URL that would not work.
func openInApp(w http.ResponseWriter, r *http.Request, secret string) {
	if r.Header.Get("Authorization") != "Bearer "+secret {
		http.Error(w, "Client not authorized", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("TokenHeader") == "" {
		http.Error(w, "Missing access token", http.StatusUnauthorized)
		return
	}

	q := r.URL.Query()
	for _, p := range []string{"fileid", "endpoint", "viewmode", "appname", "userid", "usertype"} {
		if q.Get(p) == "" {
			http.Error(w, "Missing argument "+p, http.StatusBadRequest)
			return
		}
	}

	// The WOPI server opens the edit URL only for a read-write session, and
	// the view one otherwise, so the path says which mode reached it.
	var target string
	switch q.Get("viewmode") {
	case "VIEW_MODE_READ_WRITE":
		target = q.Get("appurl")
	case "VIEW_MODE_READ_ONLY", "VIEW_MODE_VIEW_ONLY", "VIEW_MODE_PREVIEW":
		target = q.Get("appviewurl")
	default:
		http.Error(w, "Invalid viewmode "+q.Get("viewmode"), http.StatusBadRequest)
		return
	}
	if target == "" {
		target = q.Get("appurl")
	}
	u, err := url.Parse(target)
	if err != nil || target == "" {
		http.Error(w, "Invalid app URL", http.StatusBadRequest)
		return
	}
	uq := u.Query()
	uq.Set("WOPISrc", "http://fakeoffice:8880/wopi/files/"+url.PathEscape(q.Get("endpoint")+"!"+q.Get("fileid")))
	u.RawQuery = uq.Encode()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"app-url": u.String(),
		"form-parameters": map[string]string{
			"access_token": "fakeoffice-access-token",
		},
	}); err != nil {
		log.Printf("writing the openinapp response: %v", err)
	}
}
