package cli

import (
	"bytes"
	"os"
	"testing"

	"github.com/cernbox/cernbox-cli/pkg/clipboard"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/cernbox/cernbox-cli/pkg/transfer"
)

// TestProgressIsOffWhenNobodyIsWatching: a bar in a log file or in the middle of a
// pipe is noise, and under --output json it would corrupt a parseable stream. The
// gate is on stderr, not stdout, because "cernbox paste -" sends the payload to
// stdout and the bar belongs on the stream a person is looking at.
func TestProgressIsOffWhenNobodyIsWatching(t *testing.T) {
	// Under go test stderr is not a terminal, so the honest expectation here is
	// that progress is off in every one of these cases — including the "default"
	// one. What this pins is that each explicit reason is sufficient on its own.
	cases := map[string]*App{
		"default":     newProgressApp(t, false, false, output.FormatTable),
		"no-progress": newProgressApp(t, true, false, output.FormatTable),
		"quiet":       newProgressApp(t, false, true, output.FormatTable),
		"json":        newProgressApp(t, false, false, output.FormatJSON),
	}
	for name, app := range cases {
		if app.showProgress() {
			t.Errorf("%s: progress should not be drawn", name)
		}
		if app.newMeter("x", 100) != nil {
			t.Errorf("%s: newMeter should return nil", name)
		}
		if app.progressFunc() != nil {
			t.Errorf("%s: the engine should get no progress callback", name)
		}
	}
}

func newProgressApp(t *testing.T, noProgress, quiet bool, format output.Format) *App {
	t.Helper()
	var buf bytes.Buffer
	return &App{
		flags:  &globalFlags{noProgress: noProgress, quiet: quiet},
		out:    output.New(&buf, format, output.Quiet(quiet), output.Stderr(&buf)),
		stdout: &buf,
		stderr: &buf,
	}
}

// TestNilMeterIsUsable: progress is off in most runs, and every transfer path calls
// these without checking. A nil meter that panicked would break the common case
// rather than the rare one.
func TestNilMeterIsUsable(t *testing.T) {
	var m *meter

	m.Add(10)
	m.SetLabel("x")
	m.Stop()

	var sink bytes.Buffer
	if got := m.Writer(&sink); got != (&sink) {
		t.Error("a nil meter should hand back the destination untouched")
	}
	if _, err := m.Writer(&sink).Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "hello" {
		t.Errorf("the bytes did not pass through: %q", sink.String())
	}
	if newEngineProgress(nil).progressFunc() != nil {
		t.Error("a nil meter should mean no engine callback at all")
	}
}

// TestEngineProgressAccumulatesAcrossResources: the engine reports a running total
// per resource and transfers several at once, so feeding those totals to the bar
// directly would make it lurch backwards as the reports interleave.
func TestEngineProgressAccumulatesAcrossResources(t *testing.T) {
	var buf bytes.Buffer
	m := &meter{Meter: output.NewMeter(&buf, "two", 300, output.MeterWidth(80))}
	p := newEngineProgress(m)

	// Interleaved running totals from two files.
	p.event(transfer.Event{Path: "/a", Transferred: 50, Total: 100})
	p.event(transfer.Event{Path: "/b", Transferred: 40, Total: 200})
	p.event(transfer.Event{Path: "/a", Transferred: 100, Total: 100})
	p.event(transfer.Event{Path: "/b", Transferred: 200, Total: 200})

	if got := p.total(); got != 300 {
		t.Errorf("accumulated %d, want 300", got)
	}

	// A stale or repeated report must not double-count.
	p.event(transfer.Event{Path: "/a", Transferred: 60, Total: 100})
	if got := p.total(); got != 300 {
		t.Errorf("a stale report changed the total to %d", got)
	}
	// Nor must a failure.
	p.event(transfer.Event{Path: "/c", Transferred: 999, Err: os.ErrPermission})
	if got := p.total(); got != 300 {
		t.Errorf("a failed resource was counted: %d", got)
	}
}

func TestManifestTotal(t *testing.T) {
	stored := &clipboard.Manifest{Entries: []clipboard.Entry{{Size: 100}, {Size: 200}}}
	if got := manifestTotal(stored); got != 300 {
		t.Errorf("got %d, want 300", got)
	}
	// A live handover of a pipe has no length until it ends, and one unknown entry
	// makes the whole total unknown rather than wrong.
	live := &clipboard.Manifest{Entries: []clipboard.Entry{{Size: 100}, {Size: -1}}}
	if got := manifestTotal(live); got != -1 {
		t.Errorf("got %d, want -1", got)
	}
	if got := manifestTotal(&clipboard.Manifest{}); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestPasteLabel(t *testing.T) {
	one := &clipboard.Manifest{Entries: []clipboard.Entry{{Name: "a.txt"}}}
	if got := pasteLabel(one); got != "a.txt" {
		t.Errorf("got %q", got)
	}
	two := &clipboard.Manifest{Entries: []clipboard.Entry{{Name: "a"}, {Name: "b"}}}
	if got := pasteLabel(two); got != "2 items" {
		t.Errorf("got %q", got)
	}
}
