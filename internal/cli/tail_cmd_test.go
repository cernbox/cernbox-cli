package cli

import (
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

func logBox(t *testing.T, lines int) (*testBox, string) {
	t.Helper()
	box := newTestBox(t)
	var sb strings.Builder
	for i := 1; i <= lines; i++ {
		sb.WriteString("line ")
		sb.WriteString(strings.Repeat("x", 0))
		sb.WriteString(itoa(i))
		sb.WriteString("\n")
	}
	p := "/eos/user/e/einstein/job.log"
	box.putFile(p, sb.String())
	return box, p
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestTailShowsTheLastTenLinesByDefault(t *testing.T) {
	box, p := logBox(t, 25)

	stdout, _, err := run(t, box, "tail", p)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(got) != 10 {
		t.Fatalf("got %d lines, want 10:\n%s", len(got), stdout)
	}
	if got[0] != "line 16" || got[9] != "line 25" {
		t.Errorf("got %q..%q, want line 16..line 25", got[0], got[9])
	}
}

func TestTailHonoursTheLineCount(t *testing.T) {
	box, p := logBox(t, 25)

	stdout, _, err := run(t, box, "tail", "-n", "3", p)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "line 23\nline 24\nline 25\n" {
		t.Errorf("got %q", stdout)
	}
}

// TestTailShowsEverythingWhenThereIsLessThanAsked: the window search has to
// stop at the start of the file rather than looking for lines that are not
// there.
func TestTailShowsEverythingWhenThereIsLessThanAsked(t *testing.T) {
	box, p := logBox(t, 3)

	stdout, _, err := run(t, box, "tail", "-n", "100", p)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "line 1\nline 2\nline 3\n" {
		t.Errorf("got %q", stdout)
	}
}

// TestTailCrossesTheWindowBoundary is what the doubling is for: the last lines
// of a file bigger than the first guess must still be found.
func TestTailCrossesTheWindowBoundary(t *testing.T) {
	box := newTestBox(t)
	p := "/eos/user/e/einstein/big.log"
	var sb strings.Builder
	// Each line is 1 KiB, so ten of them are further back than the 8 KiB first
	// window reaches.
	for i := 1; i <= 40; i++ {
		sb.WriteString(strings.Repeat("x", 1020))
		sb.WriteString(" ")
		sb.WriteString(itoa(i))
		sb.WriteString("\n")
	}
	box.putFile(p, sb.String())

	stdout, _, err := run(t, box, "tail", "-n", "12", p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 12 {
		t.Fatalf("got %d lines, want 12", len(lines))
	}
	if !strings.HasSuffix(lines[0], " 29") || !strings.HasSuffix(lines[11], " 40") {
		t.Errorf("got lines %q..%q, want 29..40",
			lines[0][len(lines[0])-3:], lines[11][len(lines[11])-3:])
	}
}

func TestTailWithoutFollowDoesNotPoll(t *testing.T) {
	box, p := logBox(t, 5)

	if _, _, err := run(t, box, "tail", p); err != nil {
		t.Fatal(err)
	}
	box.mu.Lock()
	defer box.mu.Unlock()
	var propfinds int
	for _, req := range box.requests {
		if strings.HasPrefix(req, "PROPFIND ") {
			propfinds++
		}
	}
	// One to refuse a directory, one to size the file. Anything more is a poll
	// that nobody asked for.
	if propfinds > 3 {
		t.Errorf("made %d PROPFINDs for a single tail: %v", propfinds, box.requests)
	}
}

func TestTailRefusesADirectory(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents")

	_, _, err := run(t, box, "tail", "/eos/user/e/einstein/Documents")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("got %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("cat(1) says 'Is a directory', and so should this: %v", err)
	}
}

// TestTailHeadsEachFileWhenThereAreSeveral follows tail's own rule: a header
// only when there is more than one file to tell apart.
func TestTailHeadsEachFileWhenThereAreSeveral(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/a.log", "from a\n")
	box.putFile("/eos/user/e/einstein/b.log", "from b\n")

	stdout, _, err := run(t, box, "tail",
		"/eos/user/e/einstein/a.log", "/eos/user/e/einstein/b.log")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "==> /eos/user/e/einstein/a.log <==") ||
		!strings.Contains(stdout, "==> /eos/user/e/einstein/b.log <==") {
		t.Errorf("no headers in:\n%s", stdout)
	}

	single, _, err := run(t, box, "tail", "/eos/user/e/einstein/a.log")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(single, "==>") {
		t.Errorf("one file needs no header:\n%s", single)
	}
}

func TestTailQuietDropsTheHeaders(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/a.log", "from a\n")
	box.putFile("/eos/user/e/einstein/b.log", "from b\n")

	stdout, _, err := run(t, box, "-q", "tail",
		"/eos/user/e/einstein/a.log", "/eos/user/e/einstein/b.log")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "==>") {
		t.Errorf("--quiet should drop the headers:\n%s", stdout)
	}
}

// TestTailFollowPrintsWhatIsAppended drives the follow loop. The file grows in
// answer to the client's own request rather than on a timer, so the test does
// not depend on how fast anything runs.
func TestTailFollowPrintsWhatIsAppended(t *testing.T) {
	box, p := logBox(t, 2)

	// Grown only once the whole startup has read the file, which takes two GETs:
	// one to find where the last lines begin and one to print them. Changing it
	// earlier would be changing it mid-read, and the test would be about that
	// instead.
	var grown bool
	box.afterRequest = func(b *testBox) {
		if grown || countGets(b) < 2 {
			return
		}
		b.files[p] += "appended later\n"
		b.bumpETag(p)
		grown = true
	}

	stdout, _, err := run(t, box, "--timeout", "3s", "tail", "-f", "--interval", "10ms", p)
	if err != nil {
		t.Fatalf("following should end quietly when the deadline passes: %v", err)
	}
	if !strings.Contains(stdout, "appended later") {
		t.Errorf("the appended line was not printed:\n%s", stdout)
	}
	// And printed once, not on every poll.
	if n := strings.Count(stdout, "appended later"); n != 1 {
		t.Errorf("the appended line was printed %d times:\n%s", n, stdout)
	}
}

// TestTailFollowStartsOverOnAReplacedFile: a rotated log is a different file,
// and continuing from the old offset would print from the middle of a line.
func TestTailFollowStartsOverOnAReplacedFile(t *testing.T) {
	box, p := logBox(t, 20)

	var replaced bool
	box.afterRequest = func(b *testBox) {
		if replaced || countGets(b) < 2 {
			return
		}
		b.files[p] = "the new log\n"
		b.bumpETag(p)
		replaced = true
	}

	stdout, stderr, err := run(t, box, "--timeout", "3s", "tail", "-f", "--interval", "10ms", p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "the new log") {
		t.Errorf("the replacement was not followed:\n%s", stdout)
	}
	if !strings.Contains(stderr, "replaced") {
		t.Errorf("nothing said the file had been replaced:\n%s", stderr)
	}
}

func TestTailRejectsABadInterval(t *testing.T) {
	box, p := logBox(t, 2)

	_, _, err := run(t, box, "tail", "-f", "--interval", "0", p)
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("got %v, want a usage error", err)
	}
}

// countGets is how many file reads the box has served, which is how a test
// waits for the startup to finish without waiting on a clock.
//
// Only reads of the file itself: the identity call is a GET too, and counting
// it made a test change the file halfway through the startup rather than after
// it.
func countGets(b *testBox) int {
	n := 0
	for _, req := range b.requests {
		if strings.HasPrefix(req, "GET "+testDavPrefix) {
			n++
		}
	}
	return n
}

// TestTailSurvivesAFileThatShrinksMidRead is the race the tests themselves
// walked into: the file can be replaced between working out where its last
// lines begin and reading them, and a 416 about a range nobody typed is not a
// useful way for a command that follows a log to end.
func TestTailSurvivesAFileThatShrinksMidRead(t *testing.T) {
	box, p := logBox(t, 20)

	// Replaced after the read that locates the last lines and before the read
	// that prints them, which is exactly the window.
	var shrunk bool
	box.afterRequest = func(b *testBox) {
		if shrunk || countGets(b) < 1 {
			return
		}
		b.files[p] = "much shorter\n"
		b.bumpETag(p)
		shrunk = true
	}

	stdout, stderr, err := run(t, box, "tail", p)
	if err != nil {
		t.Fatalf("a file that shrank mid-read should be followed, not fatal: %v", err)
	}
	if !strings.Contains(stdout, "much shorter") {
		t.Errorf("the new contents were not shown:\n%s", stdout)
	}
	if !strings.Contains(stderr, "shorter than it was") {
		t.Errorf("nothing explained the restart:\n%s", stderr)
	}
}
