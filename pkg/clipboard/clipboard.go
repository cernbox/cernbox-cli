// Package clipboard describes the cross-machine clipboard: a small manifest,
// kept in the user's own CERNBox home space, naming what was copied and where
// the bytes are.
//
// The manifest is the whole trick. "Copy here, paste there" needs some state
// both machines can see, and the only thing they reliably share is CERNBox
// itself — so the clipboard lives there rather than in a local file, which by
// definition the other machine cannot read.
//
// The format has a package of its own because it is the one part of the feature
// that two machines have to agree on. A slot written by the CLI on lxplus is
// read by whatever version happens to be installed on the laptop, which may be
// older or newer, so the encoding is versioned and every field is optional
// except the ones a reader cannot do without.
package clipboard

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path"
	"strings"
	"time"
)

const (
	// Dir is where the clipboard lives inside the home space. The leading dot
	// keeps it out of an ordinary listing, since "cernbox ls" hides dot entries
	// unless asked — a user who never touches this feature should never see it.
	Dir = ".cernbox/clipboard"

	// DefaultSlot is used when none is named, so that the common case is
	// "cernbox copy" on one machine and "cernbox paste" on the other with
	// nothing to remember in between.
	DefaultSlot = "default"

	// Version is the manifest format version. A reader refuses anything higher
	// rather than guessing at fields it does not know.
	Version = 1

	// DefaultTTL is how long a slot lives before it is eligible for collection.
	// Staged bytes sit in the user's own quota, so they cannot live forever; a
	// week is long enough to cover "copy on Friday, paste on Monday".
	DefaultTTL = 7 * 24 * time.Hour

	// MaxManifestSize bounds what a reader will accept. The manifest is metadata
	// about a handful of entries, so anything larger is corrupt or hostile, and
	// reading it into memory unbounded would be the bug.
	MaxManifestSize = 1 << 20

	manifestName = "manifest.json"
	payloadName  = "payload"
)

// Manifest is what one clipboard slot holds.
type Manifest struct {
	// Version is the format version this was written with.
	Version int `json:"version"`
	// Slot is the slot name, repeated inside the file so a manifest read on its
	// own still says where it belongs.
	Slot string `json:"slot"`
	// Created is when the copy was made.
	Created time.Time `json:"created"`
	// Expires is when the slot becomes eligible for collection. Zero means it
	// never expires, which is what --ttl 0 asks for.
	Expires time.Time `json:"expires,omitzero"`
	// Origin records the machine that did the copying.
	Origin Origin `json:"origin"`
	// Entries are what was copied, in the order the arguments were given.
	Entries []Entry `json:"entries"`
}

// Entry is one copied file or directory.
type Entry struct {
	// Name is what the entry is called, and what it is pasted as.
	Name string `json:"name"`
	// Path is the absolute CERNBox path where the bytes are. For a staged entry
	// that is inside the slot's own payload directory; for a referenced one it is
	// wherever the file already lived.
	Path string `json:"path"`
	// IsDir reports whether this is a directory.
	IsDir bool `json:"is_dir"`
	// Size is the byte count, recursive for a directory.
	Size int64 `json:"size"`
	// Staged reports whether the CLI uploaded these bytes for the clipboard.
	//
	// This is the difference between the two kinds of copy, and it matters on
	// paste as much as on clear. A staged entry is a private duplicate that
	// clearing the slot should delete; a referenced one is the user's actual
	// file, which clearing must leave alone.
	Staged bool `json:"staged"`
	// Parts is how many pieces the entry was split into, and zero when it is a
	// single object at Path — which is every entry except a stream too large to
	// send in one request.
	//
	// When it is non-zero, Path is a directory and the pieces are the files
	// PartPath names, to be joined in order. Splitting is what lets a pipe be
	// copied at all: its length cannot be known in advance, and every way of
	// sending bytes to reva needs the length up front, so the stream is cut into
	// lengths that are known by the time each piece is sent.
	Parts int `json:"parts,omitempty"`
	// ETag identifies the version that was copied. On a referenced entry it is
	// how paste can tell that the source has changed since, which is worth
	// saying out loud rather than silently handing over different bytes.
	ETag string `json:"etag,omitempty"`
}

// Origin is the machine a copy was made on.
//
// It is recorded because a clipboard shared between every machine an account
// touches is otherwise ambiguous: "default holds report.pdf" is far less useful
// than knowing it was put there from lxplus812 twenty minutes ago.
type Origin struct {
	Host string `json:"host,omitempty"`
	User string `json:"user,omitempty"`
	// Dir is the working directory the copy was made from, which is often the
	// only thing that distinguishes two files of the same name.
	Dir string `json:"dir,omitempty"`
}

// LocalOrigin describes the machine this process is running on. Every field is
// best effort: none of them is load-bearing, and a hostname that cannot be read
// is not a reason to refuse to copy a file.
func LocalOrigin(cwd string) Origin {
	var o Origin
	if host, err := os.Hostname(); err == nil {
		o.Host = host
	}
	if u, err := user.Current(); err == nil {
		o.User = u.Username
	}
	o.Dir = cwd
	return o
}

// String renders an origin for a listing.
func (o Origin) String() string {
	switch {
	case o.Host != "" && o.User != "":
		return o.User + "@" + o.Host
	case o.Host != "":
		return o.Host
	case o.User != "":
		return o.User
	default:
		return "-"
	}
}

// New returns a manifest for slot, expiring after ttl. A ttl of zero or less
// means no expiry.
func New(slot string, now time.Time, ttl time.Duration, origin Origin) *Manifest {
	m := &Manifest{
		Version: Version,
		Slot:    slot,
		Created: now,
		Origin:  origin,
	}
	if ttl > 0 {
		m.Expires = now.Add(ttl)
	}
	return m
}

// Size is the total size of everything in the slot.
func (m *Manifest) Size() int64 {
	var total int64
	for _, e := range m.Entries {
		total += e.Size
	}
	return total
}

// StagedSize is how much of the slot is a duplicate the CLI made, and therefore
// how much quota clearing it would return.
func (m *Manifest) StagedSize() int64 {
	var total int64
	for _, e := range m.Entries {
		if e.Staged {
			total += e.Size
		}
	}
	return total
}

// HasStaged reports whether any bytes were uploaded for this slot.
func (m *Manifest) HasStaged() bool {
	for _, e := range m.Entries {
		if e.Staged {
			return true
		}
	}
	return false
}

// Expired reports whether the slot is past its expiry.
func (m *Manifest) Expired(now time.Time) bool {
	return !m.Expires.IsZero() && now.After(m.Expires)
}

// Find returns the entry with the given name.
func (m *Manifest) Find(name string) (Entry, bool) {
	for _, e := range m.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Encode renders the manifest for storage.
func (m *Manifest) Encode() ([]byte, error) {
	if m.Version == 0 {
		m.Version = Version
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the clipboard manifest: %w", err)
	}
	return append(b, '\n'), nil
}

// Decode parses a stored manifest.
//
// A manifest from a newer CLI is refused rather than read as far as it parses:
// the fields this version does not know about could be the ones saying where
// the bytes are, and pasting half of a copy is worse than declining to.
func Decode(b []byte) (*Manifest, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("the clipboard manifest is empty")
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("the clipboard manifest is not readable: %w", err)
	}
	if m.Version > Version {
		return nil, fmt.Errorf(
			"this clipboard was written by a newer cernbox (format %d, this one reads %d): upgrade to paste it",
			m.Version, Version)
	}
	for i, e := range m.Entries {
		if e.Name == "" || e.Path == "" {
			return nil, fmt.Errorf("the clipboard manifest is incomplete: entry %d has no name or no location", i+1)
		}
		if e.Parts < 0 {
			return nil, fmt.Errorf("the clipboard manifest is invalid: entry %q claims %d parts", e.Name, e.Parts)
		}
	}
	return &m, nil
}

// ValidateSlot checks a slot name.
//
// A slot becomes a directory name under the clipboard root, so a name with a
// separator or a dot-dot in it would write outside the clipboard entirely. This
// is the only place that is checked, so it is deliberately strict rather than
// clever: names are one plain segment.
func ValidateSlot(name string) error {
	const maxLen = 64
	switch {
	case name == "":
		return fmt.Errorf("the clipboard slot name is empty")
	case len(name) > maxLen:
		return fmt.Errorf("the clipboard slot name is longer than %d characters", maxLen)
	case name == "." || name == "..":
		return fmt.Errorf("%q is not a usable clipboard slot name", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("the clipboard slot name %q cannot contain a path separator", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf(
				"the clipboard slot name %q contains %q: use letters, digits, '-', '_' and '.'", name, r)
		}
	}
	return nil
}

// SlotDir is the directory holding one slot.
func SlotDir(root, slot string) string { return path.Join(root, slot) }

// ManifestPath is where a slot's manifest is stored.
func ManifestPath(root, slot string) string { return path.Join(root, slot, manifestName) }

// PayloadDir is where bytes staged for a slot are stored.
func PayloadDir(root, slot string) string { return path.Join(root, slot, payloadName) }

// IsManifest reports whether name is the manifest file, so a listing of a slot
// can tell metadata from payload.
func IsManifest(name string) bool { return name == manifestName }

// PartPath returns where the n-th piece of a split entry is stored, given the
// entry's Path.
//
// The pieces sit inside a directory of their own rather than alongside each other
// under suffixed names, so that releasing them is one recursive delete instead of
// one request per piece — which for a large stream is the difference between a
// single request and thousands.
//
// The number is zero-padded so that a plain listing of the directory is in the
// order the pieces have to be joined, which is what makes a half-finished stream
// something a person can look at and understand.
func PartPath(base string, n int) string {
	return path.Join(base, fmt.Sprintf("%05d", n))
}
