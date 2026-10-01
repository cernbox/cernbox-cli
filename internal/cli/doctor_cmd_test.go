package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// healthyBox is a box with the home space in place, plus an editor that is
// certainly installed: the editor check looks at PATH, and a test that assumed
// the machine running it has vi would pass or fail for reasons that have
// nothing to do with the doctor.
func healthyBox(t *testing.T) *testBox {
	t.Helper()
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv("EDITOR", exe)
	return box
}

// doctorLine returns the report line for one check.
func doctorLine(t *testing.T, out, name string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
			return line
		}
	}
	t.Fatalf("no %q line in:\n%s", name, out)
	return ""
}

func TestDoctorReportsAHealthyInstallation(t *testing.T) {
	box := healthyBox(t)

	stdout, _, err := run(t, box, "doctor")
	if err != nil {
		t.Fatalf("doctor on a working installation failed: %v\n%s", err, stdout)
	}

	// Every check has to appear. A doctor that quietly stops reporting one is
	// worse than one that fails, because the missing line reads as a pass.
	for _, name := range []string{
		"config", "endpoint", "clock", "credentials", "identity",
		"spaces", "write", "quota", "trash", "outbox", "editor",
	} {
		doctorLine(t, stdout, name)
	}
	if strings.Contains(stdout, "✗") {
		t.Errorf("a working installation reported a problem:\n%s", stdout)
	}
	if !strings.Contains(stdout, "No problems found") {
		t.Errorf("no summary in:\n%s", stdout)
	}
}

// TestDoctorWarnsAboutAConfigFileThatIsNotThere covers the failure the config
// check exists for: CERNBOX_CONFIG pointing at nothing is ignored in silence,
// so a setting somebody is certain they changed has no effect and nothing says
// why. The test harness sets that variable to a file it never creates, which is
// exactly the situation.
func TestDoctorWarnsAboutAConfigFileThatIsNotThere(t *testing.T) {
	box := healthyBox(t)

	stdout, _, err := run(t, box, "doctor")
	if err != nil {
		t.Fatalf("a missing CERNBOX_CONFIG must warn, not fail: %v", err)
	}
	if !strings.Contains(stdout, "CERNBOX_CONFIG points at") {
		t.Errorf("nothing said the variable was ignored:\n%s", stdout)
	}
	if !strings.Contains(stdout, "⚠ config") {
		t.Errorf("expected a warning on the config line:\n%s", stdout)
	}
}

func TestDoctorFailsWhenTheConfigFileCannotBeRead(t *testing.T) {
	box := healthyBox(t)

	stdout, _, err := run(t, box, "--config", "/nonexistent/cernbox.yaml", "doctor")
	if err == nil {
		t.Fatal("a --config that is not there has to be a problem")
	}
	if !strings.Contains(stdout, "✗ config") {
		t.Errorf("expected the config check to fail:\n%s", stdout)
	}
	// And nothing else may be reported: every check below would have run
	// against the defaults rather than against what the user asked for.
	if strings.Contains(stdout, "endpoint") {
		t.Errorf("checks ran on after the configuration failed:\n%s", stdout)
	}
}

func TestDoctorStopsAtAnEndpointItCannotReach(t *testing.T) {
	box := healthyBox(t)
	box.ts.Close()

	stdout, _, err := run(t, box, "doctor")
	if err == nil {
		t.Fatal("an unreachable endpoint has to be a problem")
	}
	if !strings.Contains(stdout, "✗ endpoint") {
		t.Errorf("expected the endpoint check to fail:\n%s", stdout)
	}
	// One failure, not eleven. The point of stopping is that the reader does not
	// have to work out which of a wall of failures is the cause.
	if n := strings.Count(stdout, "✗"); n != 1 {
		t.Errorf("got %d failures, want the one that caused them:\n%s", n, stdout)
	}
	for _, name := range []string{"credentials", "write", "quota"} {
		if strings.Contains(stdout, name) {
			t.Errorf("%s was checked against a server that does not answer:\n%s", name, stdout)
		}
	}
}

func TestDoctorFailsOnAClockThatWouldBreakKerberos(t *testing.T) {
	box := healthyBox(t)
	box.clockSkew = -20 * time.Minute

	stdout, _, err := run(t, box, "doctor")
	if err == nil {
		t.Fatal("twenty minutes of drift has to be a problem")
	}
	line := doctorLine(t, stdout, "clock")
	if !strings.Contains(line, "✗") || !strings.Contains(line, "ahead of the server") {
		t.Errorf("clock line does not say which way the drift goes: %q", line)
	}
	if !strings.Contains(stdout, "Kerberos") {
		t.Errorf("nothing explained why a clock matters:\n%s", stdout)
	}
}

func TestDoctorWarnsOnASmallClockDrift(t *testing.T) {
	box := healthyBox(t)
	box.clockSkew = 90 * time.Second

	stdout, _, err := run(t, box, "doctor")
	if err != nil {
		t.Fatalf("a drift short of the Kerberos limit must warn, not fail: %v", err)
	}
	if line := doctorLine(t, stdout, "clock"); !strings.Contains(line, "⚠") {
		t.Errorf("expected a warning: %q", line)
	}
}

// TestDoctorSaysNothingAboutASecondOfDrift guards the one number this check
// cannot measure: an HTTP Date carries whole seconds, so a perfectly set
// machine reads as up to a second out, and reporting that would be a warning
// that never goes away and never means anything.
func TestDoctorSaysNothingAboutASecondOfDrift(t *testing.T) {
	box := healthyBox(t)
	box.clockSkew = 900 * time.Millisecond

	stdout, _, err := run(t, box, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	line := doctorLine(t, stdout, "clock")
	if !strings.Contains(line, "in step with the server") {
		t.Errorf("sub-second drift was reported as drift: %q", line)
	}
}

// TestDoctorFailsWhenTheStorageRefusesWrites is the check nothing else can
// stand in for. Everything here is healthy — reachable, signed in, quota
// half empty — and a write still fails, which is what a storage short of
// headroom does.
func TestDoctorFailsWhenTheStorageRefusesWrites(t *testing.T) {
	box := healthyBox(t)
	box.failPut = true

	stdout, _, err := run(t, box, "doctor")
	if err == nil {
		t.Fatal("a storage that refuses writes has to be a problem")
	}
	if line := doctorLine(t, stdout, "write"); !strings.Contains(line, "✗") {
		t.Errorf("expected the write check to fail: %q", line)
	}
	if line := doctorLine(t, stdout, "quota"); strings.Contains(line, "✗") {
		t.Errorf("the quota is fine, and saying otherwise sends the reader the wrong way: %q", line)
	}
	// The checks after it still run: a refused write says nothing about the
	// trash or the outbox.
	doctorLine(t, stdout, "trash")
}

func TestDoctorRemovesTheFileItWrote(t *testing.T) {
	box := healthyBox(t)

	if _, _, err := run(t, box, "doctor"); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	box.mu.Lock()
	defer box.mu.Unlock()
	for p := range box.files {
		if strings.Contains(p, "cernbox-doctor") {
			t.Errorf("the probe was left behind at %s", p)
		}
	}
}

func TestDoctorWritesNothingWithSkipWrite(t *testing.T) {
	box := healthyBox(t)

	stdout, _, err := run(t, box, "doctor", "--skip-write")
	if err != nil {
		t.Fatalf("doctor --skip-write: %v", err)
	}
	box.mu.Lock()
	defer box.mu.Unlock()
	for _, req := range box.requests {
		if strings.HasPrefix(req, "PUT ") || strings.HasPrefix(req, "DELETE ") {
			t.Errorf("--skip-write still changed something: %s", req)
		}
	}
	if line := doctorLine(t, stdout, "write"); !strings.Contains(line, "--skip-write") {
		t.Errorf("the skipped check does not say why it was skipped: %q", line)
	}
}

func TestDoctorWarnsAboutADayTheTrashWillNotList(t *testing.T) {
	box := healthyBox(t)
	box.trashRefuseDay = time.Now().Format("2006-01-02")

	stdout, _, err := run(t, box, "doctor")
	if err != nil {
		t.Fatalf("a day that will not list must warn, not fail: %v", err)
	}
	line := doctorLine(t, stdout, "trash")
	if !strings.Contains(line, "⚠") || !strings.Contains(line, "could not be listed") {
		t.Errorf("expected a gap to be reported: %q", line)
	}
	if !strings.Contains(stdout, "--since") {
		t.Errorf("nothing said what to do about it:\n%s", stdout)
	}
}

func TestDoctorWarnsAboutAnEditorThatIsNotInstalled(t *testing.T) {
	box := healthyBox(t)
	t.Setenv("EDITOR", "definitely-not-an-installed-editor")

	stdout, _, err := run(t, box, "doctor")
	if err != nil {
		t.Fatalf("a missing editor must warn, not fail: %v", err)
	}
	line := doctorLine(t, stdout, "editor")
	if !strings.Contains(line, "⚠") || !strings.Contains(line, "not on PATH") {
		t.Errorf("expected the editor to be reported missing: %q", line)
	}
	if !strings.Contains(line, "EDITOR") {
		t.Errorf("the line does not say where the setting came from: %q", line)
	}
}

func TestDoctorJSONCountsWhatItFound(t *testing.T) {
	box := healthyBox(t)
	box.failPut = true

	stdout, _, err := run(t, box, "--output", "json", "doctor")
	if err == nil {
		t.Fatal("the exit code has to survive --output json")
	}

	var rep doctorReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout)
	}
	if rep.Problems != 1 {
		t.Errorf("problems = %d, want 1", rep.Problems)
	}
	if len(rep.Checks) == 0 {
		t.Fatal("no checks in the report")
	}
	for _, c := range rep.Checks {
		if c.Name == "write" {
			if c.Status != checkFail {
				t.Errorf("write status = %q, want %q", c.Status, checkFail)
			}
			if c.Remedy == "" {
				t.Error("a failing check with no remedy has only moved the confusion")
			}
		}
	}
}

// TestDoctorPrintsNoCredentials is the one hard constraint on this command: its
// output exists to be pasted into a support request.
func TestDoctorPrintsNoCredentials(t *testing.T) {
	box := healthyBox(t)
	t.Setenv("CERNBOX_PASSWORD", "hunter2")

	stdout, stderr, err := run(t, box, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	for _, secret := range []string{"test-token", "hunter2"} {
		if strings.Contains(stdout+stderr, secret) {
			t.Errorf("the report contains %q, which is not safe to paste:\n%s", secret, stdout)
		}
	}
}

func TestDoctorNamesEveryFailingCheckInTheError(t *testing.T) {
	box := healthyBox(t)
	box.failPut = true
	box.clockSkew = time.Hour

	_, _, err := run(t, box, "doctor")
	if err == nil {
		t.Fatal("two failures, and no error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "2 problems") || !strings.Contains(msg, "clock") ||
		!strings.Contains(msg, "write") {
		t.Errorf("error does not name what failed: %q", msg)
	}
}

func TestClockDetailSaysWhichWay(t *testing.T) {
	cases := []struct {
		skew time.Duration
		want string
	}{
		{0, "in step with the server"},
		{90 * time.Second, "1m30s ahead of the server"},
		{-90 * time.Second, "1m30s behind the server"},
	}
	for _, c := range cases {
		if got := clockDetail(c.skew); got != c.want {
			t.Errorf("clockDetail(%s) = %q, want %q", c.skew, got, c.want)
		}
	}
}

func TestPluralReadsLikeEnglish(t *testing.T) {
	if got := plural(1, "warning", "warnings"); got != "1 warning" {
		t.Errorf("got %q", got)
	}
	if got := plural(0, "warning", "warnings"); got != "0 warnings" {
		t.Errorf("got %q", got)
	}
}

// TestDoctorPurgesItsProbeFromTheRecycleBin is the behaviour a real run taught:
// deleting the probe is not enough, because a deletion goes to the bin, and a
// daily check would add a year of entries to the one place users already
// complain about.
func TestDoctorPurgesItsProbeFromTheRecycleBin(t *testing.T) {
	box := healthyBox(t)
	box.trashOnDelete = true

	if _, _, err := run(t, box, "doctor"); err != nil {
		t.Fatalf("doctor: %v", err)
	}

	box.mu.Lock()
	defer box.mu.Unlock()
	var left []string
	for key, e := range box.trash {
		if strings.Contains(e.name, "cernbox-doctor") {
			left = append(left, key+" "+e.name)
		}
	}
	if len(left) > 0 {
		t.Errorf("the probe is still in the recycle bin: %s", strings.Join(left, ", "))
	}
}

// TestWriteRemedyDoesNotBlameTheServerForAFullSpace guards the one place the
// write check could send somebody the wrong way: "not something you can fix
// from here" is the right answer for a storage out of headroom and the wrong
// one for a space that is simply full.
func TestWriteRemedyDoesNotBlameTheServerForAFullSpace(t *testing.T) {
	full := cberr.FromStatus(507, "upload", "/x", "")
	if got := writeRemedy(full); !strings.Contains(got, "quota") {
		t.Errorf("a full space should point at the quota, got %q", got)
	}
	denied := cberr.FromStatus(403, "upload", "/x", "")
	if got := writeRemedy(denied); !strings.Contains(got, "not allowed") {
		t.Errorf("a refusal should say so, got %q", got)
	}
	broken := cberr.FromStatus(500, "upload", "/x", "")
	if got := writeRemedy(broken); !strings.Contains(got, "cannot fix") &&
		!strings.Contains(got, "not something you can fix") {
		t.Errorf("an unexplained failure should say it is not the user's to fix, got %q", got)
	}
}
