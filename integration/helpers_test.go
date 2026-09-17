//go:build integration

// Package integration drives the built cernbox binary against the dev
// environment (revad on top of EOS), exercising the paths that a fake HTTP
// server cannot: real WebDAV semantics, real TUS uploads, real EOS behaviour.
//
// Start the environment with "make dev-up" before running these.
package integration_test

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	endpoint = "https://localhost"
	username = "einstein"
	password = "relativity"
	// homeRoot matches the dev revad's home_layout for the test user.
	homeRoot = "/eos/user/e/einstein"

	// A second local user, so shares can be checked from the receiving side.
	otherUser     = "marie"
	otherPassword = "radioactivity"

	// The federation partner: a second provider, so the OCM commands have a
	// real far end rather than only an error path.
	partnerEndpoint = "https://localhost:8081"
	partnerDomain   = "revad-partner"
	partnerUser     = "alice"
	partnerPassword = "wonderland"
)

// binary is the path to the cernbox binary built by TestMain.
var binary string

func TestMain(m *testing.M) {
	if !reachable() {
		fmt.Fprintln(os.Stderr, "===> dev environment not reachable — start it with 'make dev-up'")
		os.Exit(1)
	}

	dir, err := os.MkdirTemp("", "cernbox-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MkdirTemp: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	binary = filepath.Join(dir, "cernbox")
	build := exec.Command("go", "build", "-o", binary, "./cmd/cernbox")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building cernbox: %v\n%s", err, out)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func reachable() bool {
	return respondsOK(endpoint + "/status.php")
}

// partnerReachable reports whether the federation partner is running. The OCM
// tests skip rather than fail without it, so the suite still says something
// useful against a deployment that has no partner.
func partnerReachable() bool {
	return respondsOK(partnerEndpoint + "/status.php")
}

func respondsOK(url string) bool {
	c := &http.Client{Timeout: 3 * time.Second, Transport: devTransport()}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// devTransport trusts the dev certificate authority, which signs the
// certificates the dev instances serve. The test process needs this for its own
// probes; the CLI gets the same CA through SSL_CERT_FILE.
func devTransport() *http.Transport {
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(devCACert())
	if err == nil {
		pool.AppendCertsFromPEM(pem)
	}
	return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
}

// repoRoot returns the repository root. Tests run with the working directory
// set to integration/, so the root is one level up.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Dir(dir)
}

// env is one test's isolated world: a unique remote directory, a private token
// cache, and a local scratch directory.
type env struct {
	t        *testing.T
	remote   string // remote test directory, e.g. /eos/user/e/einstein/it-ab12cd34
	localDir string
	cacheDir string
}

func setup(t *testing.T) *env {
	t.Helper()

	e := &env{
		t:        t,
		remote:   path.Join(homeRoot, "it-"+randHex()),
		localDir: t.TempDir(),
		cacheDir: t.TempDir(),
	}

	e.mustRun("mkdir", "-p", e.remote)
	t.Cleanup(func() {
		// Best effort: a leftover directory would make the next run noisier but
		// not wrong.
		e.run("rm", "-r", "-f", e.remote)
	})
	return e
}

// account identifies who the CLI runs as, and against which server.
type account struct {
	endpoint string
	user     string
	password string
	// cacheKey keeps each account's token cache separate, so switching user
	// within a test cannot reuse the previous identity's session.
	cacheKey string
}

func (e *env) self() account {
	return account{endpoint: endpoint, user: username, password: password, cacheKey: "self"}
}

func (e *env) other() account {
	return account{endpoint: endpoint, user: otherUser, password: otherPassword, cacheKey: "other"}
}

func (e *env) partner() account {
	return account{
		endpoint: partnerEndpoint, user: partnerUser,
		password: partnerPassword, cacheKey: "partner",
	}
}

// cmd builds an exec.Cmd for the CLI with this test's isolated environment.
func (e *env) cmd(args ...string) *exec.Cmd {
	return e.cmdAs(e.self(), args...)
}

// cmdAs builds an exec.Cmd running as the given account.
func (e *env) cmdAs(a account, args ...string) *exec.Cmd {
	full := append([]string{
		"--endpoint", a.endpoint,
		"--method", "basic",
	}, args...)

	c := exec.Command(binary, full...)
	c.Env = append(os.Environ(),
		"CERNBOX_USERNAME="+a.user,
		"CERNBOX_PASSWORD="+a.password,
		"CERNBOX_TOKEN_CACHE="+filepath.Join(e.cacheDir, "tokens-"+a.cacheKey),
		// Point the config at a file that does not exist so a developer's own
		// configuration cannot leak into the test.
		"CERNBOX_CONFIG="+filepath.Join(e.cacheDir, "absent.yaml"),
		"CERNBOX_TOKEN=",
		"CERNBOX_APP_TOKEN=",
		// The dev instances serve TLS with a certificate from the dev CA, so
		// the CLI is pointed at that CA rather than run with --insecure: the
		// tests should exercise the same certificate verification a real
		// install does.
		"SSL_CERT_FILE="+devCACert(),
	)
	return c
}

// devCACert locates the dev certificate authority, which signs the certificates
// the dev instances serve.
func devCACert() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filepath.Dir(thisFile)), "dev", "pki", "ca.crt")
}

// run executes the CLI and returns stdout, stderr and the exit code.
func (e *env) run(args ...string) (stdout, stderr string, code int) {
	e.t.Helper()
	return e.runAs(e.self(), args...)
}

// runAs executes the CLI as the given account.
func (e *env) runAs(a account, args ...string) (stdout, stderr string, code int) {
	e.t.Helper()

	c := e.cmdAs(a, args...)
	var outBuf, errBuf strings.Builder
	c.Stdout = &outBuf
	c.Stderr = &errBuf

	err := c.Run()
	code = 0
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			code = exitErr.ExitCode()
		} else {
			e.t.Fatalf("running %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// mustRun executes the CLI and fails the test on a non-zero exit.
func (e *env) mustRun(args ...string) string {
	e.t.Helper()
	return e.mustRunAs(e.self(), args...)
}

// mustRunAs executes the CLI as the given account and fails on a non-zero exit.
func (e *env) mustRunAs(a account, args ...string) string {
	e.t.Helper()
	stdout, stderr, code := e.runAs(a, args...)
	if code != 0 {
		e.t.Fatalf("cernbox (%s@%s) %v exited %d\nstdout:\n%s\nstderr:\n%s",
			a.user, a.endpoint, args, code, stdout, stderr)
	}
	return stdout
}

// runJSON executes the CLI with --output json and decodes the result.
func (e *env) runJSON(v any, args ...string) {
	e.t.Helper()
	e.runJSONAs(e.self(), v, args...)
}

// runJSONAs executes the CLI as the given account with --output json.
func (e *env) runJSONAs(a account, v any, args ...string) {
	e.t.Helper()
	out := e.mustRunAs(a, append([]string{"--output", "json"}, args...)...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		e.t.Fatalf("cernbox %v produced invalid JSON: %v\n%s", args, err, out)
	}
}

// runJSONOne decodes a command that produced exactly one item.
//
// --output json always emits the item list, even for a command that creates a
// single thing, so that a script can treat every command's output the same way.
// A test that wants the one item it just made would otherwise have to declare a
// slice and index it.
func (e *env) runJSONOne(v any, args ...string) {
	e.t.Helper()
	e.runJSONOneAs(e.self(), v, args...)
}

func (e *env) runJSONOneAs(a account, v any, args ...string) {
	e.t.Helper()
	out := e.mustRunAs(a, append([]string{"--output", "json"}, args...)...)

	var items []json.RawMessage
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		e.t.Fatalf("cernbox %v produced invalid JSON: %v\n%s", args, err, out)
	}
	if len(items) != 1 {
		e.t.Fatalf("cernbox %v produced %d items, want exactly 1\n%s", args, len(items), out)
	}
	if err := json.Unmarshal(items[0], v); err != nil {
		e.t.Fatalf("cernbox %v item did not decode: %v\n%s", args, err, out)
	}
}

// ── local helpers ────────────────────────────────────────────────────────────

func (e *env) writeLocal(name string, body []byte) string {
	e.t.Helper()
	p := filepath.Join(e.localDir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) readLocal(name string) []byte {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.localDir, name))
	if err != nil {
		e.t.Fatalf("reading %s: %v", name, err)
	}
	return b
}

func (e *env) localPath(name string) string {
	return filepath.Join(e.localDir, name)
}

// remotePath returns a path inside this test's remote directory.
func (e *env) remotePath(rel string) string {
	return path.Join(e.remote, rel)
}

// ── assertions ───────────────────────────────────────────────────────────────

// entry mirrors the JSON shape of a listing entry.
type entry struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	IsDir    bool      `json:"is_dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	ETag     string    `json:"etag"`
	ID       string    `json:"id"`
}

func (e *env) list(remote string) []entry {
	e.t.Helper()
	var entries []entry
	e.runJSON(&entries, "ls", remote)
	return entries
}

func (e *env) stat(remote string) entry {
	e.t.Helper()
	var info entry
	e.runJSON(&info, "stat", remote)
	return info
}

func (e *env) names(remote string) []string {
	e.t.Helper()
	var out []string
	for _, it := range e.list(remote) {
		out = append(out, it.Name)
	}
	return out
}

func (e *env) requireNames(remote string, want ...string) {
	e.t.Helper()
	got := e.names(remote)
	if len(got) != len(want) {
		e.t.Fatalf("%s contains %v, want %v", remote, got, want)
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			e.t.Fatalf("%s contains %v, want %v", remote, got, want)
		}
	}
}

// ── misc ─────────────────────────────────────────────────────────────────────

func randHex() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}
