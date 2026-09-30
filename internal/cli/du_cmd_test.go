package cli

import (
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

// TestDuVersionsWorksWithTop: the two flags answer one question together — which
// of the big things is big because of its history.
func TestDuVersionsWorksWithTop(t *testing.T) {
	stdout, _, err := run(t, hiddenTree(t), "du", "--versions", "--top", "2",
		"/eos/user/e/einstein/proj")
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2:\n%s", len(rows), stdout)
	}
	// sub is charged 2000, a.bin 1000, so sub ranks first.
	if !strings.HasSuffix(rows[0], "/proj/sub") {
		t.Errorf("first row = %q, want proj/sub\n%s", rows[0], stdout)
	}
}
