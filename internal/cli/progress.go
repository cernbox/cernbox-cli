package cli

import (
	"io"
	"sync"

	"github.com/cernbox/cernbox-cli/pkg/clipboard"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/cernbox/cernbox-cli/pkg/transfer"
)

// meter wraps output.Meter so that a nil one is usable.
//
// Progress is off in most runs — piped output, --quiet, --output json,
// --no-progress — and a transfer path that had to ask "is there a bar?" at every
// step would be mostly that question. Every method here tolerates a nil receiver,
// so the callers read as though the bar always exists.
type meter struct{ *output.Meter }

func (m *meter) Add(n int64) {
	if m != nil {
		m.Meter.Add(n)
	}
}

func (m *meter) SetLabel(label string) {
	if m != nil {
		m.Meter.SetLabel(label)
	}
}

func (m *meter) Stop() {
	if m != nil {
		m.Meter.Stop()
	}
}

// Writer wraps dst so what passes through it counts, and returns dst untouched
// when there is no bar.
func (m *meter) Writer(dst io.Writer) io.Writer {
	if m == nil {
		return dst
	}
	return m.Meter.Writer(dst)
}

// engineProgress turns the transfer engine's events into increments for a bar.
//
// The engine reports a running total per resource, and a paste may cover several
// which the engine transfers in parallel. Feeding those totals straight to the bar
// would make it jump backwards as the reports interleave, so the last figure seen
// for each resource is remembered and only the difference is added.
type engineProgress struct {
	m *meter

	mu   sync.Mutex
	seen map[string]int64
}

func newEngineProgress(m *meter) *engineProgress {
	return &engineProgress{m: m, seen: map[string]int64{}}
}

// event implements transfer.ProgressFunc.
func (p *engineProgress) event(ev transfer.Event) {
	if ev.Err != nil {
		return
	}
	p.mu.Lock()
	prev := p.seen[ev.Path]
	if ev.Transferred <= prev {
		p.mu.Unlock()
		return
	}
	p.seen[ev.Path] = ev.Transferred
	p.mu.Unlock()

	p.m.Add(ev.Transferred - prev)
}

// total is how much has been accumulated across every resource, for a test to
// check that interleaved reports add up rather than fight each other.
func (p *engineProgress) total() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var sum int64
	for _, n := range p.seen {
		sum += n
	}
	return sum
}

// progressFunc returns the engine callback, or nil when no bar is being drawn so
// that the engine does no progress work at all.
func (p *engineProgress) progressFunc() transfer.ProgressFunc {
	if p.m == nil {
		return nil
	}
	return p.event
}

// manifestTotal is how many bytes a paste will move, and -1 when that cannot be
// known — a live handover of a pipe has no length until it ends, and a bar that
// invented a percentage for it would be lying.
func manifestTotal(m *clipboard.Manifest) int64 {
	var total int64
	for _, e := range m.Entries {
		if e.Size < 0 {
			return -1
		}
		total += e.Size
	}
	return total
}

// pasteLabel names what the bar is showing when a paste covers more than one
// item, in which case no single name is right.
func pasteLabel(m *clipboard.Manifest) string {
	if len(m.Entries) == 1 {
		return m.Entries[0].Name
	}
	return itemCount(len(m.Entries))
}
