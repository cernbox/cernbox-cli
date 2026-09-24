// Package packaging_test exercises the artefacts that are shipped rather than
// compiled: here, the install script that cli.cernbox.cern.ch serves.
//
// An install script nobody has run is a liability. This one is the first thing a
// new user executes, it runs on whatever shell the machine has, and its failure
// mode is a half-installed binary or a confusing message — so it is driven end to
// end against a release served locally. Everything is covered except GitHub's
// own /releases/latest redirect.
package packaging_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testVersion = "9.9.9"

// fakeRelease serves one release: the archive for this machine, and the checksum
// list goreleaser publishes beside it.
func fakeRelease(t *testing.T, corruptChecksum bool) (baseURL, archiveName string) {
	t.Helper()

	archiveName = fmt.Sprintf("cernbox-cli_%s_%s_%s.tar.gz", testVersion, runtime.GOOS, runtime.GOARCH)

	// The archive holds the binary at its root, which is what goreleaser produces
	// with wrap_in_directory off. A shell script stands in for the binary: the
	// script only has to install something and run it once.
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	tw := tar.NewWriter(zw)
	binary := "#!/bin/sh\necho \"cernbox " + testVersion + "\"\n"
	for name, body := range map[string]string{
		"cernbox":   binary,
		"README.md": "readme\n",
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	archive := gz.Bytes()

	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])
	if corruptChecksum {
		hexSum = strings.Repeat("0", len(hexSum))
	}
	checksums := fmt.Sprintf("%s  %s\n", hexSum, archiveName)

	mux := http.NewServeMux()
	mux.HandleFunc("/"+archiveName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, archiveName
}

// runInstall runs the script the way "curl … | sh" does: piped into sh, with
// nothing inherited that it has not asked for.
func runInstall(t *testing.T, baseURL, installDir string) (string, error) {
	t.Helper()

	script, err := filepath.Abs("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh")
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CERNBOX_VERSION=v" + testVersion,
		"CERNBOX_BASE_URL=" + baseURL,
		"CERNBOX_INSTALL_DIR=" + installDir,
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallScript(t *testing.T) {
	base, _ := fakeRelease(t, false)
	dir := filepath.Join(t.TempDir(), "bin")

	out, err := runInstall(t, base, dir)
	if err != nil {
		t.Fatalf("the install script failed: %v\n%s", err, out)
	}

	for _, want := range []string{"Checksum verified", "Installed cernbox " + testVersion} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not say %q:\n%s", want, out)
		}
	}

	installed := filepath.Join(dir, "cernbox")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("nothing was installed: %v\n%s", err, out)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("what was installed is not executable: %v", info.Mode())
	}

	// And it runs, which is the only claim that matters.
	ran, err := exec.Command(installed).CombinedOutput()
	if err != nil {
		t.Fatalf("the installed binary does not run: %v\n%s", err, ran)
	}
	if !strings.Contains(string(ran), testVersion) {
		t.Errorf("the installed binary printed %q", ran)
	}

	// Nothing is left behind in the install directory but the binary: a failed
	// rename would leave cernbox.new sitting there.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "cernbox" {
		t.Errorf("the install directory holds %v, want only cernbox", entries)
	}
}

// TestInstallScriptRefusesABadChecksum: the download came over the network, and
// the release publishes the list precisely so it can be checked. Installing
// anyway would make the check decoration.
func TestInstallScriptRefusesABadChecksum(t *testing.T) {
	base, _ := fakeRelease(t, true)
	dir := filepath.Join(t.TempDir(), "bin")

	out, err := runInstall(t, base, dir)
	if err == nil {
		t.Fatalf("a mismatched checksum installed anyway:\n%s", out)
	}
	if !strings.Contains(out, "does not match its checksum") {
		t.Errorf("the failure does not say what was wrong:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "cernbox")); err == nil {
		t.Error("a refused install left a binary behind")
	}
}

// TestInstallScriptReportsAMissingRelease: the version somebody asks for may not
// exist, and the message has to say where to look rather than leaving them with a
// bare 404.
func TestInstallScriptReportsAMissingRelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	dir := filepath.Join(t.TempDir(), "bin")
	out, err := runInstall(t, srv.URL, dir)
	if err == nil {
		t.Fatalf("a missing release installed something:\n%s", out)
	}
	if !strings.Contains(out, "cannot download") || !strings.Contains(out, "releases") {
		t.Errorf("the failure does not say where to look:\n%s", out)
	}
}
