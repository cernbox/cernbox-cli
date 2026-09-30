package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
)

// ── the predicates themselves ────────────────────────────────────────────────

func TestFindFilterName(t *testing.T) {
	sub, err := findOptions{name: "report"}.filter()
	if err != nil {
		t.Fatal(err)
	}
	if sub.glob {
		t.Error("plain text should not be treated as a glob")
	}
	// Text matches anywhere in the name, which is what this always did.
	if !sub.matches(client.ResourceInfo{Name: "final-report-2026.pdf"}) {
		t.Error("substring match failed")
	}
	if sub.matches(client.ResourceInfo{Name: "notes.txt"}) {
		t.Error("substring matched something it should not")
	}

	// A pattern with a metacharacter is a glob, and then it has to match the
	// whole name — "*.root" is not a substring test.
	glob, err := findOptions{name: "*.root"}.filter()
	if err != nil {
		t.Fatal(err)
	}
	if !glob.glob {
		t.Fatal("*.root should be a glob")
	}
	if !glob.matches(client.ResourceInfo{Name: "run123.root"}) {
		t.Error("glob did not match run123.root")
	}
	if glob.matches(client.ResourceInfo{Name: "run123.root.bak"}) {
		t.Error("a glob must match the whole name")
	}

	// Case-insensitive both ways, as the substring search always was.
	if !glob.matches(client.ResourceInfo{Name: "RUN123.ROOT"}) {
		t.Error("glob should ignore case")
	}
}

func TestFindFilterSize(t *testing.T) {
	cases := []struct {
		expr  string
		size  int64
		match bool
	}{
		{"+1K", 2048, true},
		{"+1K", 1024, false}, // + is strictly larger, as in find(1)
		{"-1K", 512, true},
		{"-1K", 1024, false},
		{"1K", 1024, true},
		{"1K", 1025, false},
	}
	for _, c := range cases {
		f, err := findOptions{size: c.expr}.filter()
		if err != nil {
			t.Fatalf("--size %s: %v", c.expr, err)
		}
		if got := f.matches(client.ResourceInfo{Size: c.size}); got != c.match {
			t.Errorf("--size %s against %d = %v, want %v", c.expr, c.size, got, c.match)
		}
	}
}

func TestFindFilterTime(t *testing.T) {
	now := time.Now()
	recent := client.ResourceInfo{Name: "x", Modified: now.Add(-2 * time.Hour)}
	ancient := client.ResourceInfo{Name: "x", Modified: now.AddDate(0, 0, -300)}

	newer, err := findOptions{newer: "7d"}.filter()
	if err != nil {
		t.Fatal(err)
	}
	if !newer.matches(recent) || newer.matches(ancient) {
		t.Error("--newer 7d picked the wrong entries")
	}

	older, err := findOptions{older: "7d"}.filter()
	if err != nil {
		t.Fatal(err)
	}
	if older.matches(recent) || !older.matches(ancient) {
		t.Error("--older 7d picked the wrong entries")
	}

	// An absolute date works too.
	dated, err := findOptions{newer: "2026-01-01"}.filter()
	if err != nil {
		t.Fatal(err)
	}
	if !dated.matches(recent) {
		t.Error("--newer with a date failed")
	}

	// An entry the server gave no time for cannot satisfy a test about time,
	// which is not the same as being from 1970.
	if newer.matches(client.ResourceInfo{Name: "x"}) || older.matches(client.ResourceInfo{Name: "x"}) {
		t.Error("an entry with no modification time should match neither")
	}
}

func TestFindFilterType(t *testing.T) {
	dir := client.ResourceInfo{Name: "d", IsDir: true}
	file := client.ResourceInfo{Name: "f"}

	f, _ := findOptions{kind: "f"}.filter()
	if f.matches(dir) || !f.matches(file) {
		t.Error("--type f matched a directory")
	}
	d, _ := findOptions{kind: "d"}.filter()
	if !d.matches(dir) || d.matches(file) {
		t.Error("--type d matched a file")
	}
}

// TestFindFilterCombines: the tests are an AND, so a file has to satisfy all of
// them.
func TestFindFilterCombines(t *testing.T) {
	f, err := findOptions{name: "*.root", size: "+1K", kind: "f"}.filter()
	if err != nil {
		t.Fatal(err)
	}
	if !f.matches(client.ResourceInfo{Name: "a.root", Size: 4096}) {
		t.Error("a file satisfying every test did not match")
	}
	for _, no := range []client.ResourceInfo{
		{Name: "a.txt", Size: 4096},               // wrong name
		{Name: "a.root", Size: 10},                // too small
		{Name: "a.root", Size: 4096, IsDir: true}, // wrong kind
	} {
		if f.matches(no) {
			t.Errorf("%+v should not have matched", no)
		}
	}
}

func TestFindFilterRejectsNonsense(t *testing.T) {
	for _, o := range []findOptions{
		{},                  // nothing to search for
		{size: "+banana"},   //
		{size: "1X"},        //
		{newer: "soon"},     //
		{older: "whenever"}, //
		{kind: "socket"},    //
		{name: "[unclosed"}, // an unmatchable glob
	} {
		if _, err := o.filter(); cberr.ExitCode(err) != cberr.ExitUsage {
			t.Errorf("%+v was accepted (%v)", o, err)
		}
	}
}

// TestFindFilterNameOnly decides whether the server can be asked. It can match a
// plain name and nothing else, so anything more has to be a walk — filtering the
// server's answer after it applied its own limit would drop results it never
// sent and report a short answer as a complete one.
func TestFindFilterNameOnly(t *testing.T) {
	plain, _ := findOptions{name: "report"}.filter()
	if !plain.nameOnly() {
		t.Error("a plain name is something the server can do")
	}
	for _, o := range []findOptions{
		{name: "*.root"},
		{name: "report", size: "+1K"},
		{name: "report", kind: "f"},
		{name: "report", newer: "7d"},
		{size: "+1K"},
	} {
		f, err := o.filter()
		if err != nil {
			t.Fatal(err)
		}
		if f.nameOnly() {
			t.Errorf("%+v cannot be left to the server", o)
		}
	}
}

// ── through the command ──────────────────────────────────────────────────────

func findBox(t *testing.T) *testBox {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/data/raw")
	box.putFile("/eos/user/e/einstein/data/raw/run1.root", strings.Repeat("x", 5000))
	box.putFile("/eos/user/e/einstein/data/raw/run2.root", strings.Repeat("x", 10))
	box.putFile("/eos/user/e/einstein/data/notes.txt", strings.Repeat("x", 100))
	return box
}

func TestFindBySize(t *testing.T) {
	stdout, _, err := run(t, findBox(t), "find", "/eos/user/e/einstein/data", "--size", "+1K")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "run1.root") {
		t.Errorf("the 5000-byte file is missing:\n%s", stdout)
	}
	if strings.Contains(stdout, "run2.root") || strings.Contains(stdout, "notes.txt") {
		t.Errorf("something under 1K was included:\n%s", stdout)
	}
}

func TestFindByGlob(t *testing.T) {
	stdout, _, err := run(t, findBox(t), "find", "/eos/user/e/einstein/data", "--name", "*.root")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "run1.root") || !strings.Contains(stdout, "run2.root") {
		t.Errorf("the .root files are missing:\n%s", stdout)
	}
	if strings.Contains(stdout, "notes.txt") {
		t.Errorf("the glob matched a .txt:\n%s", stdout)
	}
}

func TestFindByType(t *testing.T) {
	stdout, _, err := run(t, findBox(t), "find", "/eos/user/e/einstein/data", "--type", "d")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "/data/raw") {
		t.Errorf("the directory is missing:\n%s", stdout)
	}
	if strings.Contains(stdout, ".root") || strings.Contains(stdout, ".txt") {
		t.Errorf("--type d returned files:\n%s", stdout)
	}
}

// TestFindPrint0 is for piping into xargs -0, so it must be paths and NULs and
// nothing else.
func TestFindPrint0(t *testing.T) {
	stdout, _, err := run(t, findBox(t), "find", "/eos/user/e/einstein/data",
		"--name", "*.root", "--print0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "TYPE") || strings.Contains(stdout, "SIZE") {
		t.Errorf("--print0 wrote table headers:\n%q", stdout)
	}
	parts := strings.Split(strings.TrimSuffix(stdout, "\x00"), "\x00")
	if len(parts) != 2 {
		t.Fatalf("got %d NUL-separated paths, want 2: %q", len(parts), stdout)
	}
	for _, p := range parts {
		if !strings.HasPrefix(p, "/eos/") || strings.Contains(p, "\n") {
			t.Errorf("not a bare path: %q", p)
		}
	}
}

func TestFindPrint0RefusesMachineOutput(t *testing.T) {
	_, _, err := run(t, findBox(t), "--output", "json", "find",
		"/eos/user/e/einstein/data", "--name", "*.root", "--print0")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

func TestFindRequiresSomethingToSearchFor(t *testing.T) {
	_, _, err := run(t, findBox(t), "find", "/eos/user/e/einstein/data")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

// TestFindStillWorksWithOnlyAName keeps the behaviour this command already had.
func TestFindStillWorksWithOnlyAName(t *testing.T) {
	stdout, _, err := run(t, findBox(t), "find", "/eos/user/e/einstein/data", "--name", "run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "run1.root") || !strings.Contains(stdout, "run2.root") {
		t.Errorf("the substring search stopped working:\n%s", stdout)
	}
}
