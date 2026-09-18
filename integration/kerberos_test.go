//go:build integration

package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Native Kerberos: the ticket goes straight to CERNBox, with no identity
// provider in between. These tests want a real realm, so they run against the
// kdc container in the dev environment and skip when there is none.

const (
	// krb5ConfPath is where the kdc container publishes the client
	// configuration, on a volume the test runner also sees when it runs in
	// compose. Outside compose the realm is reached through the mapped port.
	krb5Realm = "TEST.CERN.CH"
	// kerberosPrincipal is the account the dev realm knows, with the same
	// password the rest of the environment uses.
	kerberosPrincipal = "einstein"
	kerberosPassword  = "relativity"
)

// krb5Env is one test's isolated Kerberos world: its own configuration file and
// its own credential cache, so a ticket obtained here cannot leak into another
// test and a developer's own ticket cannot leak into this one.
type krb5Env struct {
	confPath   string
	ccachePath string
}

// requireKerberos skips unless a realm is reachable and the client tools are
// present. Both are part of the dev environment, so a skip here means the
// environment is not fully up rather than that the feature is absent.
//
// In CI the environment is always fully up, and a silent skip would read as a
// pass and quietly stop covering authentication altogether. CERNBOX_REQUIRE_KERBEROS
// turns every skip below into a failure, so the suite cannot go dark by accident.
func requireKerberos(t *testing.T) *krb5Env {
	t.Helper()

	give := t.Skipf
	if os.Getenv("CERNBOX_REQUIRE_KERBEROS") != "" {
		give = t.Fatalf
	}

	if _, err := exec.LookPath("kinit"); err != nil {
		give("kinit is not installed; the Kerberos tests need the krb5 client tools")
		return nil
	}

	dir := t.TempDir()
	conf := filepath.Join(dir, "krb5.conf")

	// The KDC is reached on the port compose maps out. Writing the file rather
	// than reusing the container's means the test does not depend on running
	// inside compose.
	body := "[libdefaults]\n" +
		"    default_realm = " + krb5Realm + "\n" +
		"    dns_lookup_realm = false\n" +
		"    dns_lookup_kdc = false\n" +
		"    rdns = false\n" +
		"    udp_preference_limit = 1\n\n" +
		"[realms]\n" +
		"    " + krb5Realm + " = {\n" +
		"        kdc = 127.0.0.1:8088\n" +
		"    }\n\n" +
		"[domain_realm]\n" +
		"    localhost = " + krb5Realm + "\n"
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	env := &krb5Env{confPath: conf, ccachePath: filepath.Join(dir, "ccache")}
	if err := env.kinit(); err != nil {
		give("no Kerberos realm reachable at 127.0.0.1:8088: %v", err)
		return nil
	}
	return env
}

// kinit obtains a ticket for the test principal.
func (k *krb5Env) kinit() error {
	cmd := exec.Command("kinit", kerberosPrincipal+"@"+krb5Realm)
	cmd.Env = append(os.Environ(),
		"KRB5_CONFIG="+k.confPath,
		"KRB5CCNAME=FILE:"+k.ccachePath,
	)
	cmd.Stdin = strings.NewReader(kerberosPassword + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return &kinitError{err: err, output: string(out)}
	}
	return nil
}

// destroy removes the ticket, for the tests that check what happens without one.
func (k *krb5Env) destroy() error {
	return os.Remove(k.ccachePath)
}

type kinitError struct {
	err    error
	output string
}

func (e *kinitError) Error() string { return e.err.Error() + ": " + strings.TrimSpace(e.output) }

// runKerberos executes the CLI authenticating with the ticket in this world,
// and with nothing else: no password, no cached token, no explicit token.
func (e *env) runKerberos(k *krb5Env, args ...string) (stdout, stderr string, code int) {
	e.t.Helper()

	full := append([]string{
		"--endpoint", endpoint,
		"--method", "kerberos",
	}, args...)

	c := exec.Command(binary, full...)
	c.Env = append(os.Environ(),
		"KRB5_CONFIG="+k.confPath,
		"KRB5CCNAME=FILE:"+k.ccachePath,
		"CERNBOX_TOKEN_CACHE="+filepath.Join(e.cacheDir, "krb-tokens"),
		"CERNBOX_CONFIG="+filepath.Join(e.cacheDir, "absent.yaml"),
		// Everything else is cleared, so a pass here can only be Kerberos.
		"CERNBOX_TOKEN=",
		"CERNBOX_APP_TOKEN=",
		"CERNBOX_USERNAME=",
		"CERNBOX_PASSWORD=",
		"SSL_CERT_FILE="+devCACert(),
	)

	var outBuf, errBuf strings.Builder
	c.Stdout = &outBuf
	c.Stderr = &errBuf

	err := c.Run()
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

func (e *env) mustRunKerberos(k *krb5Env, args ...string) string {
	e.t.Helper()
	stdout, stderr, code := e.runKerberos(k, args...)
	if code != 0 {
		e.t.Fatalf("cernbox %v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
	}
	return stdout
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestKerberosAuthenticatesNatively is the whole point: a ticket, presented
// straight to CERNBox, with no identity provider in the path.
func TestKerberosAuthenticatesNatively(t *testing.T) {
	e := setup(t)
	k := requireKerberos(t)

	out := e.mustRunKerberos(k, "whoami")
	if !strings.Contains(out, kerberosPrincipal) {
		t.Errorf("whoami did not report the principal's account:\n%s", out)
	}
}

// TestKerberosStatusReportsTheProvider checks the session really came from the
// ticket rather than from something left in the environment.
func TestKerberosStatusReportsTheProvider(t *testing.T) {
	e := setup(t)
	k := requireKerberos(t)

	out := e.mustRunKerberos(k, "status")
	if !strings.Contains(out, "kerberos") {
		t.Errorf("status should name kerberos as the provider:\n%s", out)
	}
	if !strings.Contains(out, kerberosPrincipal) {
		t.Errorf("status should name the authenticated user:\n%s", out)
	}
}

// TestKerberosSessionDoesRealWork: authenticating is not the point on its own,
// the session it yields has to be usable.
func TestKerberosSessionDoesRealWork(t *testing.T) {
	e := setup(t)
	k := requireKerberos(t)

	local := e.writeLocal("krb.txt", []byte("authenticated with a ticket"))
	e.mustRunKerberos(k, "put", local, e.remotePath("krb.txt"))

	if out := e.mustRunKerberos(k, "cat", e.remotePath("krb.txt")); out != "authenticated with a ticket" {
		t.Errorf("cat returned %q", out)
	}
	e.mustRunKerberos(k, "ls", e.remote)
}

// TestKerberosWithoutATicketFails: with the ccache gone there is nothing to
// authenticate with, and the CLI has to say so rather than hang or half-work.
func TestKerberosWithoutATicketFails(t *testing.T) {
	e := setup(t)
	k := requireKerberos(t)

	if err := k.destroy(); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := e.runKerberos(k, "whoami")
	if code == 0 {
		t.Fatal("authentication should fail with no ticket")
	}
	if !strings.Contains(stderr, "kinit") {
		t.Errorf("the error should tell the user to run kinit:\n%s", stderr)
	}
}

// TestKerberosWrongServicePrincipal covers the failure a deployment actually
// hits: the endpoint is an alias, the client asks the KDC for a ticket for a
// name that is not in the keytab, and the raw GSS error says nothing useful.
func TestKerberosWrongServicePrincipal(t *testing.T) {
	e := setup(t)
	k := requireKerberos(t)

	c := exec.Command(binary,
		"--endpoint", endpoint,
		"--method", "kerberos",
		"--kerberos-spn", "HTTP/not-in-the-keytab.invalid",
		"whoami")
	c.Env = append(os.Environ(),
		"KRB5_CONFIG="+k.confPath,
		"KRB5CCNAME=FILE:"+k.ccachePath,
		"CERNBOX_TOKEN_CACHE="+filepath.Join(e.cacheDir, "spn-tokens"),
		"CERNBOX_CONFIG="+filepath.Join(e.cacheDir, "absent.yaml"),
		"CERNBOX_TOKEN=", "CERNBOX_APP_TOKEN=", "CERNBOX_USERNAME=", "CERNBOX_PASSWORD=",
		"SSL_CERT_FILE="+devCACert(),
	)
	out, err := c.CombinedOutput()
	if err == nil {
		t.Fatalf("a ticket for the wrong service should not authenticate:\n%s", out)
	}
	if !strings.Contains(string(out), "rdns") {
		t.Errorf("the error should point at the DNS alias problem:\n%s", out)
	}
}
