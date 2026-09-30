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
