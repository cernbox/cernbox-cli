package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
)

// openScreen is a seam: the tests drive the browser through a fake screen that
// replays a scripted sequence of keys and records the frames it is asked to
// draw, so almost all of this file is exercised without a terminal.
var openScreen = output.OpenScreen

type browseMode int

const (
	modeBrowse browseMode = iota
	modeFilter
	modeWindow
	modeConfirm
)

// browser is the state of an interactive trash session.
type browser struct {
	app   *App
	scr   output.Screen
	space string
	base  string

	window  client.TrashWindow
	listing *client.TrashListing
	root    *trashNode

	// where the user is, and what they have picked
	at     []string
	cursor int
	top    int
	sel    map[string]client.TrashItem

	mode   browseMode
	input  string
	filter string
	status string
	plan   trashPlan
	quit   bool

	// report is written to the normal screen after the browser closes. The
	// alternate screen takes its contents with it, so anything the user needs to
	// keep has to survive the handover.
	report []string
}

// runTrashBrowser fetches a first window, then takes the terminal.
//
// The order matters. Credentials resolve on the first request, not when the
// client is built, so a device-code sign-in prints a URL and a code and waits —
// which has to happen while the user can still see the normal screen. The same
// goes for the first failure: a 403, or a space with no trash bin, is an
// ordinary command error, not something to discover inside a full-screen UI.
func runTrashBrowser(ctx context.Context, app *App, space, base string, window client.TrashWindow) error {
	b := &browser{app: app, space: space, base: base, window: window, sel: map[string]client.TrashItem{}}
	if err := b.fetch(ctx); err != nil {
		return err
	}

	scr, err := openScreen(app.stdinFile(), app.stdoutFile())
	if err != nil {
		return cberr.New(cberr.KindUsage, "browse the trash bin", "",
			"this needs a terminal; 'cernbox trash list' prints the same thing")
	}
	b.scr = scr

	loopErr := b.loop(ctx)

	// Closed before anything is printed, so the report lands on the screen the
	// user is left looking at.
	_ = scr.Close()
	for _, line := range b.report {
		app.out.Msg("%s", line)
	}
	return loopErr
}

func (b *browser) loop(ctx context.Context) error {
	for {
		if err := b.draw(); err != nil {
			return err
		}
		ev, err := b.scr.NextEvent()
		if err != nil {
			return nil
		}
		if ev.Resize {
			continue
		}
		b.handle(ctx, ev.Key)
		if b.quit {
			return nil
		}
	}
}

func (b *browser) draw() error {
	w, h := b.scr.Size()
	return b.scr.Draw(b.frame(w, h))
}

// ── fetching ───────────────────────────────────────────────────────────────

func (b *browser) fetch(ctx context.Context) error {
	listing, err := b.app.client.ListTrash(ctx, b.base, b.window)
	if err != nil {
		return err
	}
	b.listing = listing
	b.window = listing.Window
	b.root = buildTrashTree(listing.Items)

	// A narrower window can take away the directory the user was standing in.
	for len(b.at) > 0 && b.root.find(b.at) == nil {
		b.at = b.at[:len(b.at)-1]
	}
	b.clampCursor()
	return nil
}

// reload refetches with the current window, drawing a notice first because the
// request can take a while on a full bin and a frozen frame looks like a hang.
func (b *browser) reload(ctx context.Context) {
	b.status = "Loading…"
	_ = b.draw()
	if err := b.fetch(ctx); err != nil {
		b.status = errLine(err)
		return
	}
	b.status = ""
}

// ── the view ───────────────────────────────────────────────────────────────

// rows are what the current directory shows, after filtering.
type browseRow struct {
	node *trashNode
	up   bool
}

func (b *browser) rows() []browseRow {
	node := b.root.find(b.at)
	if node == nil {
		return nil
	}
	var out []browseRow
	if len(b.at) > 0 {
		out = append(out, browseRow{up: true})
	}
	for _, c := range node.children {
		if b.filter != "" && !strings.Contains(strings.ToLower(c.name), strings.ToLower(b.filter)) {
			continue
		}
		out = append(out, browseRow{node: c})
	}
	return out
}

func (b *browser) current() *trashNode {
	rows := b.rows()
	if b.cursor < 0 || b.cursor >= len(rows) {
		return nil
	}
	return rows[b.cursor].node
}

func (b *browser) clampCursor() {
	n := len(b.rows())
	if b.cursor >= n {
		b.cursor = max(0, n-1)
	}
	if b.cursor < 0 {
		b.cursor = 0
	}
}

// frame renders the whole screen. It is a pure function of the state and the
// size, which is what makes the browser testable: a test drives keys and
// asserts on frames, with no terminal anywhere.
func (b *browser) frame(w, h int) []output.Line {
	if h < 6 || w < 20 {
		return []output.Line{{Text: output.Ellipsize("terminal too small", w)}}
	}

	lines := make([]output.Line, 0, h)
	lines = append(lines, output.Line{Text: b.titleBar(), Highlight: true})
	lines = append(lines, output.Line{Text: strings.Repeat("─", w)})

	body := h - 4 // title, rule, status, help
	rows := b.rows()
	b.scrollInto(body, len(rows))

	for i := b.top; i < len(rows) && i < b.top+body; i++ {
		lines = append(lines, output.Line{
			Text:      b.rowText(rows[i], w),
			Highlight: i == b.cursor,
		})
	}
	for range body - min(body, max(0, len(rows)-b.top)) {
		lines = append(lines, output.Line{})
	}

	lines = append(lines, output.Line{Text: strings.Repeat("─", w)})
	lines = append(lines, output.Line{Text: b.footer()})

	// Trimmed here rather than only in the screen, so that a frame is a true
	// description of what will be shown and a test can hold it to the width.
	for i := range lines {
		lines[i].Text = output.Ellipsize(lines[i].Text, w)
	}
	return lines
}

// scrollInto keeps the cursor on screen.
func (b *browser) scrollInto(body, total int) {
	if b.cursor < b.top {
		b.top = b.cursor
	}
	if b.cursor >= b.top+body {
		b.top = b.cursor - body + 1
	}
	if b.top > max(0, total-body) {
		b.top = max(0, total-body)
	}
	if b.top < 0 {
		b.top = 0
	}
}

func (b *browser) titleBar() string {
	where := "/"
	if len(b.at) > 0 {
		where = "/" + strings.Join(b.at, "/")
	}
	space := b.space
	if space == "" {
		space = "home"
	}

	parts := []string{"TRASH  " + space + ":" + where, b.windowLabel()}
	if b.root != nil {
		parts = append(parts, fmt.Sprintf("%d entries", b.root.entries))
	}
	if n := len(b.sel); n > 0 {
		parts = append(parts, fmt.Sprintf("%d selected", n))
	}
	if b.listing != nil && len(b.listing.Gaps) > 0 {
		parts = append(parts, fmt.Sprintf("⚠ %d days unlistable", len(b.listing.Gaps)))
	}
	if b.filter != "" {
		parts = append(parts, "filter: "+b.filter)
	}
	return strings.Join(parts, "  ·  ")
}

func (b *browser) windowLabel() string {
	const day = "2 Jan"
	return "loaded " + b.window.From.Format(day) + "–" + b.window.To.Format(day)
}

// rowText lays out one row: marks, name, what it holds, and when it went.
func (b *browser) rowText(r browseRow, w int) string {
	if r.up {
		return "  .."
	}
	n := r.node

	mark := " "
	if b.isSelected(n) {
		mark = "✓"
	}
	icon, name := " ", n.name
	switch {
	case n.wholeFolder():
		icon, name = "▪", n.name+"/"
	case n.isDir():
		icon, name = "▸", n.name+"/"
	}

	var detail string
	switch {
	case n.wholeFolder():
		detail = fmt.Sprintf("whole folder  %9s", output.HumanSize(n.size))
	case n.isDir():
		detail = fmt.Sprintf("%d items  %9s", n.entries, output.HumanSize(n.size))
	default:
		detail = fmt.Sprintf("%19s", output.HumanSize(n.size))
	}
	age := output.HumanTime(n.newest, time.Now())

	// The name takes whatever the fixed columns leave, so a long path shortens
	// instead of pushing the layout apart.
	fixed := len(mark) + len(icon) + 2 + 2 + len([]rune(detail)) + 2 + len([]rune(age))
	nameWidth := max(8, w-fixed)
	return fmt.Sprintf("%s%s %-*s  %s  %s",
		mark, icon, nameWidth, output.Ellipsize(name, nameWidth), detail, age)
}

func (b *browser) footer() string {
	switch b.mode {
	case modeFilter:
		return "search: " + b.input + "▏  (enter to apply, esc to cancel)"
	case modeWindow:
		return "load since: " + b.input + "▏  e.g. 30d, 2w  (enter to load, esc to cancel)"
	case modeConfirm:
		return b.confirmPrompt()
	}
	if b.status != "" {
		return b.status
	}
	return "↑↓ move  → open  ← up  SPACE pick  / search  t window  r restore  c clear  q quit"
}

func (b *browser) confirmPrompt() string {
	files, dirs, size := 0, 0, int64(0)
	for _, it := range b.plan.Items {
		if it.IsDir {
			dirs++
		} else {
			files++
		}
		size += it.Size
	}
	msg := fmt.Sprintf("Restore %d entries (%d folders, %d files, %s)",
		len(b.plan.Items), dirs, files, output.HumanSize(size))
	if b.plan.Covered > 0 {
		msg += fmt.Sprintf(", %d already covered by a folder above them", b.plan.Covered)
	}
	return msg + "?  [y/N]"
}

// ── keys ───────────────────────────────────────────────────────────────────

func (b *browser) handle(ctx context.Context, k output.Key) {
	switch b.mode {
	case modeFilter, modeWindow:
		b.handleInput(ctx, k)
		return
	case modeConfirm:
		// Out of the confirmation before the work starts, not after: the footer
		// belongs to the mode, so restoring while still in modeConfirm would
		// keep drawing the question instead of the progress.
		b.mode = modeBrowse
		if k.Is('y') || k.Is('Y') {
			b.restore(ctx)
		} else {
			b.status = "Cancelled."
		}
		return
	}

	switch {
	case k.Name == output.KeyInterrupt, k.Is('q'), k.Name == output.KeyEscape:
		b.quit = true
	case k.Name == output.KeyUp, k.Is('k'):
		b.cursor = max(0, b.cursor-1)
	case k.Name == output.KeyDown, k.Is('j'):
		b.cursor = min(max(0, len(b.rows())-1), b.cursor+1)
	case k.Name == output.KeyPageUp:
		_, h := b.scr.Size()
		b.cursor = max(0, b.cursor-(h-4))
	case k.Name == output.KeyPageDown:
		_, h := b.scr.Size()
		b.cursor = min(max(0, len(b.rows())-1), b.cursor+(h-4))
	case k.Name == output.KeyHome:
		b.cursor = 0
	case k.Name == output.KeyEnd:
		b.cursor = max(0, len(b.rows())-1)
	case k.Name == output.KeyRight, k.Name == output.KeyEnter, k.Is('l'):
		b.open()
	case k.Name == output.KeyLeft, k.Name == output.KeyBackspace, k.Is('h'):
		b.leave()
	case k.Is(' '):
		b.toggle()
	case k.Is('c'):
		b.sel = map[string]client.TrashItem{}
		b.status = "Selection cleared."
	case k.Is('/'):
		b.mode, b.input = modeFilter, b.filter
	case k.Is('t'):
		b.mode, b.input = modeWindow, ""
	case k.Is('r'):
		b.propose()
	}
}

func (b *browser) handleInput(ctx context.Context, k output.Key) {
	switch k.Name {
	case output.KeyEscape, output.KeyInterrupt:
		b.mode = modeBrowse
	case output.KeyEnter:
		if b.mode == modeFilter {
			b.filter = b.input
			b.cursor, b.top = 0, 0
		} else if since := strings.TrimSpace(b.input); since != "" {
			d, err := parseLookback(since)
			if err != nil {
				b.status = errLine(err)
				b.mode = modeBrowse
				return
			}
			to := time.Now()
			b.window = client.TrashWindow{From: to.Add(-d), To: to}
			b.mode = modeBrowse
			b.reload(ctx)
			return
		}
		b.mode = modeBrowse
	case output.KeyBackspace:
		if b.input != "" {
			r := []rune(b.input)
			b.input = string(r[:len(r)-1])
		}
	case output.KeyRune:
		b.input += string(k.Rune)
	}
}

func (b *browser) open() {
	rows := b.rows()
	if b.cursor >= len(rows) {
		return
	}
	if rows[b.cursor].up {
		b.leave()
		return
	}
	n := rows[b.cursor].node
	if !n.isDir() || len(n.children) == 0 {
		return
	}
	b.at = append(b.at, n.name)
	b.top, b.filter = 0, ""

	// Past the ".." row. Landing on it would mean the next Enter walks straight
	// back out of the directory just opened.
	b.cursor = 0
	for i, r := range b.rows() {
		if !r.up {
			b.cursor = i
			break
		}
	}
}

func (b *browser) leave() {
	if len(b.at) == 0 {
		return
	}
	leaving := b.at[len(b.at)-1]
	b.at = b.at[:len(b.at)-1]
	b.filter, b.top = "", 0

	// Put the cursor back on the directory just left, rather than at the top:
	// walking out of a deep tree one level at a time is otherwise maddening.
	b.cursor = 0
	for i, r := range b.rows() {
		if r.node != nil && r.node.name == leaving {
			b.cursor = i
			break
		}
	}
}

// toggle picks or unpicks the row under the cursor. A directory takes everything
// below it, because that is what a user means by pointing at a folder.
func (b *browser) toggle() {
	n := b.current()
	if n == nil {
		return
	}
	entries := n.subtreeEntries()
	if len(entries) == 0 {
		return
	}
	if b.isSelected(n) {
		for _, it := range entries {
			delete(b.sel, it.Key)
		}
	} else {
		for _, it := range entries {
			b.sel[it.Key] = it
		}
	}
	b.cursor = min(max(0, len(b.rows())-1), b.cursor+1)
}

// isSelected reports whether everything a node stands for is picked.
func (b *browser) isSelected(n *trashNode) bool {
	entries := n.subtreeEntries()
	if len(entries) == 0 {
		return false
	}
	for _, it := range entries {
		if _, ok := b.sel[it.Key]; !ok {
			return false
		}
	}
	return true
}

// propose builds the plan and asks. Nothing is restored until the answer is yes.
func (b *browser) propose() {
	sel := make([]client.TrashItem, 0, len(b.sel))
	for _, it := range b.sel {
		sel = append(sel, it)
	}
	if len(sel) == 0 {
		// Nothing picked: act on what the cursor is pointing at, which is what
		// pressing restore on a row plainly means.
		if n := b.current(); n != nil {
			sel = n.subtreeEntries()
		}
	}
	if len(sel) == 0 {
		b.status = "Nothing to restore. Pick rows with SPACE."
		return
	}
	b.plan = buildTrashPlan(sel)
	b.mode = modeConfirm
}

// ── doing it ───────────────────────────────────────────────────────────────

// restore works through the plan, redrawing as it goes.
//
// Parents first, so a child never arrives before the directory that has to hold
// it; within one depth the order does not matter, so those run together.
func (b *browser) restore(ctx context.Context) {
	var done, failed int
	var failures []string

	for _, group := range byDepth(b.plan.Items) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		var restored []string

		for _, it := range group {
			wg.Add(1)
			go func(it client.TrashItem) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				err := b.app.client.RestoreTrash(ctx, it.Key, b.base)

				// Only this function's own state, never the browser's: the
				// redraw below reads the whole browser to build a frame, and a
				// worker writing to it at the same time is a race whether or not
				// a test happens to be slow enough to catch it.
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					failed++
					if len(failures) < 10 {
						failures = append(failures, fmt.Sprintf("  %s — %s",
							orDash(it.OriginalPath), errLine(err)))
					}
					return
				}
				done++
				restored = append(restored, it.Key)
			}(it)
		}

		// Redraw while the group runs, so a long restore shows movement.
		waiting := make(chan struct{})
		go func() { wg.Wait(); close(waiting) }()
		// Named so as not to shadow the running total.
		for groupDone := false; !groupDone; {
			select {
			case <-waiting:
				groupDone = true
			case <-time.After(150 * time.Millisecond):
				mu.Lock()
				b.status = b.progressLine(done + failed)
				mu.Unlock()
				_ = b.draw()
			}
		}

		for _, key := range restored {
			delete(b.sel, key)
		}
	}

	b.report = append(b.report, fmt.Sprintf("Restored %d of %d entries.", done, len(b.plan.Items)))
	if b.plan.Covered > 0 {
		b.report = append(b.report,
			fmt.Sprintf("%d were left to the folders above them, which bring them back.", b.plan.Covered))
	}
	if failed > 0 {
		b.report = append(b.report, fmt.Sprintf("%d failed:", failed))
		b.report = append(b.report, failures...)
	}
	if len(b.plan.Items) > 0 && len(b.plan.Items) <= 5 {
		var keys []string
		for _, it := range b.plan.Items {
			keys = append(keys, it.Key)
		}
		b.report = append(b.report, "Same thing without the browser:",
			"  cernbox trash restore "+strings.Join(keys, " "))
	}

	b.status = fmt.Sprintf("Restored %d, failed %d.", done, failed)
	b.plan = trashPlan{}

	// What is in the bin has changed, so the tree has to be rebuilt rather than
	// left showing entries that are now files again.
	b.reload(ctx)
}

// progressLine is what the status shows while a restore is running. Kept here
// rather than inline so the worker loop reads as the one thing it is doing.
func (b *browser) progressLine(finished int) string {
	return fmt.Sprintf("Restoring… %d of %d", finished, len(b.plan.Items))
}

// byDepth groups an ordered plan into runs of equal path depth.
func byDepth(items []client.TrashItem) [][]client.TrashItem {
	var out [][]client.TrashItem
	var cur []client.TrashItem
	depth := -1
	for _, it := range items {
		d := len(trashPathSegments(it.OriginalPath))
		if d != depth && cur != nil {
			out = append(out, cur)
			cur = nil
		}
		depth = d
		cur = append(cur, it)
	}
	if cur != nil {
		out = append(out, cur)
	}
	return out
}

// errLine reduces an error to something that fits on one row.
func errLine(err error) string {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return msg
}

// stdinFile and stdoutFile are the real terminal behind the App's streams, or nil
// when there is none. The streams are interfaces so that tests can read back
// what a user would see, but taking over a terminal needs the file itself.
func (a *App) stdinFile() *os.File {
	if f, ok := a.stdin.(*os.File); ok {
		return f
	}
	return nil
}

func (a *App) stdoutFile() *os.File {
	if f, ok := a.stdout.(*os.File); ok {
		return f
	}
	return nil
}
