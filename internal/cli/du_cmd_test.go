package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// duTree builds an account with known sizes at known depths, so a ranking can be
// checked rather than merely observed.
func duTree(t *testing.T) *testBox {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/data/raw/2026")
	box.mkdir("/eos/user/e/einstein/small")
	box.putFile("/eos/user/e/einstein/data/raw/2026/huge.bin", strings.Repeat("x", 9000))
	box.putFile("/eos/user/e/einstein/data/mid.bin", strings.Repeat("x", 500))
	box.putFile("/eos/user/e/einstein/small/tiny.txt", strings.Repeat("x", 10))
	return box
}

// paths returns the path column of du's output, in the order it was printed.
func duPaths(out string) []string {
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if _, p, ok := strings.Cut(line, "\t"); ok {
			got = append(got, p)
		}
	}
	return got
}

// TestDuTopRanksBiggestFirst is the point of the flag: the question is not "how
// big is this" but "what is using the space".
func TestDuTopRanksBiggestFirst(t *testing.T) {
	stdout, _, err := run(t, duTree(t), "du", "--top", "3", "/eos/user/e/einstein")
	if err != nil {
		t.Fatal(err)
	}

	got := duPaths(stdout)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3:\n%s", len(got), stdout)
	}
	// data (9500) > data/raw (9000) = data/raw/2026 (9000), and the 9000s break
	// the tie on the path so the order is stable between runs.
	want := []string{
		"/eos/user/e/einstein/data",
		"/eos/user/e/einstein/data/raw",
		"/eos/user/e/einstein/data/raw/2026",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %s, want %s\n%s", i, got[i], want[i], stdout)
		}
	}
}

// TestDuTopFindsSomethingBuriedDeep: the biggest thing is rarely at the top
// level, so ranking has to look all the way down. This also covers the depth
// being unbounded, which is where an overflowing slice hint would panic.
func TestDuTopFindsSomethingBuriedDeep(t *testing.T) {
	stdout, _, err := run(t, duTree(t), "du", "--top", "20", "/eos/user/e/einstein")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "data/raw/2026/huge.bin") {
		t.Errorf("the deep file is missing, so the walk stopped short:\n%s", stdout)
	}
	// Files as well as directories: a single file is usually the answer.
	if !strings.Contains(stdout, "small/tiny.txt") {
		t.Errorf("files should be ranked too:\n%s", stdout)
	}
}

// TestDuTopLeavesOutTheArgumentItself: the total is the biggest entry by
// definition, and it is what plain du already prints.
func TestDuTopLeavesOutTheArgumentItself(t *testing.T) {
	stdout, _, err := run(t, duTree(t), "du", "--top", "10", "/eos/user/e/einstein")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range duPaths(stdout) {
		if p == "/eos/user/e/einstein" {
			t.Errorf("the argument's own total was ranked:\n%s", stdout)
		}
	}
}

// TestDuTopRespectsAnExplicitDepth: --top widens the walk only because nothing
// else said how far to look.
func TestDuTopRespectsAnExplicitDepth(t *testing.T) {
	stdout, _, err := run(t, duTree(t), "du", "--top", "20", "-d", "1", "/eos/user/e/einstein")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "huge.bin") {
		t.Errorf("-d 1 should not have reached the file three levels down:\n%s", stdout)
	}
	if !strings.Contains(stdout, "/eos/user/e/einstein/data") {
		t.Errorf("the first level should still be there:\n%s", stdout)
	}
}

func TestDuTopRejectsSummarize(t *testing.T) {
	_, _, err := run(t, duTree(t), "du", "--top", "5", "-s", "/eos/user/e/einstein")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error: -s leaves nothing to rank", err)
	}
}

func TestDuTopRejectsANegativeCount(t *testing.T) {
	_, _, err := run(t, duTree(t), "du", "--top", "-2", "/eos/user/e/einstein")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

// TestDuTopKeepsTheTabSeparatedShape: du's output is parsed by scripts, and
// ranking changes the order of the rows rather than their shape.
func TestDuTopKeepsTheTabSeparatedShape(t *testing.T) {
	stdout, _, err := run(t, duTree(t), "du", "--top", "2", "-h", "/eos/user/e/einstein")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if !strings.Contains(line, "\t") {
			t.Errorf("row %q has no tab, so 'cut -f2' would break", line)
		}
	}
}

func TestDuWithoutTopIsUnchanged(t *testing.T) {
	stdout, _, err := run(t, duTree(t), "du", "/eos/user/e/einstein")
	if err != nil {
		t.Fatal(err)
	}
	// One row, the total, deepest-last as du prints it.
	if got := duPaths(stdout); len(got) != 1 || got[0] != "/eos/user/e/einstein" {
		t.Errorf("plain du printed %v, want just the total", got)
	}
}

// ── --versions: the bytes no listing shows ───────────────────────────────────

// hiddenTree is a tree with version bytes at two levels, which is what makes the
// roll-up worth testing: the deeper ones are what a user cannot find by looking.
//
//	proj/         1000 visible + 3000 hidden
//	proj/sub/      500 visible + 1500 hidden
func hiddenTree(t *testing.T) *testBox {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/proj/sub")
	box.putFile("/eos/user/e/einstein/proj/a.bin", strings.Repeat("x", 1000))
	box.putFile("/eos/user/e/einstein/proj/sub/b.bin", strings.Repeat("x", 500))
	box.hidden["/eos/user/e/einstein/proj"] = 3000
	box.hidden["/eos/user/e/einstein/proj/sub"] = 1500
	return box
}

// columns returns the numeric columns of one --versions row, by path.
func columns(t *testing.T, out, wantPath string) (charged, listed, unlisted string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 4 && f[3] == wantPath {
			return f[0], f[1], f[2]
		}
	}
	t.Fatalf("no row for %s in:\n%s", wantPath, out)
	return "", "", ""
}

// TestDuVersionsSplitsWhatIsCharged is the whole feature: du already bills you
// for versions, and this says how much of the number they are.
func TestDuVersionsSplitsWhatIsCharged(t *testing.T) {
	stdout, _, err := run(t, hiddenTree(t), "du", "--versions", "-d", "1",
		"/eos/user/e/einstein/proj")
	if err != nil {
		t.Fatal(err)
	}

	charged, listed, unlisted := columns(t, stdout, "/eos/user/e/einstein/proj/sub")
	if charged != "2000" || listed != "500" || unlisted != "1500" {
		t.Errorf("sub = charged %s, listed %s, unlisted %s; want 2000/500/1500\n%s",
			charged, listed, unlisted, stdout)
	}
}

// TestDuVersionsCountsHiddenBytesFromDeeperDown: the reason this walks the whole
// tree even when printing one level. Version bytes several directories down are
// exactly the ones a user cannot find by looking, so they have to be rolled up.
func TestDuVersionsCountsHiddenBytesFromDeeperDown(t *testing.T) {
	stdout, _, err := run(t, hiddenTree(t), "du", "--versions",
		"/eos/user/e/einstein/proj")
	if err != nil {
		t.Fatal(err)
	}

	charged, listed, unlisted := columns(t, stdout, "/eos/user/e/einstein/proj")
	// 1000 + 3000 + 500 + 1500 charged; 1500 of it is the two files.
	if charged != "6000" || listed != "1500" || unlisted != "4500" {
		t.Errorf("proj = charged %s, listed %s, unlisted %s; want 6000/1500/4500\n%s",
			charged, listed, unlisted, stdout)
	}
}

func TestDuVersionsReportsNothingHiddenWhenNothingIs(t *testing.T) {
	stdout, _, err := run(t, duTree(t), "du", "--versions", "/eos/user/e/einstein/small")
	if err != nil {
		t.Fatal(err)
	}
	_, listed, unlisted := columns(t, stdout, "/eos/user/e/einstein/small")
	if unlisted != "0" || listed != "10" {
		t.Errorf("listed %s, unlisted %s; want 10/0\n%s", listed, unlisted, stdout)
	}
}

// TestDuVersionsKeepsStdoutParseable: the labels are for a person, so they must
// not land in a pipe.
func TestDuVersionsKeepsStdoutParseable(t *testing.T) {
	stdout, stderr, err := run(t, hiddenTree(t), "du", "--versions",
		"/eos/user/e/einstein/proj")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "CHARGED") {
		t.Errorf("the header reached stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "CHARGED") {
		t.Errorf("the header should be on stderr:\n%s", stderr)
	}
	// And it says what the unlisted bytes are, since the number alone explains
	// nothing to somebody wondering where their quota went.
	if !strings.Contains(stderr, "versions") {
		t.Errorf("stderr should explain the unlisted bytes:\n%s", stderr)
	}
}

func TestDuVersionsJSONCarriesTheSplit(t *testing.T) {
	stdout, _, err := run(t, hiddenTree(t), "--output", "json", "du", "--versions",
		"/eos/user/e/einstein/proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"unlisted"`, `"listed"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("JSON is missing %s:\n%s", want, stdout)
		}
	}
}

// TestDuJSONIsUnchangedWithoutTheFlag: the split fields are omitted, so anything
// already parsing du keeps seeing what it saw.
func TestDuJSONIsUnchangedWithoutTheFlag(t *testing.T) {
	stdout, _, err := run(t, hiddenTree(t), "--output", "json", "du",
		"/eos/user/e/einstein/proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{`"unlisted"`, `"listed"`} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("JSON gained %s without --versions:\n%s", unwanted, stdout)
		}
	}
}

func TestDuVersionsRejectsSummarize(t *testing.T) {
	_, _, err := run(t, hiddenTree(t), "du", "--versions", "-s", "/eos/user/e/einstein/proj")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error", err)
	}
}

// rankingTree makes the two rankings disagree, which is the only way to tell
// them apart: both directories have hidden bytes, so neither is filtered out,
// and they come in opposite orders depending on which figure is ranked.
//
//	rank/big/      10000 visible,  100 hidden  → charged 10100, hidden  100
//	rank/history/    100 visible, 5000 hidden  → charged  5100, hidden 5000
//
// A fixture where the big directory had nothing hidden would not do: it would
// be dropped for having no history whatever the sort key was, and a test on it
// passes with the ranking put back the way it was. Checked by doing exactly
// that.
func rankingTree(t *testing.T) *testBox {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/rank/big")
	box.mkdir("/eos/user/e/einstein/rank/history")
	box.putFile("/eos/user/e/einstein/rank/big/a.bin", strings.Repeat("x", 10000))
	box.putFile("/eos/user/e/einstein/rank/history/b.bin", strings.Repeat("x", 100))
	box.hidden["/eos/user/e/einstein/rank/big"] = 100
	box.hidden["/eos/user/e/einstein/rank/history"] = 5000
	return box
}

// emptyHistoryTree is rankingTree without the token history in the big
// directory, for the filtering half of the behaviour.
func emptyHistoryTree(t *testing.T) *testBox {
	box := rankingTree(t)
	delete(box.hidden, "/eos/user/e/einstein/rank/big")
	return box
}

// TestDuVersionsTopRanksByWhatIsHidden is the question the two flags together
// are asked: not which directory is biggest, which plain --top already answers,
// but which is big because of its history. Ranking these rows by their totals
// answers the first question while printing the columns of the second, and the
// largest directory in a tree frequently has the least history in it.
func TestDuVersionsTopRanksByWhatIsHidden(t *testing.T) {
	stdout, _, err := run(t, rankingTree(t), "du", "--versions", "--top", "1",
		"/eos/user/e/einstein/rank")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "/rank/history") {
		t.Errorf("the directory holding the history is not the top row:\n%s", stdout)
	}
	if strings.Contains(stdout, "/rank/big") {
		t.Errorf("the bigger directory holds less history, so it does not rank "+
			"first here — that is what plain --top is for:\n%s", stdout)
	}
}

// TestDuVersionsTopLeavesOutRowsWithNothingHidden: most rows have nothing
// hidden, and files never have any of their own — a file's history is charged
// to the directory beside it — so neither belongs in this ranking.
func TestDuVersionsTopLeavesOutRowsWithNothingHidden(t *testing.T) {
	stdout, _, err := run(t, emptyHistoryTree(t), "du", "--versions", "--top", "20",
		"/eos/user/e/einstein/rank")
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want only the one with history:\n%s", len(rows), stdout)
	}
}

// TestDuVersionsWorksWithTop: --top still widens the walk and rolls up, so the
// row it keeps carries the bytes from below it.
func TestDuVersionsWorksWithTop(t *testing.T) {
	stdout, _, err := run(t, hiddenTree(t), "du", "--versions", "--top", "2",
		"/eos/user/e/einstein/proj")
	if err != nil {
		t.Fatal(err)
	}
	charged, listed, unlisted := columns(t, stdout, "/eos/user/e/einstein/proj/sub")
	if charged != "2000" || listed != "500" || unlisted != "1500" {
		t.Errorf("sub = charged %s, listed %s, unlisted %s; want 2000/500/1500\n%s",
			charged, listed, unlisted, stdout)
	}
	// a.bin is a file with no history, and --versions asks about history.
	if strings.Contains(stdout, "a.bin") {
		t.Errorf("a file with no history of its own is in the ranking:\n%s", stdout)
	}
}

// ── the walk runs its listings together ──────────────────────────────────────

// wideTree is broad rather than deep, so a level holds enough directories for
// concurrency to be observable at all.
func wideTree(t *testing.T, dirs int) *testBox {
	box := newTestBox(t)
	for i := range dirs {
		d := fmt.Sprintf("/eos/user/e/einstein/wide/d%02d", i)
		box.mkdir(d)
		box.putFile(d+"/f.bin", strings.Repeat("x", 100))
		box.hidden[d] = 50
	}
	return box
}

// TestDuVersionsWalksInParallel: one request per directory is unavoidable, but
// they need not wait for each other. Serialised, the whole cost was latency times
// directory count.
func TestDuVersionsWalksInParallel(t *testing.T) {
	box := wideTree(t, 24)

	if _, _, err := run(t, box, "du", "--versions", "--jobs", "6",
		"/eos/user/e/einstein/wide"); err != nil {
		t.Fatal(err)
	}
	if peak := box.peakInFlight.Load(); peak < 2 {
		t.Errorf("peak concurrency was %d: the listings ran one after another", peak)
	}
}

// TestDuVersionsRespectsTheJobLimit: unbounded concurrency against a real server
// is a way to be rate-limited, so the bound has to be a bound.
func TestDuVersionsRespectsTheJobLimit(t *testing.T) {
	box := wideTree(t, 30)

	if _, _, err := run(t, box, "du", "--versions", "--jobs", "3",
		"/eos/user/e/einstein/wide"); err != nil {
		t.Fatal(err)
	}
	// One more than asked for is allowed: the walk lists a level of directories
	// under the limit, and the request that discovered the level may still be
	// finishing as they start.
	if peak := box.peakInFlight.Load(); peak > 4 {
		t.Errorf("peak concurrency was %d with --jobs 3", peak)
	}
}

// TestDuVersionsIsCorrectUnderConcurrency: the listings race, so the totals must
// not. Run this package with -race as well.
func TestDuVersionsIsCorrectUnderConcurrency(t *testing.T) {
	const dirs = 24
	box := wideTree(t, dirs)

	stdout, _, err := run(t, box, "--output", "json", "du", "--versions", "--jobs", "8",
		"/eos/user/e/einstein/wide")
	if err != nil {
		t.Fatal(err)
	}

	var rows []struct {
		Path     string `json:"path"`
		Size     int64  `json:"size"`
		Listed   int64  `json:"listed"`
		Unlisted int64  `json:"unlisted"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout)
	}

	var root *struct {
		Path     string `json:"path"`
		Size     int64  `json:"size"`
		Listed   int64  `json:"listed"`
		Unlisted int64  `json:"unlisted"`
	}
	for i := range rows {
		if rows[i].Path == "/eos/user/e/einstein/wide" {
			root = &rows[i]
		}
	}
	if root == nil {
		t.Fatalf("no row for the root:\n%s", stdout)
	}
	// Every directory holds 100 listable bytes and 50 nobody can see.
	if root.Listed != dirs*100 || root.Unlisted != dirs*50 {
		t.Errorf("root = listed %d, unlisted %d; want %d/%d",
			root.Listed, root.Unlisted, dirs*100, dirs*50)
	}
	if root.Size != root.Listed+root.Unlisted {
		t.Errorf("charged %d != listed %d + unlisted %d", root.Size, root.Listed, root.Unlisted)
	}
}
