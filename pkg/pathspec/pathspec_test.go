package pathspec

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseRemote(t *testing.T) {
	tests := []struct {
		name      string
		arg       string
		wantSpace string
		wantPath  string
		wantSlash bool
	}{
		{"absolute eos path", "/eos/user/g/gdelmont/Documents", "", "/eos/user/g/gdelmont/Documents", false},
		{"namespace root", "/", "", "/", false},
		{"cb prefix stripped", "cb:/eos/user/g/gdelmont", "", "/eos/user/g/gdelmont", false},
		{"trailing slash recorded", "/eos/project/c/cernbox/", "", "/eos/project/c/cernbox", true},
		{"dot segments collapsed", "/eos/user/g/./gdelmont/../gdelmont/Docs", "", "/eos/user/g/gdelmont/Docs", false},
		{"home alias", "home:Documents", "home", "Documents", false},
		{"home alias with leading slash", "home:/Documents", "home", "Documents", false},
		{"home alias bare", "home:", "home", "", false},
		{"project alias", "project/cernbox:data/raw", "project/cernbox", "data/raw", false},
		{"relative resolves against home", "Documents/notes.txt", "home", "Documents/notes.txt", false},
		{"cb prefix with alias", "cb:home:Documents", "home", "Documents", false},
		{"space id alias", "1284d238-aa92:sub/dir", "1284d238-aa92", "sub/dir", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRemote(tt.arg)
			if err != nil {
				t.Fatalf("ParseRemote(%q) returned error: %v", tt.arg, err)
			}
			if !got.IsRemote() {
				t.Errorf("ParseRemote(%q).Kind = %v, want remote", tt.arg, got.Kind)
			}
			if got.Space != tt.wantSpace {
				t.Errorf("ParseRemote(%q).Space = %q, want %q", tt.arg, got.Space, tt.wantSpace)
			}
			if got.Path != tt.wantPath {
				t.Errorf("ParseRemote(%q).Path = %q, want %q", tt.arg, got.Path, tt.wantPath)
			}
			if got.TrailingSlash != tt.wantSlash {
				t.Errorf("ParseRemote(%q).TrailingSlash = %v, want %v", tt.arg, got.TrailingSlash, tt.wantSlash)
			}
			if got.Raw != tt.arg {
				t.Errorf("ParseRemote(%q).Raw = %q, want the original argument", tt.arg, got.Raw)
			}
		})
	}
}

func TestParseRemoteErrors(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		wantSub string
	}{
		{"empty", "", "empty path"},
		{"cb prefix with nothing after it", "cb:", "no path after"},
		{"file prefix rejected", "file:./local.txt", "only works on CERNBox paths"},
		{"escaping absolute path", "/eos/../../etc/passwd", "escapes the namespace root"},
		{"escaping alias path", "home:../../etc/passwd", "escapes the space root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRemote(tt.arg)
			if err == nil {
				t.Fatalf("ParseRemote(%q) succeeded, want an error", tt.arg)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("ParseRemote(%q) error = %q, want it to mention %q", tt.arg, err, tt.wantSub)
			}
		})
	}
}

// TestParseTransferDisambiguation is the core of this package: on lxplus an
// absolute /eos path is a local FUSE mount unless it is explicitly marked.
func TestParseTransferDisambiguation(t *testing.T) {
	tests := []struct {
		name      string
		arg       string
		wantKind  Kind
		wantPath  string
		wantSpace string
	}{
		{"bare eos path is LOCAL", "/eos/user/g/gdelmont/a.txt", Local, "/eos/user/g/gdelmont/a.txt", ""},
		{"cb-marked eos path is remote", "cb:/eos/user/g/gdelmont/a.txt", Remote, "/eos/user/g/gdelmont/a.txt", ""},
		{"relative local path", "./report.pdf", Local, "report.pdf", ""},
		{"plain local path", "report.pdf", Local, "report.pdf", ""},
		{"local absolute path", "/tmp/report.pdf", Local, "/tmp/report.pdf", ""},
		{"space alias is remote without cb", "home:Documents", Remote, "Documents", "home"},
		{"project alias is remote", "project/cernbox:data", Remote, "data", "project/cernbox"},
		{"file prefix forces local", "file:home:weird", Local, "home:weird", ""},
		{"windows drive letter stays local", `C:\Users\gdelmont\a.txt`, Local, `C:\Users\gdelmont\a.txt`, ""},
		{"colon inside a relative file name stays local", "./weird:name.txt", Local, "weird:name.txt", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTransfer(tt.arg)
			if err != nil {
				t.Fatalf("ParseTransfer(%q) returned error: %v", tt.arg, err)
			}
			if got.Kind != tt.wantKind {
				t.Errorf("ParseTransfer(%q).Kind = %v, want %v", tt.arg, got.Kind, tt.wantKind)
			}
			if got.Path != tt.wantPath {
				t.Errorf("ParseTransfer(%q).Path = %q, want %q", tt.arg, got.Path, tt.wantPath)
			}
			if got.Space != tt.wantSpace {
				t.Errorf("ParseTransfer(%q).Space = %q, want %q", tt.arg, got.Space, tt.wantSpace)
			}
		})
	}
}

// TestParseTransferAliasAmbiguity pins a known, deliberate false positive:
// "data/archive:2024" is shaped exactly like a project space alias, so it is
// read as one. There is no way to tell the two apart from the string alone,
// which is why the file: prefix exists.
func TestParseTransferAliasAmbiguity(t *testing.T) {
	got, err := ParseTransfer("data/archive:2024")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsRemote() || got.Space != "data/archive" {
		t.Errorf("got %+v, want it read as the space alias data/archive", got)
	}

	escaped, err := ParseTransfer("file:data/archive:2024")
	if err != nil {
		t.Fatal(err)
	}
	if !escaped.IsLocal() || escaped.Path != "data/archive:2024" {
		t.Errorf("file: prefix did not force a local path, got %+v", escaped)
	}
}

func TestParseTransferTrailingSlash(t *testing.T) {
	withSlash, err := ParseTransfer("cb:/eos/user/g/gdelmont/Documents/")
	if err != nil {
		t.Fatal(err)
	}
	if !withSlash.TrailingSlash {
		t.Error("trailing slash was not recorded, cp-style destination semantics depend on it")
	}

	withoutSlash, err := ParseTransfer("cb:/eos/user/g/gdelmont/Documents")
	if err != nil {
		t.Fatal(err)
	}
	if withoutSlash.TrailingSlash {
		t.Error("TrailingSlash set on a path that has none")
	}
	if withSlash.Path != withoutSlash.Path {
		t.Errorf("trailing slash changed the path: %q vs %q", withSlash.Path, withoutSlash.Path)
	}
}

func TestParseTransferPair(t *testing.T) {
	t.Run("local to remote", func(t *testing.T) {
		src, dst, err := ParseTransferPair("./a.txt", "cb:/eos/user/g/gdelmont/")
		if err != nil {
			t.Fatal(err)
		}
		if !src.IsLocal() || !dst.IsRemote() {
			t.Errorf("got src=%v dst=%v, want local → remote", src.Kind, dst.Kind)
		}
	})

	t.Run("remote to local", func(t *testing.T) {
		src, dst, err := ParseTransferPair("cb:/eos/user/g/gdelmont/a.txt", "./")
		if err != nil {
			t.Fatal(err)
		}
		if !src.IsRemote() || !dst.IsLocal() {
			t.Errorf("got src=%v dst=%v, want remote → local", src.Kind, dst.Kind)
		}
	})

	// The lxplus trap: both sides look like CERNBox paths but neither is marked.
	t.Run("two bare eos paths are rejected with advice", func(t *testing.T) {
		_, _, err := ParseTransferPair("/eos/user/g/gdelmont/a.txt", "/eos/user/g/gdelmont/b.txt")
		if err == nil {
			t.Fatal("expected an error for two unmarked paths")
		}
		if !strings.Contains(err.Error(), "cb:") {
			t.Errorf("error should suggest the cb: prefix, got %q", err)
		}
	})

	t.Run("two remote paths are allowed", func(t *testing.T) {
		src, dst, err := ParseTransferPair("cb:/eos/a", "home:b")
		if err != nil {
			t.Fatal(err)
		}
		if !src.IsRemote() || !dst.IsRemote() {
			t.Error("both sides should be remote")
		}
	})
}

// stubResolver resolves a fixed set of aliases.
type stubResolver struct {
	spaces map[string]string
	calls  int
}

func (s *stubResolver) ResolveSpace(_ context.Context, alias string) (string, error) {
	s.calls++
	root, ok := s.spaces[alias]
	if !ok {
		return "", errors.New("no such space")
	}
	return root, nil
}

func TestResolve(t *testing.T) {
	r := &stubResolver{spaces: map[string]string{
		"home":            "/eos/user/g/gdelmont",
		"project/cernbox": "/eos/project/c/cernbox",
	}}
	ctx := context.Background()

	tests := []struct {
		arg  string
		want string
	}{
		{"home:Documents", "/eos/user/g/gdelmont/Documents"},
		{"home:", "/eos/user/g/gdelmont"},
		{"project/cernbox:data/raw", "/eos/project/c/cernbox/data/raw"},
		{"Documents/notes.txt", "/eos/user/g/gdelmont/Documents/notes.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			s, err := ParseRemote(tt.arg)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Resolve(ctx, r)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}

func TestResolveAbsoluteDoesNotCallResolver(t *testing.T) {
	r := &stubResolver{spaces: map[string]string{}}
	s, err := ParseRemote("/eos/user/g/gdelmont")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/eos/user/g/gdelmont" {
		t.Errorf("Resolve = %q, want the path unchanged", got)
	}
	if r.calls != 0 {
		t.Errorf("resolver was called %d times for an absolute path, want 0", r.calls)
	}
}

func TestResolveErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("local spec", func(t *testing.T) {
		s, err := ParseTransfer("./a.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Resolve(ctx, &stubResolver{}); err == nil {
			t.Error("Resolve on a local spec should fail")
		}
	})

	t.Run("unknown space", func(t *testing.T) {
		s, err := ParseRemote("project/nope:data")
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Resolve(ctx, &stubResolver{spaces: map[string]string{}})
		if err == nil {
			t.Fatal("Resolve should fail for an unknown space")
		}
		if !strings.Contains(err.Error(), "project/nope") {
			t.Errorf("error should name the space, got %q", err)
		}
	})

	t.Run("nil resolver", func(t *testing.T) {
		s, err := ParseRemote("home:Documents")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Resolve(ctx, nil); err == nil {
			t.Error("Resolve with a nil resolver should fail")
		}
	})
}

func TestJoin(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		elem string
		want string
	}{
		{"absolute", "/eos/user/g/gdelmont", "Documents", "/eos/user/g/gdelmont/Documents"},
		{"alias", "home:Documents", "notes.txt", "Documents/notes.txt"},
		{"root", "/", "eos", "/eos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseRemote(tt.arg)
			if err != nil {
				t.Fatal(err)
			}
			if got := Join(s, tt.elem); got.Path != tt.want {
				t.Errorf("Join(%q, %q).Path = %q, want %q", tt.arg, tt.elem, got.Path, tt.want)
			}
		})
	}
}

func TestBase(t *testing.T) {
	tests := []struct {
		arg  string
		want string
	}{
		{"/eos/user/g/gdelmont/notes.txt", "notes.txt"},
		{"home:Documents/notes.txt", "notes.txt"},
		{"project/cernbox:", "cernbox"},
		{"/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			s, err := ParseRemote(tt.arg)
			if err != nil {
				t.Fatal(err)
			}
			if got := Base(s); got != tt.want {
				t.Errorf("Base(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, arg := range []string{
		"/eos/user/g/gdelmont/Documents",
		"home:Documents",
		"project/cernbox:data/raw",
	} {
		t.Run(arg, func(t *testing.T) {
			s, err := ParseRemote(arg)
			if err != nil {
				t.Fatal(err)
			}
			reparsed, err := ParseRemote(s.String())
			if err != nil {
				t.Fatalf("re-parsing %q: %v", s.String(), err)
			}
			if reparsed.Path != s.Path || reparsed.Space != s.Space {
				t.Errorf("round trip changed the spec: %+v → %q → %+v", s, s.String(), reparsed)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	if Remote.String() != "remote" || Local.String() != "local" {
		t.Errorf("Kind.String is wrong: %q %q", Remote, Local)
	}
}
