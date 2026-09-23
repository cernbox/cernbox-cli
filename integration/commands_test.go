//go:build integration

package integration_test

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// This file exists to make the coverage claim checkable: every command in the
// tree is exercised against the dev environment somewhere in this package, and
// what is not covered here is covered in integration_test.go, features_test.go
// or ocm_test.go.

// ── authentication ───────────────────────────────────────────────────────────

func TestLoginAndLogout(t *testing.T) {
	e := setup(t)

	out := e.mustRun("login")
	if !strings.Contains(out, username) {
		t.Errorf("login should report who it signed in as:\n%s", out)
	}

	// The session is cached, so a following command needs no credentials of
	// its own — which is the point of caching it.
	e.mustRun("whoami")

	e.mustRun("logout")

	// After logout the chain re-authenticates from the environment, so the
	// command still works; what must not happen is a stale session being used.
	e.mustRun("whoami")
}

func TestLoginReportsTheProvider(t *testing.T) {
	e := setup(t)

	var result struct {
		User     string `json:"user"`
		Provider string `json:"provider"`
	}
	e.runJSON(&result, "login")
	if result.User != username {
		t.Errorf("user = %q", result.User)
	}
	if result.Provider != "basic" {
		t.Errorf("provider = %q, want the method that was forced", result.Provider)
	}
}

// ── listing and metadata ─────────────────────────────────────────────────────

func TestLsLongAndRecursive(t *testing.T) {
	e := setup(t)
	e.mustRun("mkdir", e.remotePath("sub"))
	e.mustRun("put", e.writeLocal("a.txt", []byte("alpha")), e.remotePath("a.txt"))
	e.mustRun("put", e.writeLocal("b.txt", []byte("beta")), e.remotePath("sub/b.txt"))

	// The long listing is shaped like ls: a total line, then one line per
	// entry starting with a mode. No labelled headers.
	long := e.mustRun("ls", "-l", e.remote)
	if !strings.HasPrefix(long, "total ") {
		t.Errorf("ls -l should open with a total line:\n%s", long)
	}
	for _, unwanted := range []string{"TYPE", "MODIFIED"} {
		if strings.Contains(long, unwanted) {
			t.Errorf("ls -l should not print a %q header:\n%s", unwanted, long)
		}
	}
	if !strings.Contains(long, "a.txt") || !strings.Contains(long, "sub") {
		t.Errorf("ls -l is missing entries:\n%s", long)
	}
	for _, line := range strings.Split(strings.TrimSpace(long), "\n")[1:] {
		if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "d") {
			t.Errorf("every entry should start with a mode, got %q", line)
		}
	}

	// A directory is marked only under -F, as ls does.
	if strings.Contains(e.mustRun("ls", e.remote), "sub/") {
		t.Error("ls should not classify directories without -F")
	}
	if !strings.Contains(e.mustRun("ls", "-F", e.remote), "sub/") {
		t.Error("ls -F should mark the directory")
	}

	recursive := e.mustRun("ls", "-R", e.remote)
	if !strings.Contains(recursive, "sub/b.txt") {
		t.Errorf("ls -R did not descend:\n%s", recursive)
	}
}

// TestLsSortFlags: -t, -S and -r are the orderings ls offers, and a listing
// people read in a terminal is worth little without them.
func TestLsSortFlags(t *testing.T) {
	e := setup(t)
	// Written oldest-first and with distinct sizes, so name, time and size
	// each give a different order.
	e.mustRun("put", e.writeLocal("big.txt", []byte("0123456789")), e.remotePath("big.txt"))
	e.mustRun("put", e.writeLocal("small.txt", []byte("x")), e.remotePath("small.txt"))

	first := func(out string) string {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) == 0 {
			return ""
		}
		return lines[0]
	}

	if got := first(e.mustRun("ls", e.remote)); !strings.Contains(got, "big.txt") {
		t.Errorf("default order should be by name, first line was %q", got)
	}
	if got := first(e.mustRun("ls", "-S", e.remote)); !strings.Contains(got, "big.txt") {
		t.Errorf("-S should put the largest first, got %q", got)
	}
	if got := first(e.mustRun("ls", "-r", e.remote)); !strings.Contains(got, "small.txt") {
		t.Errorf("-r should reverse the order, got %q", got)
	}
	// -t is checked as a property rather than by naming a file: two uploads a
	// moment apart can share a timestamp, and then the order is decided by the
	// tiebreak, not by time.
	var byTime []struct {
		Name     string    `json:"name"`
		Modified time.Time `json:"modified"`
	}
	e.runJSON(&byTime, "ls", "-t", e.remote)
	for i := 1; i < len(byTime); i++ {
		if byTime[i].Modified.After(byTime[i-1].Modified) {
			t.Errorf("-t is not newest-first: %s (%s) came after %s (%s)",
				byTime[i].Name, byTime[i].Modified, byTime[i-1].Name, byTime[i-1].Modified)
		}
	}
}

// TestLsSizesMatchLs: bytes by default, 1.2K under -h — the way ls does it,
// so a habit formed on ls carries over.
func TestLsSizesMatchLs(t *testing.T) {
	e := setup(t)
	// 2048 bytes is 2.0K, so the two renderings cannot be confused.
	e.mustRun("put", e.writeLocal("a.txt", make([]byte, 2048)), e.remotePath("a.txt"))

	plain := e.mustRun("ls", "-l", e.remote)
	if !strings.Contains(plain, "2048") {
		t.Errorf("a long listing should print exact bytes by default:\n%s", plain)
	}

	human := e.mustRun("ls", "-lh", e.remote)
	if !strings.Contains(human, "2.0K") {
		t.Errorf("-h should print a human-readable size:\n%s", human)
	}
	if strings.Contains(human, "2048") {
		t.Errorf("-h should replace the byte count, not add to it:\n%s", human)
	}
}

// TestLsHelpIsStillReachable: -h is human-readable on ls, so --help has to
// carry the weight, and -h must not be swallowed as an unknown flag.
func TestLsHelpIsStillReachable(t *testing.T) {
	e := setup(t)
	out, _, code := e.run("ls", "--help")
	if code != 0 {
		t.Fatalf("ls --help exited %d", code)
	}
	if !strings.Contains(out, "human-readable") {
		t.Errorf("ls --help should document the flags:\n%s", out)
	}
}

func TestLsCSV(t *testing.T) {
	e := setup(t)
	e.mustRun("put", e.writeLocal("a.txt", []byte("alpha")), e.remotePath("a.txt"))

	out := e.mustRun("--output", "csv", "ls", e.remote)
	records, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\n%s", err, out)
	}
	if len(records) < 2 {
		t.Fatalf("CSV has %d rows, want a header and at least one entry:\n%s", len(records), out)
	}
	if records[0][0] != "NAME" {
		t.Errorf("first CSV column = %q, want the header", records[0][0])
	}
}

// TestLsStreamingJSON: each entry is its own line, so a very large listing can
// be consumed without holding it all in memory.
func TestLsStreamingJSON(t *testing.T) {
	e := setup(t)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		e.mustRun("put", e.writeLocal(name, []byte(name)), e.remotePath(name))
	}

	out := e.mustRun("--output", "json", "--stream", "ls", e.remote)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d NDJSON lines, want 3:\n%s", len(lines), out)
	}
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("line %q is not valid JSON: %v", line, err)
		}
	}
}

// TestFindFallsBackToWalking: reva's search-files REPORT handler is a stub that
// answers 501, so the client-side walk is the path that actually runs here.
func TestFindFallsBackToWalking(t *testing.T) {
	e := setup(t)
	e.mustRun("mkdir", e.remotePath("deep"))
	e.mustRun("put", e.writeLocal("needle.txt", []byte("x")), e.remotePath("deep/needle.txt"))
	e.mustRun("put", e.writeLocal("other.txt", []byte("y")), e.remotePath("deep/other.txt"))

	var results []entry
	e.runJSON(&results, "find", e.remote, "--name", "needle")

	for _, r := range results {
		if strings.Contains(r.Name, "needle") {
			if len(results) != 1 {
				t.Errorf("find returned %d results, want only the match: %+v", len(results), results)
			}
			return
		}
	}
	t.Errorf("find did not return the matching file: %+v", results)
}

func TestDu(t *testing.T) {
	e := setup(t)
	e.mustRun("mkdir", e.remotePath("sub"))
	e.mustRun("put", e.writeLocal("a.txt", []byte("0123456789")), e.remotePath("sub/a.txt"))

	var usage []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	e.runJSON(&usage, "du", e.remote)
	if len(usage) == 0 {
		t.Fatal("du reported nothing")
	}

	withDepth := e.mustRun("du", "--depth", "1", e.remote)
	if !strings.Contains(withDepth, "sub") {
		t.Errorf("du --depth 1 should list the child:\n%s", withDepth)
	}
}

func TestTouchCreatesAnEmptyFile(t *testing.T) {
	e := setup(t)
	e.mustRun("touch", e.remotePath("empty.txt"))

	info := e.stat(e.remotePath("empty.txt"))
	if info.Size != 0 {
		t.Errorf("size = %d, want 0", info.Size)
	}
	if info.IsDir {
		t.Error("touch created a directory")
	}
}

// ── spaces ───────────────────────────────────────────────────────────────────

func TestSpaceInfo(t *testing.T) {
	e := setup(t)

	var space struct {
		Type string `json:"type"`
		Path string `json:"path"`
		ID   string `json:"id"`
	}
	e.runJSON(&space, "space", "info", "home")
	if space.Path != homeRoot {
		t.Errorf("space info path = %q, want %q", space.Path, homeRoot)
	}
	if space.Type != "personal" {
		t.Errorf("space type = %q", space.Type)
	}
}

func TestSpaceListFilteredByType(t *testing.T) {
	e := setup(t)

	var spaces []struct {
		Type string `json:"type"`
	}
	e.runJSON(&spaces, "space", "list", "--type", "personal")
	for _, s := range spaces {
		if s.Type != "personal" {
			t.Errorf("--type personal returned a %q space", s.Type)
		}
	}
}

// ── sharing, from both sides ─────────────────────────────────────────────────

// TestShareIsVisibleToTheRecipient checks the half that only a second account
// can check: that the share actually arrives.
func TestShareIsVisibleToTheRecipient(t *testing.T) {
	e := setup(t)
	target := e.remotePath("for-marie.txt")
	e.mustRun("put", e.writeLocal("for-marie.txt", []byte("hello marie")), target)

	e.mustRun("share", "create", target, "--with", otherUser, "--role", "viewer")

	var received []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Role     string `json:"role"`
		SharedBy struct {
			DisplayName string `json:"display_name"`
		} `json:"shared_by"`
	}
	e.runJSONAs(e.other(), &received, "share", "received")

	for _, s := range received {
		if strings.Contains(s.Name, "for-marie.txt") {
			if s.Role != "viewer" {
				t.Errorf("the recipient sees role %q, want viewer", s.Role)
			}
			return
		}
	}
	t.Errorf("the share did not reach %s: %+v", otherUser, received)
}

func TestShareWithGroup(t *testing.T) {
	e := setup(t)
	target := e.remotePath("for-group.txt")
	e.mustRun("put", e.writeLocal("for-group.txt", []byte("hello group")), target)

	// physics-lovers is a group both einstein and marie belong to in the dev
	// fixtures.
	e.mustRun("share", "create", target, "--with-group", "physics-lovers", "--role", "viewer")

	var perms []struct {
		GrantedTo struct {
			Type string `json:"type"`
		} `json:"granted_to"`
	}
	e.runJSON(&perms, "share", "list", target)

	for _, p := range perms {
		if p.GrantedTo.Type == "group" {
			return
		}
	}
	t.Errorf("no group share in the listing: %+v", perms)
}

func TestShareListWithoutPathShowsEverythingShared(t *testing.T) {
	e := setup(t)
	target := e.remotePath("shared-by-me.txt")
	e.mustRun("put", e.writeLocal("shared-by-me.txt", []byte("x")), target)
	e.mustRun("share", "create", target, "--with", otherUser, "--role", "viewer")

	var items []struct {
		Name string `json:"name"`
	}
	e.runJSON(&items, "share", "list")
	for _, it := range items {
		if strings.Contains(it.Name, "shared-by-me.txt") {
			return
		}
	}
	t.Errorf("the share is not in the shared-by-me listing: %+v", items)
}

func TestShareExpiryIsAccepted(t *testing.T) {
	e := setup(t)
	target := e.remotePath("expiring.txt")
	e.mustRun("put", e.writeLocal("expiring.txt", []byte("x")), target)

	e.mustRun("share", "create", target, "--with", otherUser, "--role", "viewer", "--expiry", "2030-12-31")
}

func TestShareRejectsAnUnknownRole(t *testing.T) {
	e := setup(t)
	target := e.remotePath("a.txt")
	e.mustRun("put", e.writeLocal("a.txt", []byte("x")), target)

	_, _, code := e.run("share", "create", target, "--with", otherUser, "--role", "overlord")
	if code != 2 {
		t.Errorf("exit code = %d, want 2 for an unknown role", code)
	}
}

// ── public links ─────────────────────────────────────────────────────────────

func TestLinkWithExpiryAndName(t *testing.T) {
	e := setup(t)
	target := e.remotePath("linked.txt")
	e.mustRun("put", e.writeLocal("linked.txt", []byte("x")), target)

	var created struct {
		ID   string `json:"id"`
		Link *struct {
			URL  string `json:"url"`
			Type string `json:"type"`
		} `json:"link"`
	}
	e.runJSONOne(&created, "link", "create", target,
		"--role", "viewer", "--name", "review copy", "--expiry", "2030-12-31")

	if created.Link == nil || created.Link.URL == "" {
		t.Fatalf("no link URL: %+v", created)
	}
	if created.Link.Type != "view" {
		t.Errorf("link type = %q, want view", created.Link.Type)
	}

	e.mustRun("link", "remove", target, created.ID)
}

func TestLinkRejectsAnUnknownRole(t *testing.T) {
	e := setup(t)
	target := e.remotePath("a.txt")
	e.mustRun("put", e.writeLocal("a.txt", []byte("x")), target)

	_, _, code := e.run("link", "create", target, "--role", "overlord")
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// TestLinkPasswordNeedsATerminal: the password is read from a prompt rather
// than taken as a flag, so that it never lands in shell history or ps output.
// With no terminal there is nothing to read, and the command must say so
// instead of hanging.
func TestLinkPasswordNeedsATerminal(t *testing.T) {
	e := setup(t)
	target := e.remotePath("a.txt")
	e.mustRun("put", e.writeLocal("a.txt", []byte("x")), target)

	var created struct {
		ID string `json:"id"`
	}
	e.runJSONOne(&created, "link", "create", target)

	_, _, code := e.run("link", "password", target, created.ID)
	if code == 0 {
		t.Error("setting a link password with no terminal should fail rather than hang")
	}

	// Clearing it needs no prompt, so that one must work.
	e.mustRun("link", "password", target, created.ID, "--clear")
}

// ── applications ─────────────────────────────────────────────────────────────

// requireApps skips when no application can actually open anything.
//
// The dev environment runs reva's demo app provider, which advertises an empty
// mime type list; the provider then intersects that with the configured one and
// ends up handling nothing, and the catalogue is filtered down to nothing
// because reva drops any mime type with no apps. So these skip against the dev
// environment and run against a deployment with a real provider such as
// Collabora. The provider is configured all the same: the moment one is added,
// these start exercising it.
// requireApps skips when nothing on this deployment can open a file.
//
// It reads the JSON rather than the table: the table always prints its header,
// so testing the text for content only ever detected that the command ran.
//
// The skip is expected against the dev environment and is not a gap in the CLI.
// reva ships two app drivers: wopi, which needs a real WOPI server such as
// Collabora, and demo, which reports an empty mime type list. The app provider
// advertises the intersection of what its driver reports with what it was
// configured for, and an intersection with nothing is nothing — so no
// configuration can make the demo provider open anything.
func requireApps(t *testing.T, e *env) {
	t.Helper()

	stdout, stderr, code := e.run("--output", "json", "apps")
	if code != 0 {
		t.Skipf("no application provider on this deployment: %s", stderr)
	}

	var types []struct {
		MimeType string   `json:"mime_type"`
		Apps     []string `json:"apps"`
	}
	if err := json.Unmarshal([]byte(stdout), &types); err != nil {
		t.Fatalf("apps produced invalid JSON: %v\n%s", err, stdout)
	}
	for _, ty := range types {
		if len(ty.Apps) > 0 {
			return
		}
	}
	t.Skipf("no application can open anything here: %d mime types, none with an app "+
		"(reva's demo provider reports no mime types, and wopi needs a real WOPI server)", len(types))
}

func TestAppsListsMimeTypes(t *testing.T) {
	e := setup(t)
	requireApps(t, e)

	var types []struct {
		MimeType  string   `json:"mime_type"`
		Extension string   `json:"extension"`
		Apps      []string `json:"apps"`
	}
	e.runJSON(&types, "apps")
	if len(types) == 0 {
		t.Fatal("no mime types were listed")
	}

	for _, ty := range types {
		if ty.MimeType == "text/plain" {
			if len(ty.Apps) == 0 {
				t.Error("text/plain is listed but no application can open it")
			}
			return
		}
	}
	t.Errorf("text/plain is not in the catalogue: %+v", types)
}

func TestOpenReturnsAnApplicationLink(t *testing.T) {
	e := setup(t)
	requireApps(t, e)

	target := e.remotePath("openable.txt")
	e.mustRun("put", e.writeLocal("openable.txt", []byte("hello")), target)

	var result struct {
		URL  string `json:"url"`
		Kind string `json:"kind"`
	}
	e.runJSON(&result, "open", target)
	if result.URL == "" {
		t.Errorf("open returned no URL: %+v", result)
	}
	if result.Kind != "app" {
		t.Errorf("kind = %q, want app", result.Kind)
	}
}

func TestOpenWithExplicitApp(t *testing.T) {
	e := setup(t)
	requireApps(t, e)

	target := e.remotePath("openable.txt")
	e.mustRun("put", e.writeLocal("openable.txt", []byte("hello")), target)

	// The dev environment runs the demo provider, which advertises itself under
	// this name.
	stdout, stderr, code := e.run("open", "--app", "demo-app", target)
	if code != 0 {
		t.Fatalf("open --app failed (%d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

func TestOpenWriteMode(t *testing.T) {
	e := setup(t)
	requireApps(t, e)

	target := e.remotePath("editable.txt")
	e.mustRun("put", e.writeLocal("editable.txt", []byte("hello")), target)

	e.mustRun("open", "--view-mode", "write", target)
}

func TestOpenUnknownPathIsNotFound(t *testing.T) {
	e := setup(t)
	_, _, code := e.run("open", e.remotePath("ghost.txt"))
	if code != 5 {
		t.Errorf("exit code = %d, want 5", code)
	}
}

// ── app tokens ───────────────────────────────────────────────────────────────

func TestTokenList(t *testing.T) {
	e := setup(t)

	// A fresh account has no app tokens, so an empty list is the expected
	// answer; what matters is that the endpoint answers at all.
	var tokens []struct {
		ID string `json:"id"`
	}
	e.runJSON(&tokens, "token", "list")
}

func TestTokenRevokeUnknownIDIsIdempotent(t *testing.T) {
	e := setup(t)
	// The server treats revocation as idempotent, so revoking an id that is
	// already gone must not be an error.
	e.mustRun("token", "revoke", "no-such-token")
}

func TestTokenCreateExplainsWhereToGo(t *testing.T) {
	e := setup(t)
	_, stderr, code := e.run("token", "create", "--all")
	if code == 0 {
		t.Fatal("token create should not claim to have created one")
	}
	if !strings.Contains(stderr, "web interface") {
		t.Errorf("the error should say where to create a token:\n%s", stderr)
	}
}

// ── shell integration ────────────────────────────────────────────────────────

func TestCompletionScripts(t *testing.T) {
	e := setup(t)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out := e.mustRun("completion", shell)
		if len(out) < 100 {
			t.Errorf("%s completion looks empty:\n%s", shell, out)
		}
		if !strings.Contains(out, "cernbox") {
			t.Errorf("%s completion does not mention the command", shell)
		}
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	e := setup(t)
	if _, _, code := e.run("completion", "csh"); code == 0 {
		t.Error("an unknown shell should be rejected")
	}
}

func TestStatusJSON(t *testing.T) {
	e := setup(t)

	var status struct {
		Endpoint      string   `json:"endpoint"`
		User          string   `json:"user"`
		Provider      string   `json:"provider"`
		Available     []string `json:"available_methods"`
		TokenCache    string   `json:"token_cache"`
		ServerVersion string   `json:"server_version"`
		Tus           bool     `json:"tus_supported"`
	}
	e.runJSON(&status, "status")

	if status.User != username {
		t.Errorf("user = %q", status.User)
	}
	if status.Endpoint != endpoint {
		t.Errorf("endpoint = %q", status.Endpoint)
	}
	if status.ServerVersion == "" {
		t.Error("status did not report the server version")
	}
	if status.TokenCache == "" {
		t.Error("status did not report where the token cache lives")
	}
}

// TestDuLooksLikeDu: a size, a tab, a path — no header, and a directory
// reported after everything it contains.
func TestDuLooksLikeDu(t *testing.T) {
	e := setup(t)
	e.mustRun("mkdir", "-p", e.remotePath("tree/inner"))
	e.mustRun("put", e.writeLocal("a.txt", make([]byte, 2048)), e.remotePath("tree/inner/a.txt"))

	out := e.mustRun("du", "-d", "2", e.remotePath("tree"))
	if strings.Contains(out, "SIZE") || strings.Contains(out, "PATH") {
		t.Errorf("du should print no header:\n%s", out)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, l := range lines {
		if !strings.Contains(l, "\t") {
			t.Errorf("each line should be size<TAB>path, got %q", l)
		}
	}
	// The argument itself is reported last, after what it contains.
	if last := lines[len(lines)-1]; !strings.HasSuffix(last, "/tree") {
		t.Errorf("the argument should come last, got %q", last)
	}
	if first := lines[0]; !strings.Contains(first, "/tree/inner") {
		t.Errorf("the deepest entry should come first, got %q", first)
	}
}

// TestDuSummarizeAndHuman: -s collapses to one line per argument, and -h is
// human-readable as in du(1) — bytes otherwise.
func TestDuSummarizeAndHuman(t *testing.T) {
	e := setup(t)
	e.mustRun("mkdir", "-p", e.remotePath("tree/inner"))
	e.mustRun("put", e.writeLocal("a.txt", make([]byte, 2048)), e.remotePath("tree/inner/a.txt"))

	summary := strings.TrimSpace(e.mustRun("du", "-s", "-d", "2", e.remotePath("tree")))
	if strings.Contains(summary, "\n") {
		t.Errorf("-s should report one line per argument, got:\n%s", summary)
	}

	// EOS accounts a directory's recursive size asynchronously, so the total
	// is 0 for a moment after the upload. Wait for it rather than race it.
	e.waitFor("du to account the upload", func() bool {
		return strings.Contains(e.mustRun("du", "-s", e.remotePath("tree")), "2048")
	})

	if h := e.mustRun("du", "-hs", e.remotePath("tree")); !strings.Contains(h, "2.0K") {
		t.Errorf("-h should print a human-readable size:\n%s", h)
	}
}
