package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/clipboard"
)

const (
	// einsteinSlot is where a handover to the test user is staged, in the
	// sender's own space.
	otherSpace   = "/eos/user/o/other"
	einsteinSlot = otherSpace + "/.cernbox/clipboard/to-einstein"
	// myHandover is the slot the test user stages for somebody else.
	myHandover = "/eos/user/e/einstein/.cernbox/clipboard/to-marie"
)

func TestCopyToSharesASlotOfItsOwn(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	local := filepath.Join(dir, "report.pdf")
	writeFileOrFail(t, local, "contents")

	stdout, stderr, err := run(t, box, "copy", local, "--to", "marie")
	if err != nil {
		t.Fatalf("copy --to: %v (%s%s)", err, stdout, stderr)
	}

	// The slot is named after the recipient, never the default one: the whole
	// directory becomes readable by them.
	if body, ok := box.files[myHandover+"/payload/report.pdf"]; !ok || body != "contents" {
		t.Fatalf("the payload is not in the handover slot: %v", box.snapshotFiles())
	}
	var m clipboard.Manifest
	raw, ok := box.files[myHandover+"/manifest.json"]
	if !ok {
		t.Fatal("no manifest was written")
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("the manifest does not parse: %v", err)
	}
	if m.Slot != clipboard.HandoverSlot("marie") || m.To != "marie" {
		t.Errorf("the manifest does not record the recipient: %+v", m)
	}

	// And the slot is shared with them, read-only.
	if !strings.Contains(box.lastPostBody(), `"marie"`) {
		t.Errorf("the slot was not shared with the recipient: %s", box.lastPostBody())
	}
	if !strings.Contains(stderr, "paste --from") {
		t.Errorf("the message does not say how they collect it: %s", stderr)
	}
}

func TestCopyToStagesACERNBoxPathRatherThanPointingAtIt(t *testing.T) {
	box := newTestBox(t)
	box.putFile("/eos/user/e/einstein/report.pdf", "contents")

	if _, _, err := run(t, box, "copy", "cb:/eos/user/e/einstein/report.pdf", "--to", "marie"); err != nil {
		t.Fatalf("copy --to: %v", err)
	}

	// An ordinary copy would point at the path and move nothing. A recipient
	// cannot read the sender's own paths, so a handover duplicates it into the
	// shared slot instead — server-side, which is why this is a COPY and not an
	// upload.
	copied := false
	for _, r := range box.requests {
		if strings.HasPrefix(r, "COPY ") {
			copied = true
		}
		if strings.HasPrefix(r, "PUT "+testDavPrefix+"/eos/user/e/einstein/.cernbox/clipboard/to-marie/payload") {
			t.Errorf("the bytes went through this process: %v", box.requests)
		}
	}
	if !copied {
		t.Fatalf("no server-side copy was made: %v", box.requests)
	}

	var m clipboard.Manifest
	if err := json.Unmarshal([]byte(box.files[myHandover+"/manifest.json"]), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 || !m.Entries[0].Staged {
		t.Errorf("the entry should be staged, not referenced: %+v", m.Entries)
	}
	if !strings.HasPrefix(m.Entries[0].Path, myHandover) {
		t.Errorf("the entry points outside the shared slot: %q", m.Entries[0].Path)
	}
}

func TestCopyToRefusesWhatWouldShareTooMuch(t *testing.T) {
	box := newTestBox(t)
	dir := t.TempDir()
	local := filepath.Join(dir, "report.pdf")
	writeFileOrFail(t, local, "contents")

	// --slot would let a handover be staged in a slot the rest of the user's
	// copies go to, which would hand over the next thing they copied as well.
	_, _, err := run(t, box, "copy", local, "--to", "marie", "--slot", "build")
	if err == nil || cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("error = %v, want a usage error for --to with --slot", err)
	}

	// A username that is not one path segment would put the slot outside the
	// clipboard altogether.
	_, _, err = run(t, box, "copy", local, "--to", "../elsewhere")
	if err == nil || cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("error = %v, want a usage error for a username with a separator", err)
	}
	if len(box.files) != 0 {
		t.Errorf("a refused handover wrote something: %v", box.snapshotFiles())
	}
}

// handoverBox is a box where "other" has staged a handover for the test user.
func handoverBox(t *testing.T) *testBox {
	t.Helper()

	box := newTestBox(t)
	box.handoverFrom = "other"
	box.handoverSpace = otherSpace
	box.putFile(einsteinSlot+"/payload/notes.txt", "from the sender")

	m := clipboard.New(clipboard.HandoverSlot("einstein"), time.Now(), 0,
		clipboard.Origin{Host: "sender-host", User: "other"})
	m.To = "einstein"
	m.Entries = []clipboard.Entry{{
		Name: "notes.txt", Path: einsteinSlot + "/payload/notes.txt",
		Size: int64(len("from the sender")), Staged: true,
	}}
	raw, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	box.putFile(einsteinSlot+"/manifest.json", string(raw))
	return box
}

func TestPasteFromReadsTheSendersSlot(t *testing.T) {
	box := handoverBox(t)
	dir := t.TempDir()
	t.Chdir(dir)

	stdout, stderr, err := run(t, box, "paste", "--from", "other")
	if err != nil {
		t.Fatalf("paste --from: %v (%s%s)", err, stdout, stderr)
	}

	if got := readFileOrFail(t, filepath.Join(dir, "notes.txt")); got != "from the sender" {
		t.Errorf("the pasted file reads %q", got)
	}
	// Clearing is the sender's business: the bytes are on their quota, and the
	// recipient running 'clipboard clear' would clear a slot of their own that
	// does not exist.
	if !strings.Contains(stderr, "other's clipboard") {
		t.Errorf("the message does not say whose clipboard holds it: %s", stderr)
	}
}

func TestPasteFromNeedsSomethingToHaveBeenSent(t *testing.T) {
	box := newTestBox(t) // nobody has staged anything

	_, _, err := run(t, box, "paste", "--from", "other")
	if err == nil || cberr.ExitCode(err) != cberr.ExitNotFound {
		t.Fatalf("error = %v, want a not-found error", err)
	}
	// The error says what the other person has to type, since that is the only
	// thing the recipient can act on.
	if !strings.Contains(err.Error(), "copy --to einstein") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

func TestPasteFromRefusesASlotName(t *testing.T) {
	box := handoverBox(t)

	_, _, err := run(t, box, "paste", "--from", "other", "--slot", "build")
	if err == nil || cberr.ExitCode(err) != cberr.ExitUsage {
		t.Fatalf("error = %v, want a usage error", err)
	}
}

func TestClipboardListShowsWhatOthersHaveSent(t *testing.T) {
	box := handoverBox(t)

	stdout, stderr, err := run(t, box, "clipboard", "list")
	if err != nil {
		t.Fatalf("clipboard list: %v", err)
	}

	// Without this, the only way to learn somebody sent you something is for them
	// to tell you.
	if !strings.Contains(stdout, "to-einstein") || !strings.Contains(stdout, "other") {
		t.Errorf("the listing does not show the incoming handover:\n%s", stdout)
	}
	if !strings.Contains(stderr, "paste --from other") {
		t.Errorf("the listing does not say how to collect it: %s", stderr)
	}
}

func TestClipboardListJSONKeepsItsShape(t *testing.T) {
	box := handoverBox(t)

	stdout, _, err := run(t, box, "--output", "json", "clipboard", "list")
	if err != nil {
		t.Fatalf("clipboard list: %v", err)
	}

	var slots []map[string]any
	if err := json.Unmarshal([]byte(stdout), &slots); err != nil {
		t.Fatalf("the listing is not one JSON array: %v\n%s", err, stdout)
	}
	if len(slots) != 1 {
		t.Fatalf("expected one slot, got %d: %s", len(slots), stdout)
	}
	// The manifest fields are still at the top level, with "from" added for a
	// slot that is somebody else's.
	if slots[0]["slot"] != "to-einstein" || slots[0]["from"] != "other" {
		t.Errorf("the JSON shape changed: %v", slots[0])
	}
	if _, ok := slots[0]["entries"]; !ok {
		t.Errorf("the manifest fields are no longer inline: %v", slots[0])
	}
}
