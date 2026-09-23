package cli

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/clipboard"
)

// A live handover has two processes in it, so the fake server has to serve both
// at once: these tests run the two halves of the CLI in parallel goroutines
// against one box, which is why the box serialises its own state behind a lock.

// streamBox is a testBox that can serve two clients at once.
func streamBox(t *testing.T) *testBox {
	t.Helper()
	box := newTestBox(t)
	box.mkdir(clipHome)
	return box
}

// runPair drives a sender and a receiver against the same box, returning what
// each of them did. The sender blocks until the receiver arrives, so they have to
// be running at the same time for either to finish.
func runPair(t *testing.T, box *testBox, in string, sendArgs, recvArgs []string) (recvOut string, sendErr, recvErr error) {
	t.Helper()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _, sendErr = runStdin(t, box, in, sendArgs...)
	}()
	go func() {
		defer wg.Done()
		// A short delay so the sender has written the manifest. Without it the
		// receiver would legitimately find no slot; a real user types the second
		// command seconds later.
		time.Sleep(150 * time.Millisecond)
		recvOut, _, recvErr = run(t, box, recvArgs...)
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the handover deadlocked: neither side finished")
	}
	return recvOut, sendErr, recvErr
}

// TestStreamHandsOverLive is the feature: copy waits, paste arrives, the bytes
// move, and nothing is left on the server.
func TestStreamHandsOverLive(t *testing.T) {
	box := streamBox(t)
	cfg := smallChunkConfig(t, "1K")
	body := streamPayload(5000)

	out, sendErr, recvErr := runPair(t, box, body,
		[]string{"--config", cfg, "copy", "--stream", "-", "--name", "live.bin", "--wait", "20s"},
		[]string{"--config", cfg, "paste", "--wait", "20s", "-"})

	if sendErr != nil {
		t.Fatalf("the sender failed: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("the receiver failed: %v", recvErr)
	}
	if out != body {
		t.Errorf("the receiver got %d bytes, want %d", len(out), len(body))
	}

	// Nothing survives a handover: no manifest, no chunks, no slot.
	for p := range box.snapshotFiles() {
		if strings.Contains(p, "/.cernbox/clipboard/") {
			t.Errorf("the handover left %s behind", p)
		}
	}
}

// TestStreamNeverHoldsMoreThanTheWindow is the property that makes this a pipe
// rather than storage: however much goes through, the server holds only a few
// pieces at a time.
func TestStreamNeverHoldsMoreThanTheWindow(t *testing.T) {
	box := streamBox(t)
	cfg := smallChunkConfig(t, "1K")
	body := streamPayload(60 << 10) // sixty pieces through a window of four

	var peak int
	var mu sync.Mutex
	box.afterRequest = func(b *testBox) {
		n := 0
		for p := range b.files {
			if strings.Contains(p, "/clipboard/default/stream/") {
				n++
			}
		}
		mu.Lock()
		peak = max(peak, n)
		mu.Unlock()
	}

	out, sendErr, recvErr := runPair(t, box, body,
		[]string{"--config", cfg, "copy", "--stream", "-", "--wait", "20s"},
		[]string{"--config", cfg, "paste", "--wait", "20s", "-"})

	if sendErr != nil || recvErr != nil {
		t.Fatalf("send: %v, receive: %v", sendErr, recvErr)
	}
	if out != body {
		t.Errorf("the receiver got %d bytes, want %d", len(out), len(body))
	}

	mu.Lock()
	defer mu.Unlock()
	if peak > streamWindow {
		t.Errorf("the server held %d pieces at once, want at most %d: the window is not holding",
			peak, streamWindow)
	}
	if peak == 0 {
		t.Error("no pieces were ever observed in flight, so this test proved nothing")
	}
}

// TestStreamWritesToAFile: the receiving end does not have to be a pipe.
func TestStreamWritesToAFile(t *testing.T) {
	box := streamBox(t)
	cfg := smallChunkConfig(t, "1K")
	body := streamPayload(3000)
	dest := t.TempDir()

	_, sendErr, recvErr := runPair(t, box, body,
		[]string{"--config", cfg, "copy", "--stream", "-", "--name", "landed.bin", "--wait", "20s"},
		[]string{"--config", cfg, "paste", "--wait", "20s", dest})

	if sendErr != nil || recvErr != nil {
		t.Fatalf("send: %v, receive: %v", sendErr, recvErr)
	}
	got := readFileOrFail(t, dest+"/landed.bin")
	if got != body {
		t.Errorf("the file holds %d bytes, want %d", len(got), len(body))
	}
	if fileExists(dest + "/landed.bin.part") {
		t.Error("the partial file was left behind")
	}
}

// TestStreamSenderGivesUpWithoutAReceiver: copy waits, but not forever, and it
// has to clean up after itself when it stops.
func TestStreamSenderGivesUpWithoutAReceiver(t *testing.T) {
	box := streamBox(t)

	_, _, err := runStdin(t, box, "nobody is listening",
		"copy", "--stream", "-", "--wait", "600ms")
	if err == nil {
		t.Fatal("the sender should give up when nobody pastes")
	}
	if !strings.Contains(err.Error(), "nobody pasted") {
		t.Errorf("the error should say what it was waiting for: %v", err)
	}
	for p := range box.snapshotFiles() {
		if strings.Contains(p, "/.cernbox/clipboard/") {
			t.Errorf("the abandoned handover left %s behind", p)
		}
	}
}

// TestStreamReceiverGivesUpOnASilentSender: the mirror image, so that a paste
// against a sender that died does not hang for ever.
func TestStreamReceiverGivesUpOnASilentSender(t *testing.T) {
	box := streamBox(t)

	// A manifest that claims a stream, with no process behind it.
	m := clipboard.New(clipboard.DefaultSlot, time.Now(), clipboard.DefaultTTL, clipboard.Origin{Host: "gone"})
	m.Mode = clipboard.ModeStream
	m.Stream = &clipboard.StreamInfo{ChunkSize: 1024, Window: streamWindow}
	m.Entries = []clipboard.Entry{{
		Name: "orphan.bin", Path: clipRoot + "/default/stream", Staged: true,
	}}
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	box.putFile(manifestPath(clipboard.DefaultSlot), string(body))
	box.mkdir(clipRoot + "/default/stream")

	_, _, err = run(t, box, "paste", "--wait", "600ms", "-")
	if err == nil {
		t.Fatal("the receiver should give up on a sender that never sends")
	}
	if !strings.Contains(err.Error(), "other computer") {
		t.Errorf("the error should point at the sender: %v", err)
	}
}

// TestStreamRefusesARemoteSource: streaming a file that is already on the server
// would be pure waste, and the alternative is strictly better.
func TestStreamRefusesARemoteSource(t *testing.T) {
	box := streamBox(t)
	box.putFile(clipHome+"/already.txt", "already there")

	_, _, err := run(t, box, "copy", "--stream", "cb:"+clipHome+"/already.txt")
	if err == nil {
		t.Fatal("streaming something already in CERNBox should be refused")
	}
	if !strings.Contains(err.Error(), "already in CERNBox") {
		t.Errorf("the error should explain why: %v", err)
	}
}

// TestStreamRefusesSeveralSources: a handover is one live pipe.
func TestStreamRefusesSeveralSources(t *testing.T) {
	box := streamBox(t)
	dir := t.TempDir()
	writeFileOrFail(t, dir+"/a.txt", "a")
	writeFileOrFail(t, dir+"/b.txt", "b")

	if _, _, err := run(t, box, "copy", "--stream", dir+"/a.txt", dir+"/b.txt"); err == nil {
		t.Fatal("--stream with two sources should be refused")
	}
}

// TestStreamCannotBePastedIntoCERNBox: there is nothing on the server to copy —
// the bytes only exist while they are passing through.
func TestStreamCannotBePastedIntoCERNBox(t *testing.T) {
	box := streamBox(t)

	m := clipboard.New(clipboard.DefaultSlot, time.Now(), clipboard.DefaultTTL, clipboard.Origin{Host: "sender"})
	m.Mode = clipboard.ModeStream
	m.Stream = &clipboard.StreamInfo{ChunkSize: 1024, Window: streamWindow}
	m.Entries = []clipboard.Entry{{Name: "live.bin", Path: clipRoot + "/default/stream", Staged: true}}
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	box.putFile(manifestPath(clipboard.DefaultSlot), string(body))

	_, _, err = run(t, box, "paste", "cb:"+clipHome+"/landing/")
	if err == nil {
		t.Fatal("a live stream cannot be pasted to a CERNBox path")
	}
	if !strings.Contains(err.Error(), "sending this live") {
		t.Errorf("the error should say why: %v", err)
	}
}

// TestStreamManifestRoundTrip: the mode and window survive encoding, since they
// are what the far end reads to know this is a handover at all.
func TestStreamManifestRoundTrip(t *testing.T) {
	m := clipboard.New("live", time.Now(), 0, clipboard.Origin{Host: "h"})
	m.Mode = clipboard.ModeStream
	m.Stream = &clipboard.StreamInfo{ChunkSize: 4096, Window: 4}
	m.Entries = []clipboard.Entry{{Name: "x", Path: "/p", Staged: true}}

	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := clipboard.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsStream() {
		t.Error("the stream mode did not survive")
	}
	if got.Stream == nil || got.Stream.ChunkSize != 4096 || got.Stream.Window != 4 {
		t.Errorf("the stream settings did not survive: %+v", got.Stream)
	}

	// And a stored copy must not grow the field, so an older reader sees exactly
	// what it saw before.
	stored := clipboard.New("kept", time.Now(), 0, clipboard.Origin{})
	sb, err := stored.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sb), "mode") || strings.Contains(string(sb), "stream") {
		t.Errorf("a stored copy should carry neither field:\n%s", sb)
	}
}

func TestDoneMarkerRoundTrip(t *testing.T) {
	b, err := json.Marshal(clipboard.Done{Parts: 7, Size: 1234})
	if err != nil {
		t.Fatal(err)
	}
	var got clipboard.Done
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Parts != 7 || got.Size != 1234 {
		t.Errorf("got %+v", got)
	}
}

func TestChunkIndex(t *testing.T) {
	cases := map[string]struct {
		n  int
		ok bool
	}{
		"00000":  {0, true},
		"00007":  {7, true},
		"12345":  {12345, true},
		"":       {0, false},
		"0000":   {0, false},
		"000000": {0, false},
		"0000a":  {0, false},
		"reader": {0, false},
		"done":   {0, false},
	}
	for name, want := range cases {
		n, ok := clipboard.ChunkIndex(name)
		if ok != want.ok || (ok && n != want.n) {
			t.Errorf("ChunkIndex(%q) = %d, %v; want %d, %v", name, n, ok, want.n, want.ok)
		}
	}
}

// ── small file helpers ───────────────────────────────────────────────────────

func readFileOrFail(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return string(b)
}

func writeFileOrFail(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestPendingChunksCountsOnlyPieces(t *testing.T) {
	// A handover's arrival marker lives in the pieces directory, because it is the
	// only place the recipient can write. Counting entries rather than pieces would
	// leave the window permanently one short and the drain never finishing.
	entries := []client.ResourceInfo{
		{Name: "00000"}, {Name: "00001"}, {Name: "reader"},
	}
	if got := pendingChunks(entries); got != 2 {
		t.Errorf("pendingChunks = %d, want 2", got)
	}
	if got := pendingChunks([]client.ResourceInfo{{Name: "reader"}}); got != 0 {
		t.Errorf("a directory holding only the marker has %d pieces, want 0", got)
	}
}

func TestStreamHandoverSharesTheSlotAndTheScratchDirectory(t *testing.T) {
	box := streamBox(t)

	// Nobody will paste, so the sender gives up — after it has set up everything
	// the recipient would have needed.
	_, _, err := runStdin(t, box, "payload",
		"copy", "--stream", "-", "--name", "live.bin", "--to", "marie", "--wait", "1s")
	if err == nil {
		t.Fatal("the sender should have given up with no receiver")
	}
	if !strings.Contains(err.Error(), "paste --from einstein") {
		t.Errorf("the error does not say what the recipient had to type: %v", err)
	}

	// Two grants, deliberately different: the slot read-only so the manifest
	// cannot be rewritten, and the pieces directory writable so the recipient can
	// announce itself and delete what it has consumed.
	var roles []string
	for _, body := range box.postBodies {
		switch {
		case strings.Contains(body, client.RoleViewer):
			roles = append(roles, "viewer")
		case strings.Contains(body, client.RoleEditor):
			roles = append(roles, "editor")
		}
		if !strings.Contains(body, `"marie"`) {
			t.Errorf("a share went to somebody other than the recipient: %s", body)
		}
	}
	if strings.Join(roles, ",") != "viewer,editor" {
		t.Fatalf("the shares were %v, want viewer on the slot then editor on the pieces directory", roles)
	}

	// And it waited on the marker the recipient can actually write.
	waited := false
	for _, r := range box.requests {
		if strings.HasSuffix(r, "/to-marie/stream/reader") {
			waited = true
		}
		if strings.HasSuffix(r, "/to-marie/reader") {
			t.Errorf("the sender waited on a marker the recipient cannot write: %v", r)
		}
	}
	if !waited {
		t.Errorf("the sender never looked for the recipient's marker: %v", box.requests)
	}
}
