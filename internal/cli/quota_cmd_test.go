package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

func TestQuotaDefaultsToTheUserSpace(t *testing.T) {
	stdout, _, err := run(t, newTestBox(t), "quota")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"home", "personal", "Quota", "Used", "Remaining"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q:\n%s", want, stdout)
		}
	}
}

// TestQuotaWorksForAProjectSpace: a project's quota is its own, and a space with
// no quota set has to read as unlimited rather than as zero.
func TestQuotaWorksForAProjectSpace(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/project/c/cernbox")

	stdout, _, err := run(t, box, "quota", "project/cernbox")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "project/cernbox") || !strings.Contains(stdout, "project") {
		t.Errorf("the project space is not described:\n%s", stdout)
	}
	if !strings.Contains(stdout, "unlimited") {
		t.Errorf("a space with no quota should read as unlimited:\n%s", stdout)
	}
}

func TestQuotaAllListsEverySpace(t *testing.T) {
	stdout, _, err := run(t, newTestBox(t), "quota", "--all")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"home", "project/cernbox", "USE%"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q:\n%s", want, stdout)
		}
	}
	// No quota means no percentage to report, rather than a misleading 0%.
	if !strings.Contains(stdout, "-") {
		t.Errorf("a space without a quota needs a dash, not a number:\n%s", stdout)
	}
}

// TestQuotaVersionsSplitsFilesFromVersions is the question the command exists to
// answer: a quota larger than anything you can find.
func TestQuotaVersionsSplitsFilesFromVersions(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/kept.bin", strings.Repeat("x", 1000))
	box.hidden["/eos/user/e/einstein"] = 3000

	stdout, stderr, err := run(t, box, "quota", "--versions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Files") || !strings.Contains(stdout, "Versions") {
		t.Fatalf("the split is missing:\n%s", stdout)
	}
	// 1000 bytes of listable content and 3000 that no listing shows, rendered the
	// way every other size is. The exact figures are asserted over JSON below.
	if !strings.Contains(stdout, "1000") || !strings.Contains(stdout, "2.9K") {
		t.Errorf("want files 1000 and versions 2.9K:\n%s", stdout)
	}
	// And it says what can be done about them, since versions cannot be deleted.
	if !strings.Contains(stderr, "cannot be deleted") {
		t.Errorf("the note about versions is missing:\n%s", stderr)
	}
}

// TestQuotaReportsWhatTheTreeDoesNotExplain: whether a recycle bin shares the
// space's quota node is a deployment's choice, so the report reconciles instead
// of assuming. Measured on the dev instance the trash is charged elsewhere; a
// deployment where it is not would show up here rather than silently not add up.
func TestQuotaReportsWhatTheTreeDoesNotExplain(t *testing.T) {
	box := newTestBox(t)
	// The fake reports 524288 used, far more than the tree holds.
	box.putFile("/eos/user/e/einstein/small.bin", "x")

	stdout, stderr, err := run(t, box, "quota")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Outside the tree") {
		t.Errorf("the unexplained remainder is not reported:\n%s", stdout)
	}
	if !strings.Contains(stderr, "recycle bin") {
		t.Errorf("the remainder should say what usually causes it:\n%s", stderr)
	}
}

func TestQuotaRejectsAllWithASpace(t *testing.T) {
	_, _, err := run(t, newTestBox(t), "quota", "--all", "project/cernbox")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

// TestQuotaRejectsAllWithVersions: a full walk per space is a long wait nobody
// asked for by writing --all.
func TestQuotaRejectsAllWithVersions(t *testing.T) {
	_, _, err := run(t, newTestBox(t), "quota", "--all", "--versions")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

func TestQuotaUnknownSpace(t *testing.T) {
	_, _, err := run(t, newTestBox(t), "quota", "project/nope")
	if err == nil {
		t.Error("an unknown space should fail")
	}
}

func TestQuotaJSONCarriesTheNumbers(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/kept.bin", strings.Repeat("x", 1000))
	box.hidden["/eos/user/e/einstein"] = 3000

	stdout, _, err := run(t, box, "--output", "json", "quota", "--versions")
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Alias    string `json:"alias"`
		Used     int64  `json:"quota_used"`
		Files    int64  `json:"files"`
		Versions int64  `json:"versions"`
	}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout)
	}
	if rep.Alias != "home" || rep.Files != 1000 || rep.Versions != 3000 {
		t.Errorf("report = %+v", rep)
	}
}
