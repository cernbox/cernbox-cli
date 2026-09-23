package cli

import (
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// complete runs a completion the way a shell does and returns the candidates.
//
// cobra prints the candidates one per line, then a line beginning with ":" that
// carries the directive. Both are parsed here, because a completion is as much
// the directive as the list: the same candidates with the wrong directive leave
// the shell appending spaces inside a path, or offering local filenames for a
// CERNBox one.
func complete(t *testing.T, box *testBox, args ...string) ([]string, cobra.ShellCompDirective) {
	t.Helper()

	stdout, _, err := run(t, box, append([]string{cobra.ShellCompRequestCmd}, args...)...)
	if err != nil {
		t.Fatalf("completion failed: %v", err)
	}

	var out []string
	directive := cobra.ShellCompDirectiveDefault
	for line := range strings.SplitSeq(strings.TrimRight(stdout, "\n"), "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, ":"):
			d, err := strconv.Atoi(strings.TrimSpace(line[1:]))
			if err != nil {
				t.Fatalf("cannot read the directive from %q: %v", line, err)
			}
			directive = cobra.ShellCompDirective(d)
		default:
			// Descriptions are separated from the candidate by a tab.
			out = append(out, strings.SplitN(line, "\t", 2)[0])
		}
	}
	return out, directive
}

func TestCompleteListsTheChildrenOfTheHomeSpace(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents")
	box.putFile("/eos/user/e/einstein/notes.txt", "hello")

	got, directive := complete(t, box, "ls", "")

	want := []string{"Documents/", "notes.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	// A directory must not gain a trailing space, or typing the next name means
	// deleting one first.
	if directive&cobra.ShellCompDirectiveNoSpace == 0 {
		t.Errorf("directive %v does not keep the cursor on a directory", directive)
	}
	if directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Errorf("directive %v lets the shell offer local filenames for a CERNBox path", directive)
	}
}

func TestCompleteFiltersByWhatIsTyped(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/report.pdf", "a")
	box.putFile("/eos/user/e/einstein/receipt.pdf", "b")
	box.putFile("/eos/user/e/einstein/notes.txt", "c")

	got, _ := complete(t, box, "cat", "re")

	want := []string{"receipt.pdf", "report.pdf"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestCompleteKeepsThePrefixAsTyped(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/Documents")
	box.putFile("/eos/user/e/einstein/Documents/report.pdf", "a")

	// Whatever the user typed to name the directory has to come back on the
	// candidate, or the shell replaces the whole word with something that means
	// a different path.
	for _, tc := range []struct {
		typed string
		want  string
	}{
		{"/eos/user/e/einstein/Documents/", "/eos/user/e/einstein/Documents/report.pdf"},
		{"cb:/eos/user/e/einstein/Documents/re", "cb:/eos/user/e/einstein/Documents/report.pdf"},
		{"home:Documents/", "home:Documents/report.pdf"},
		{"Documents/", "Documents/report.pdf"},
	} {
		got, _ := complete(t, box, "cat", tc.typed)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("completing %q gave %v, want [%s]", tc.typed, got, tc.want)
		}
	}
}

func TestCompleteHidesDotEntriesUntilAsked(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/.cernbox")
	box.putFile("/eos/user/e/einstein/notes.txt", "a")

	got, _ := complete(t, box, "ls", "")
	if len(got) != 1 || got[0] != "notes.txt" {
		t.Fatalf("candidates = %v, want [notes.txt]: the CLI's own directory should stay out of the way", got)
	}

	got, _ = complete(t, box, "ls", ".")
	if len(got) != 1 || got[0] != ".cernbox/" {
		t.Fatalf("candidates = %v, want [.cernbox/] once a dot is typed", got)
	}
}

func TestCompleteLeavesLocalPathsToTheShell(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "a")

	// The first argument of put is on this computer. Nothing is suggested, and
	// the directive tells the shell to complete filenames itself.
	got, directive := complete(t, box, "put", "./re")
	if len(got) != 0 {
		t.Errorf("candidates = %v, want none for a local path", got)
	}
	if directive != cobra.ShellCompDirectiveDefault {
		t.Errorf("directive = %v, want the shell to complete the filename", directive)
	}

	// The second is in CERNBox, and is completed.
	got, _ = complete(t, box, "put", "./report.pdf", "")
	if len(got) != 1 || got[0] != "notes.txt" {
		t.Errorf("candidates = %v, want [notes.txt]", got)
	}
}

func TestCompleteOnlyTouchesTheServerForAMarkedTransferPath(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "a")

	// cp cannot tell the two sides apart by the string alone, so an unmarked
	// word is local and no request is made at all.
	if _, directive := complete(t, box, "cp", "/eos/user"); directive != cobra.ShellCompDirectiveDefault {
		t.Errorf("directive = %v, want the shell to complete an unmarked path locally", directive)
	}
	if len(box.requests) != 0 {
		t.Errorf("completing an unmarked path made requests: %v", box.requests)
	}

	got, _ := complete(t, box, "cp", "cb:")
	if len(got) != 1 || got[0] != "cb:notes.txt" {
		t.Errorf("candidates = %v, want [cb:notes.txt]", got)
	}
}

func TestCompleteStopsAtTheLastArgument(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.txt", "a")

	// mv takes two paths. A third is not a path, and suggesting one would be
	// inviting a usage error.
	got, _ := complete(t, box, "mv", "a", "b", "")
	if len(got) != 0 {
		t.Fatalf("candidates = %v, want none past the last argument", got)
	}
}

func TestCompleteClipboardSlots(t *testing.T) {
	box := newTestBox(t)
	box.mkdir("/eos/user/e/einstein/.cernbox/clipboard/default")
	box.mkdir("/eos/user/e/einstein/.cernbox/clipboard/plots")

	got, _ := complete(t, box, "clipboard", "clear", "")
	want := []string{"default", "plots"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestCompleteSpaceAliases(t *testing.T) {
	box := newTestBox(t)

	got, _ := complete(t, box, "space", "info", "")
	if len(got) != 2 || got[0] != "home" {
		t.Fatalf("candidates = %v, want home first", got)
	}
}

func TestCompleteFlagValues(t *testing.T) {
	box := newTestBox(t)

	got, _ := complete(t, box, "archive", "--format", "")
	if strings.Join(got, ",") != "tar,zip" {
		t.Fatalf("candidates = %v, want tar and zip", got)
	}

	got, _ = complete(t, box, "--output", "j")
	if strings.Join(got, ",") != "json" {
		t.Fatalf("candidates = %v, want json", got)
	}
}

func TestCompleteVersionKeys(t *testing.T) {
	box := newTestBox(t)
	versionsFor(box, "/eos/user/e/einstein/report.txt", "current",
		versionEntry{key: "1700000000", body: "older"},
		versionEntry{key: "1800000000", body: "newer"},
	)

	// A version key is a timestamp nobody types from memory.
	got, _ := complete(t, box, "versions", "restore", "/eos/user/e/einstein/report.txt", "18")
	if len(got) != 1 || got[0] != "1800000000" {
		t.Fatalf("candidates = %v, want [1800000000]", got)
	}
}

func TestCompleteShareAndLinkIDs(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/shared.txt", "x")

	// The fake reports one share and one link on the path. Each command
	// completes only the kind of id it can act on.
	got, _ := complete(t, box, "share", "remove", "/eos/user/e/einstein/shared.txt", "")
	if len(got) != 1 || got[0] != "share-1" {
		t.Errorf("share ids = %v, want [share-1]", got)
	}

	got, _ = complete(t, box, "link", "remove", "/eos/user/e/einstein/shared.txt", "")
	if len(got) != 1 || got[0] != "link-1" {
		t.Errorf("link ids = %v, want [link-1]", got)
	}
}

func TestCompleteApplicationNames(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/notes.odt", "x")

	got, _ := complete(t, box, "open", "--app", "")
	if len(got) != 1 || got[0] != "Collabora" {
		t.Fatalf("applications = %v, want [Collabora]", got)
	}
}

func TestCompleteAppTokenIDs(t *testing.T) {
	box := newTestBox(t)

	got, _ := complete(t, box, "token", "revoke", "")
	if len(got) != 1 || got[0] != "c1" {
		t.Fatalf("app token ids = %v, want [c1]", got)
	}
}

func TestCompleteSurvivesAnUnreachableServer(t *testing.T) {
	box := newTestBox(t)
	box.ts.Close() // nothing is listening any more

	// A shell asks for completions on a laptop with no network as readily as on
	// a working one. The answer is an empty list and a clean exit, never an
	// error printed into the middle of the command the user is typing.
	stdout, stderr, err := run(t, box, cobra.ShellCompRequestCmd, "ls", "")
	if err != nil {
		t.Fatalf("completion returned an error: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		if line != "" && !strings.HasPrefix(line, ":") {
			t.Errorf("completion suggested %q with no server to ask", line)
		}
	}
	if strings.Contains(stderr, "cernbox:") {
		t.Errorf("completion printed an error at the user's cursor: %q", stderr)
	}
}

// TestEveryCommandCompletesItsArguments checks the completion table against the
// command tree, in both directions.
//
// A command added without an entry falls back to completing local filenames,
// which for a CLI whose arguments are mostly CERNBox paths is always wrong and
// is invisible until somebody presses TAB. This is the same bargain as the
// integration suite's coverage map: adding a command forces the decision.
func TestEveryCommandCompletesItsArguments(t *testing.T) {
	app := &App{flags: &globalFlags{}}
	root := newRootCmd(app)

	table := app.argCompletions()
	present := map[string]bool{}

	walkCommands(root, nil, func(path string, cmd *cobra.Command) {
		if cmd.Name() == "completion" || strings.HasPrefix(cmd.Name(), "__") {
			return
		}
		present[path] = true
		if cmd.ValidArgsFunction == nil {
			t.Errorf("%q completes nothing: add it to argCompletions, with "+
				"cobra.NoFileCompletions if it takes no arguments", path)
		}
	})

	for path := range table {
		if !present[path] {
			t.Errorf("argCompletions has an entry for %q, which is not a command", path)
		}
	}
}

func TestFlagCompletionsNameRealFlags(t *testing.T) {
	// completeFlagValues panics on a flag that does not exist, so building the
	// tree is the check. It runs here rather than nowhere so that the failure is
	// a test, not somebody's first TAB.
	app := &App{flags: &globalFlags{}}
	root := newRootCmd(app)

	for path := range flagCompletions {
		cmd, _, err := root.Find(strings.Split(path, " "))
		if err != nil {
			t.Errorf("flagCompletions names %q, which is not a command: %v", path, err)
		} else if cmd.Name() != strings.Fields(path)[len(strings.Fields(path))-1] {
			t.Errorf("flagCompletions names %q, which resolved to %q", path, cmd.Name())
		}
	}
}
