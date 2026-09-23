package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/client"
)

// TestColumniseFillsDownThenAcross: ls orders a multi-column listing by column,
// not by row, so reading straight down gives alphabetical order.
func TestColumniseFillsDownThenAcross(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f"}

	// Width for three columns of one character plus two-space gaps.
	lines := columnise(names, names, 9, false)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if lines[0] != "a  c  e" {
		t.Errorf("first line = %q, want %q", lines[0], "a  c  e")
	}
	if lines[1] != "b  d  f" {
		t.Errorf("second line = %q, want %q", lines[1], "b  d  f")
	}
}

// TestColumniseOnePerLineWhenPiped: width 0 stands for "not a terminal", and a
// piped listing must be one name per line so that read/xargs work.
func TestColumniseOnePerLineWhenPiped(t *testing.T) {
	names := []string{"a", "b", "c"}
	for _, lines := range [][]string{
		columnise(names, names, 0, false),
		columnise(names, names, 200, true),
	} {
		if len(lines) != len(names) {
			t.Fatalf("got %d lines, want one per name: %v", len(lines), lines)
		}
		for i, l := range lines {
			if l != names[i] {
				t.Errorf("line %d = %q, want %q with no padding", i, l, names[i])
			}
		}
	}
}

// TestColumniseNoTrailingWhitespace: trailing padding is invisible but ends up
// in anything that captures the output.
func TestColumniseNoTrailingWhitespace(t *testing.T) {
	plain := []string{"short", "muchlongername", "mid"}
	for _, l := range columnise(plain, plain, 40, false) {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("line %q has trailing whitespace", l)
		}
	}
}

func TestModeString(t *testing.T) {
	tests := []struct {
		name string
		in   client.ResourceInfo
		want string
	}{
		// The strings a real reva returns.
		{"writable file", client.ResourceInfo{Permissions: "RGDNVWZO"}, "-rw-"},
		{"writable dir", client.ResourceInfo{IsDir: true, Permissions: "RGDNVCKZ"}, "drwx"},
		{"read-only file", client.ResourceInfo{Permissions: "RG"}, "-r--"},
		{"read-only dir", client.ResourceInfo{IsDir: true, Permissions: "RG"}, "dr-x"},
		// No rights reported at all is not the same as no rights.
		{"unknown", client.ResourceInfo{}, "-???"},
		{"unknown dir", client.ResourceInfo{IsDir: true}, "d???"},
	}
	for _, tt := range tests {
		if got := modeString(tt.in); got != tt.want {
			t.Errorf("%s: modeString() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestSortEntries(t *testing.T) {
	mk := func(name string, size int64, min int) client.ResourceInfo {
		return client.ResourceInfo{
			Path: "/" + name, Name: name, Size: size,
			Modified: time.Unix(1700000000, 0).Add(time.Duration(min) * time.Minute),
		}
	}
	// b is newest and largest, so each sort should pick a different order.
	base := []client.ResourceInfo{mk("a", 10, 0), mk("b", 30, 2), mk("c", 20, 1)}

	names := func(es []client.ResourceInfo) string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.Name
		}
		return strings.Join(out, ",")
	}

	tests := []struct {
		opts lsOptions
		want string
	}{
		{lsOptions{}, "a,b,c"},
		{lsOptions{SortTime: true}, "b,c,a"},
		{lsOptions{SortSize: true}, "b,c,a"},
		{lsOptions{Reverse: true}, "c,b,a"},
		{lsOptions{SortTime: true, Reverse: true}, "a,c,b"},
	}
	for _, tt := range tests {
		entries := append([]client.ResourceInfo(nil), base...)
		sortEntries(entries, tt.opts)
		if got := names(entries); got != tt.want {
			t.Errorf("sortEntries(%+v) = %s, want %s", tt.opts, got, tt.want)
		}
	}
}

// TestColumniseAlignsDespiteColour is the trap this arrangement exists to
// avoid: escape sequences make a name longer as a Go string while occupying no
// width on screen, so padding computed from the decorated name shears every
// column after the first.
func TestColumniseAlignsDespiteColour(t *testing.T) {
	plain := []string{"aa", "b", "cc", "d"}
	shown := []string{"\033[01;34maa\033[0m", "b", "\033[01;31mcc\033[0m", "d"}

	lines := columnise(plain, shown, 10, false)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), lines)
	}

	// Strip the escapes and the layout must be exactly what the plain names
	// would have produced.
	want := columnise(plain, plain, 10, false)
	for i, l := range lines {
		if got := stripANSI(l); got != want[i] {
			t.Errorf("line %d with colour = %q, want the same layout as %q", i, got, want[i])
		}
	}
}

// stripANSI removes SGR sequences, so a coloured line can be compared with the
// plain layout it must match.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\033' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // skip the m
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
