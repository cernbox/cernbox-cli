package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
)

// ── a screen with no terminal behind it ──────────────────────────────────────

// fakeScreen replays a scripted sequence of events and records every frame it is
// asked to draw. The browser's rendering is a pure function of its state, so
// this is enough to exercise all of it without a terminal anywhere.
type fakeScreen struct {
	w, h   int
	events []output.Event
	at     int
	frames [][]output.Line
	closed bool
}

func (f *fakeScreen) Size() (int, int) { return f.w, f.h }

func (f *fakeScreen) NextEvent() (output.Event, error) {
	if f.at >= len(f.events) {
		// Input ending is how a real session ends too, so the loop treats it as
		// a quit rather than an error.
		return output.Event{}, context.Canceled
	}
	ev := f.events[f.at]
	f.at++
	return ev, nil
}

func (f *fakeScreen) Draw(lines []output.Line) error {
	f.frames = append(f.frames, append([]output.Line(nil), lines...))
	return nil
}

func (f *fakeScreen) Close() error { f.closed = true; return nil }

// last is the final frame as plain text, which is what a user would be left
// looking at.
func (f *fakeScreen) last() string {
	if len(f.frames) == 0 {
		return ""
	}
	return frameText(f.frames[len(f.frames)-1])
}

// all is every frame concatenated, for asserting that something was shown at
// some point during the session.
func (f *fakeScreen) all() string {
	var sb strings.Builder
	for _, fr := range f.frames {
		sb.WriteString(frameText(fr))
	}
	return sb.String()
}

func frameText(lines []output.Line) string {
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func kr(r rune) output.Event { return output.Event{Key: output.Key{Name: output.KeyRune, Rune: r}} }
func kn(n output.KeyName) output.Event {
	return output.Event{Key: output.Key{Name: n}}
}
func ktype(s string) []output.Event {
	var out []output.Event
	for _, r := range s {
		out = append(out, kr(r))
	}
	return out
}

// browserFor builds a browser wired to the test box, with a fake screen that
// will replay events.
func browserFor(t *testing.T, box *testBox, events ...output.Event) (*browser, *fakeScreen) {
	t.Helper()

	c, err := client.New(box.ts.URL, client.WithCredentials(
		client.CredentialFunc(func(context.Context) (client.Credential, error) {
			return client.Credential{Header: "Authorization", Value: "Bearer test-token"}, nil
		})))
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	app := &App{
		flags:  &globalFlags{},
		stdout: &stdout,
		stderr: &stderr,
		client: c,
		out:    output.New(&stdout, output.FormatTable, output.Stderr(&stderr)),
	}
	scr := &fakeScreen{w: 100, h: 20, events: events}
	b := &browser{app: app, scr: scr, sel: map[string]client.TrashItem{}}
	if err := b.fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b, scr
}

// ── the tree ─────────────────────────────────────────────────────────────────

func TestTrashTreeGroupsByOriginalPath(t *testing.T) {
	now := time.Now()
	items := []client.TrashItem{
		{Key: "k1", Name: "a.txt", OriginalPath: "Documents/notes/a.txt", Size: 10, DeletedAt: now},
		{Key: "k2", Name: "b.txt", OriginalPath: "Documents/notes/b.txt", Size: 20, DeletedAt: now.Add(-time.Hour)},
		{Key: "k3", Name: "report.pdf", OriginalPath: "Documents/report.pdf", Size: 100, DeletedAt: now},
		{Key: "k4", Name: "plots", OriginalPath: "plots", Size: 4096, DeletedAt: now, IsDir: true},
	}

	root := buildTrashTree(items)
	if root.entries != 4 {
		t.Errorf("root holds %d entries, want 4", root.entries)
	}
	if root.size != 4226 {
		t.Errorf("root size = %d, want 4226", root.size)
	}

	docs := root.find([]string{"Documents"})
	if docs == nil {
		t.Fatal("Documents is missing from the tree")
	}
	// Documents itself was never deleted, only things inside it.
	if docs.entry != nil {
		t.Error("Documents should have no entry of its own")
	}
	if docs.entries != 3 {
		t.Errorf("Documents holds %d entries, want 3", docs.entries)
	}
	if !docs.isDir() {
		t.Error("a node with children is a directory")
	}

	// A directory deleted whole is one entry that restores everything.
	plots := root.find([]string{"plots"})
	if plots == nil || !plots.wholeFolder() {
		t.Errorf("plots = %+v, want a whole deleted folder", plots)
	}

	// Directories sort before files.
	if root.children[0].name != "Documents" {
		t.Errorf("first child is %q, want Documents to sort first", root.children[0].name)
	}
}

// TestTrashTreeKeepsTheNewestOfARepeatedPath: the same name can be deleted over
// and over. The newest is the one a restore should bring back.
func TestTrashTreeKeepsTheNewestOfARepeatedPath(t *testing.T) {
	now := time.Now()
	root := buildTrashTree([]client.TrashItem{
		{Key: "old", OriginalPath: "log.txt", DeletedAt: now.Add(-48 * time.Hour)},
		{Key: "new", OriginalPath: "log.txt", DeletedAt: now},
	})
	node := root.find([]string{"log.txt"})
	if node == nil || node.entry.Key != "new" {
		t.Errorf("kept %+v, want the newest deletion", node)
	}
}

func TestTrashTreeHandlesAnItemWithNoLocation(t *testing.T) {
	root := buildTrashTree([]client.TrashItem{{Key: "k", Name: "orphan.txt"}})
	if root.find([]string{"orphan.txt"}) == nil {
		t.Error("an item with no original path must still be reachable, since it is still restorable")
	}
}

// ── the plan ─────────────────────────────────────────────────────────────────

// TestTrashPlanDropsItemsCoveredByAFolder: restoring a folder brings back
// everything under it, so also restoring the children repeats the work and then
// fails, because by their turn the paths already exist.
func TestTrashPlanDropsItemsCoveredByAFolder(t *testing.T) {
	plan := buildTrashPlan([]client.TrashItem{
		{Key: "dir", OriginalPath: "analysis/output", IsDir: true},
		{Key: "child", OriginalPath: "analysis/output/run1/a.txt"},
		{Key: "sibling", OriginalPath: "analysis/outputs.txt"},
	})

	if plan.Covered != 1 {
		t.Errorf("Covered = %d, want 1", plan.Covered)
	}
	var keys []string
	for _, it := range plan.Items {
		keys = append(keys, it.Key)
	}
	// "analysis/outputs.txt" is not under "analysis/output" despite the prefix.
	want := []string{"sibling", "dir"}
	if len(keys) != 2 || !strings.Contains(strings.Join(keys, ","), "dir") ||
		!strings.Contains(strings.Join(keys, ","), "sibling") {
		t.Errorf("plan keys = %v, want %v in some order", keys, want)
	}
}

func TestTrashPlanRestoresParentsFirst(t *testing.T) {
	plan := buildTrashPlan([]client.TrashItem{
		{Key: "deep", OriginalPath: "a/b/c/d.txt"},
		{Key: "shallow", OriginalPath: "a"},
		{Key: "middle", OriginalPath: "a/b"},
	})
	var keys []string
	for _, it := range plan.Items {
		keys = append(keys, it.Key)
	}
	if got := strings.Join(keys, ","); got != "shallow,middle,deep" {
		t.Errorf("order = %s, want shallow,middle,deep: a child cannot arrive before its parent", got)
	}
}

// ── the browser ──────────────────────────────────────────────────────────────

func trashBox(t *testing.T) *testBox {
	box := newTestBox(t)
	box.trash["k1"] = trashEntry{name: "a.txt", location: "Documents/notes/a.txt", body: "alpha"}
	box.trash["k2"] = trashEntry{name: "report.pdf", location: "Documents/report.pdf", body: "pdf"}
	box.trash["k3"] = trashEntry{name: "old.txt", location: "old.txt", body: "old",
		deleted: time.Now().AddDate(0, 0, -20)}
	return box
}

func TestBrowserShowsTheTreeNotTheKeys(t *testing.T) {
	b, scr := browserFor(t, trashBox(t), kr('q'))
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}

	frame := scr.last()
	if !strings.Contains(frame, "Documents/") {
		t.Errorf("the first level should show the directory:\n%s", frame)
	}
	if strings.Contains(frame, "k1") || strings.Contains(frame, "k2") {
		t.Errorf("no key should be on screen — that is the point:\n%s", frame)
	}
	if !strings.Contains(frame, "TRASH") || !strings.Contains(frame, "loaded") {
		t.Errorf("the title bar should say where and when:\n%s", frame)
	}
	// The 20-day-old entry is outside the default window.
	if strings.Contains(frame, "old.txt") {
		t.Errorf("the default window should not reach 20 days back:\n%s", frame)
	}
}

func TestBrowserNavigatesIntoAndOutOfDirectories(t *testing.T) {
	// Into Documents, into notes, then back out twice.
	b, scr := browserFor(t, trashBox(t),
		kn(output.KeyEnter),
		kn(output.KeyEnter),
	)
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.at, "/"); got != "Documents/notes" {
		t.Fatalf("ended at %q, want Documents/notes", got)
	}
	if !strings.Contains(scr.last(), "a.txt") {
		t.Errorf("the file inside should be listed:\n%s", scr.last())
	}

	b2, _ := browserFor(t, trashBox(t),
		kn(output.KeyEnter), kn(output.KeyEnter), kn(output.KeyLeft), kn(output.KeyLeft))
	if err := b2.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b2.at) != 0 {
		t.Errorf("ended at %v, want the root", b2.at)
	}
}

// TestBrowserLeavingPutsTheCursorBackOnTheDirectory: walking out of a tree one
// level at a time is unusable if the cursor jumps to the top each time.
func TestBrowserLeavingPutsTheCursorBackOnTheDirectory(t *testing.T) {
	box := newTestBox(t)
	box.trash["k1"] = trashEntry{name: "a", location: "aaa/a.txt", body: "x"}
	box.trash["k2"] = trashEntry{name: "b", location: "zzz/b.txt", body: "y"}

	// Move to the second directory, enter it, come back.
	b, _ := browserFor(t, box, kn(output.KeyDown), kn(output.KeyEnter), kn(output.KeyLeft))
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := b.current()
	if n == nil || n.name != "zzz" {
		t.Errorf("cursor is on %v, want the directory just left", n)
	}
}

func TestBrowserRestoresWhatWasPicked(t *testing.T) {
	box := trashBox(t)
	// Into Documents, pick report.pdf, restore, confirm.
	events := []output.Event{kn(output.KeyEnter), kn(output.KeyDown), kr(' '), kr('r'), kr('y'), kr('q')}
	b, scr := browserFor(t, box, events...)
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := box.files["/eos/user/e/einstein/Documents/report.pdf"]; got != "pdf" {
		t.Errorf("the file was not restored: %+v", box.files)
	}
	if !strings.Contains(scr.all(), "Restore 1 entries") {
		t.Errorf("the plan should have been shown before restoring:\n%s", scr.all())
	}
	// What happened has to survive the alternate screen closing.
	if !strings.Contains(strings.Join(b.report, "\n"), "Restored 1 of 1") {
		t.Errorf("report = %q, want a summary for the normal screen", b.report)
	}
}

// TestBrowserRestoreNeedsConfirmation: nothing may move on the strength of one
// keypress.
func TestBrowserRestoreNeedsConfirmation(t *testing.T) {
	box := trashBox(t)
	b, _ := browserFor(t, box, kn(output.KeyEnter), kn(output.KeyDown), kr(' '), kr('r'), kr('n'), kr('q'))
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, restored := box.files["/eos/user/e/einstein/Documents/report.pdf"]; restored {
		t.Error("answering no still restored the file")
	}
	if len(box.trash) != 3 {
		t.Errorf("the bin holds %d items, want all 3 untouched", len(box.trash))
	}
}

// TestBrowserRestoresAWholeFolderInOneRequest is the cheap path: a directory
// deleted as a tree is a single entry.
func TestBrowserRestoresAWholeFolderInOneRequest(t *testing.T) {
	box := newTestBox(t)
	box.trash["tree"] = trashEntry{name: "plots", location: "plots", body: "dir", isDir: true}

	b, scr := browserFor(t, box, kr(' '), kr('r'), kr('y'), kr('q'))
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scr.all(), "whole folder") {
		t.Errorf("a folder deleted whole should say so:\n%s", scr.all())
	}
	if _, still := box.trash["tree"]; still {
		t.Error("the folder was not restored")
	}
}

// TestBrowserRedrawsWhileRestoring: a restore of many entries takes minutes, and
// a frame that does not move looks like a hang. Worth its own test because the
// redraw runs while the workers do, which is where a race would live — run this
// package with -race and this is the case that exercises it.
func TestBrowserRedrawsWhileRestoring(t *testing.T) {
	box := trashBox(t)
	box.trashMoveDelay = 250 * time.Millisecond

	b, scr := browserFor(t, box, kn(output.KeyEnter), kr(' '), kr('r'), kr('y'), kr('q'))
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scr.all(), "Restoring…") {
		t.Errorf("no progress was drawn while the restore ran:\n%s", scr.all())
	}
}

func TestBrowserWidensTheWindowFromInside(t *testing.T) {
	box := trashBox(t)
	events := append([]output.Event{kr('t')}, ktype("30d")...)
	events = append(events, kn(output.KeyEnter), kr('q'))

	b, scr := browserFor(t, box, events...)
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if days := b.window.To.Sub(b.window.From).Hours() / 24; days < 29 || days > 31 {
		t.Errorf("window is %.1f days, want 30", days)
	}
	if !strings.Contains(scr.last(), "old.txt") {
		t.Errorf("widening should have brought in the 20-day-old entry:\n%s", scr.last())
	}
}

func TestBrowserRejectsAnUnreadableWindow(t *testing.T) {
	events := append([]output.Event{kr('t')}, ktype("soon")...)
	events = append(events, kn(output.KeyEnter), kr('q'))

	b, scr := browserFor(t, trashBox(t), events...)
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scr.all(), "invalid --since") {
		t.Errorf("a bad span should be reported in the status line:\n%s", scr.last())
	}
}

func TestBrowserFiltersTheCurrentLevel(t *testing.T) {
	box := newTestBox(t)
	box.trash["k1"] = trashEntry{name: "keep.txt", location: "keep.txt", body: "a"}
	box.trash["k2"] = trashEntry{name: "other.txt", location: "other.txt", body: "b"}

	events := append([]output.Event{kr('/')}, ktype("keep")...)
	events = append(events, kn(output.KeyEnter), kr('q'))

	b, scr := browserFor(t, box, events...)
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	frame := scr.last()
	if !strings.Contains(frame, "keep.txt") || strings.Contains(frame, "other.txt") {
		t.Errorf("the filter did not narrow the level:\n%s", frame)
	}
	if !strings.Contains(frame, "filter: keep") {
		t.Errorf("the title bar must say a filter is on, or the listing looks short for no reason:\n%s", frame)
	}
}

// TestBrowserSaysWhenADayCouldNotBeListed: the gap has to reach the screen. A
// count that silently omits a day is the one thing a trash browser must not show.
func TestBrowserSaysWhenADayCouldNotBeListed(t *testing.T) {
	box := trashBox(t)
	box.trashRefuseDay = time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	b, scr := browserFor(t, box, kr('q'))
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scr.last(), "unlistable") {
		t.Errorf("the refused day is missing from the title bar:\n%s", scr.last())
	}
}

func TestBrowserSelectingADirectoryTakesEverythingUnderIt(t *testing.T) {
	b, _ := browserFor(t, trashBox(t), kr(' '))
	if err := b.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.sel) != 2 {
		t.Errorf("selected %d entries, want both files under Documents", len(b.sel))
	}
}

func TestBrowserFrameSurvivesATinyTerminal(t *testing.T) {
	b, _ := browserFor(t, trashBox(t))
	for _, size := range [][2]int{{10, 4}, {40, 8}, {200, 60}} {
		lines := b.frame(size[0], size[1])
		if len(lines) > size[1] {
			t.Errorf("%dx%d produced %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if n := len([]rune(l.Text)); n > size[0] {
				t.Errorf("%dx%d produced a %d-cell line: %q", size[0], size[1], n, l.Text)
			}
		}
	}
}

// ── launching it ─────────────────────────────────────────────────────────────

// TestBrowseRefusesWithoutATerminal is the guarantee that matters most: a cron
// job that meets a full-screen UI hangs until somebody kills it.
func TestBrowseRefusesWithoutATerminal(t *testing.T) {
	box := newTestBox(t)
	_, stderr, err := run(t, box, "trash", "browse")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error with no terminal", err)
	}
	if !strings.Contains(stderr+errLine(err), "trash list") {
		t.Errorf("the refusal should point at the listing:\n%s / %v", stderr, err)
	}
}

func TestBrowseRefusesMachineOutput(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "--output", "json", "trash", "browse")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error for --output json", err)
	}
}

// TestBrowsePlainFallsBackToTheListing keeps an escape hatch for terminals the
// browser cannot drive.
func TestBrowsePlainFallsBackToTheListing(t *testing.T) {
	box := trashBox(t)
	stdout, _, err := run(t, box, "trash", "browse", "--plain")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "KEY") {
		t.Errorf("--plain should print the listing:\n%s", stdout)
	}
}

func TestTrashRestoreWithNoKeysRefusesOffATerminal(t *testing.T) {
	box := newTestBox(t)
	_, _, err := run(t, box, "trash", "restore")
	if cberr.ExitCode(err) != cberr.ExitUsage {
		t.Errorf("got %v, want a usage error: there is no terminal to pick on", err)
	}
}
