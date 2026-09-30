package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// diffBox is a file with two earlier versions, newest last as the fake orders
// them.
func diffBox(t *testing.T) *testBox {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/report.md", "line one\nline two changed\nline three\n",
		versionEntry{key: "v-old", body: "line one\nline two\n"},
		versionEntry{key: "v-mid", body: "line one\nline two\nline three\n"},
	)
	return box
}

// TestVersionsDiffAgainstNow is the default: no version named, so the newest one
// is compared with the file as it is, which answers "what did I just change".
func TestVersionsDiffAgainstNow(t *testing.T) {
	stdout, _, err := run(t, diffBox(t), "versions", "diff", "/eos/user/e/einstein/report.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- report.md", "+++ report.md (now)", "@@ -", "-line two", "+line two changed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q:\n%s", want, stdout)
		}
	}
	// Unchanged lines are context, not changes.
	if strings.Contains(stdout, "-line one") || strings.Contains(stdout, "+line one") {
		t.Errorf("an unchanged line was marked as changed:\n%s", stdout)
	}
}

func TestVersionsDiffOneVersionAgainstNow(t *testing.T) {
	stdout, _, err := run(t, diffBox(t), "versions", "diff",
		"/eos/user/e/einstein/report.md", "v-old")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "(v-old)") || !strings.Contains(stdout, "(now)") {
		t.Errorf("both sides should be labelled:\n%s", stdout)
	}
	// v-old has no third line; now it does.
	if !strings.Contains(stdout, "+line three") {
		t.Errorf("the added line is missing:\n%s", stdout)
	}
}

func TestVersionsDiffTwoVersions(t *testing.T) {
	stdout, _, err := run(t, diffBox(t), "versions", "diff",
		"/eos/user/e/einstein/report.md", "v-old", "v-mid")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "(v-old)") || !strings.Contains(stdout, "(v-mid)") {
		t.Errorf("both versions should be labelled:\n%s", stdout)
	}
	// Neither side is the current file, so its change must not appear.
	if strings.Contains(stdout, "line two changed") {
		t.Errorf("the current file leaked into a diff of two versions:\n%s", stdout)
	}
}

func TestVersionsDiffIdenticalSaysSo(t *testing.T) {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/same.md", "unchanged\n",
		versionEntry{key: "v1", body: "unchanged\n"},
	)

	stdout, stderr, err := run(t, box, "versions", "diff", "/eos/user/e/einstein/same.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "@@") {
		t.Errorf("identical content produced a diff:\n%s", stdout)
	}
	if !strings.Contains(stderr, "identical") {
		t.Errorf("it should say they are the same:\n%s", stderr)
	}
}

// TestVersionsDiffRefusesBinary: a line diff of a PNG is noise, and saying so is
// more use than printing it.
func TestVersionsDiffRefusesBinary(t *testing.T) {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/shot.png", "PNG\x00\x01binary",
		versionEntry{key: "v1", body: "PNG\x00older"},
	)

	_, _, err := run(t, box, "versions", "diff", "/eos/user/e/einstein/shot.png")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error for a binary file", err)
	}
	if !strings.Contains(errLine(err), "not text") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

func TestVersionsDiffNoHistory(t *testing.T) {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/fresh.md", "only ever this\n")

	_, _, err := run(t, box, "versions", "diff", "/eos/user/e/einstein/fresh.md")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error when there is nothing to compare", err)
	}
}

func TestVersionsDiffRejectsADirectory(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents")

	_, _, err := run(t, box, "versions", "diff", "/eos/user/e/einstein/Documents")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error for a directory", err)
	}
}

// TestVersionsDiffComparesAgainstTheNewest pins the default on the most recent
// version, which the client guarantees by sorting the listing newest first. The
// server here returns them oldest first, so this also covers that sort still
// happening — picking the wrong version would produce a perfectly plausible diff
// of the wrong pair.
func TestVersionsDiffComparesAgainstTheNewest(t *testing.T) {
	box := newTestBox(t)
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	versionsFor(box, "/eos/user/e/einstein/ordered.md", "third\n",
		versionEntry{key: "v-oldest", body: "first\n", modified: older},
		versionEntry{key: "v-newest", body: "second\n", modified: newer},
	)

	stdout, _, err := run(t, box, "versions", "diff", "/eos/user/e/einstein/ordered.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "(v-newest)") {
		t.Errorf("the newest version was not chosen:\n%s", stdout)
	}
	if !strings.Contains(stdout, "-second") || !strings.Contains(stdout, "+third") {
		t.Errorf("wrong pair compared:\n%s", stdout)
	}
}

// TestVersionsDiffContext: -U bounds how much unchanged text is shown, which is
// what makes a diff of a long file readable.
func TestVersionsDiffContext(t *testing.T) {
	box := newTestBox(t)
	var old, now []string
	for i := range 30 {
		old = append(old, "line")
		now = append(now, "line")
		_ = i
	}
	now[15] = "changed"
	versionsFor(box, "/eos/user/e/einstein/long.md", strings.Join(now, "\n")+"\n",
		versionEntry{key: "v1", body: strings.Join(old, "\n") + "\n"},
	)

	stdout, _, err := run(t, box, "versions", "diff", "/eos/user/e/einstein/long.md", "-U", "1")
	if err != nil {
		t.Fatal(err)
	}
	// One change, one line of context either side, plus the two header lines and
	// the hunk header.
	if n := strings.Count(stdout, "\n"); n > 8 {
		t.Errorf("-U 1 printed %d lines, which is more context than asked for:\n%s", n, stdout)
	}
}

// TestVersionsDiffPlainHasNoColour: the output is data, so a pipe must not get
// escape sequences. The writer only colours a terminal anyway; --plain is the
// explicit way to be sure.
func TestVersionsDiffPlainHasNoColour(t *testing.T) {
	stdout, _, err := run(t, diffBox(t), "versions", "diff",
		"/eos/user/e/einstein/report.md", "--plain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "\033[") {
		t.Errorf("colour reached a pipe:\n%q", stdout)
	}
}
