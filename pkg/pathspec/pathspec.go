// Package pathspec parses the path arguments accepted by the cernbox CLI and
// decides, for each one, whether it names a local file or a CERNBox resource.
//
// That decision is the whole reason this package exists. On lxplus a path like
// /eos/user/g/gdelmont is simultaneously a valid CERNBox path and a real local
// FUSE mount point, so "cernbox cp /eos/a /eos/b" cannot be disambiguated by
// looking at the string, and guessing would silently do the wrong thing. The
// CLI therefore splits its commands in two:
//
//   - Namespace commands (ls, rm, share, ...) have no local side at all, so
//     every path they take is remote. Use ParseRemote.
//   - Transfer commands (cp, sync) take a path as remote only when it is marked
//     with "cb:". Use ParseTransfer.
//
// The marker can also name a user, "marie@cb:", which makes the path that user's
// view of CERNBox: the CLI acts as them to reach it. That is the same shape as
// scp's user@host:path, and it is the only place an identity is written.
//
// Nothing outside this package may infer local-versus-remote, or whom a path
// is acted on as, by any other means.
package pathspec

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Kind distinguishes a local filesystem path from a CERNBox path.
type Kind int

const (
	// Local is a path on the machine running the CLI.
	Local Kind = iota
	// Remote is a path inside CERNBox.
	Remote
)

func (k Kind) String() string {
	if k == Remote {
		return "remote"
	}
	return "local"
}

// Prefixes recognised at the start of an argument.
const (
	// RemotePrefix forces an argument to be interpreted as a CERNBox path.
	RemotePrefix = "cb:"
	// LocalPrefix forces an argument to be interpreted as a local path. A
	// transfer never needs it, since anything without cb: is local there; it is
	// for edit, which looks for an unmarked path in CERNBox first.
	LocalPrefix = "file:"
)

// HomeShorthand stands for the home space after the marker: "cb:~/Documents".
// It only works there. Written first on the command line, the shell would expand
// it to the local home before the CLI saw it.
const HomeShorthand = "~"

// userRe matches the username in an identity marker, "marie@cb:".
var userRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// HomeAlias is the space alias for the caller's own home space. A relative
// remote path is resolved against it.
const HomeAlias = "home"

// aliasRe matches a space alias: a segment, optionally followed by more
// slash-separated segments, as in "home" or "project/cernbox".
//
// The two-character minimum on the first segment is deliberate: it stops a
// Windows drive letter ("C:\Users\...") from being read as a space alias.
var aliasRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// Spec is a parsed path argument.
type Spec struct {
	// Kind reports whether this is a local or a remote path.
	Kind Kind

	// Path is the cleaned path. For a Remote spec written absolutely it is the
	// absolute CERNBox path ("/eos/user/g/gdelmont/Documents"). For a Remote
	// spec written with a space alias it is the path relative to that space's
	// root, without a leading slash ("Documents"). For a Local spec it is the
	// local path as given, cleaned.
	Path string

	// Space is the space alias the argument was written with ("home",
	// "project/cernbox"), or empty when the argument was an absolute path or a
	// local path. Resolve turns it into an absolute path.
	Space string

	// As is the user the path was written for, "marie" in "marie@cb:~/x", or
	// empty for the identity the command runs as. The path, its space alias
	// included, is resolved as that user sees CERNBox.
	As string

	// TrailingSlash records whether the argument ended in a separator. Transfer
	// commands use it the way cp does: "put a.txt cb:/eos/x/dir/" means "into
	// dir", whereas "put a.txt cb:/eos/x/dir" means "as dir".
	TrailingSlash bool

	// Raw is the original argument, used in error messages so the user sees
	// what they typed.
	Raw string
}

// IsRemote reports whether s names a CERNBox resource.
func (s Spec) IsRemote() bool { return s.Kind == Remote }

// IsLocal reports whether s names a local file.
func (s Spec) IsLocal() bool { return s.Kind == Local }

// String renders the spec back into the syntax a user would type. A remote
// spec always carries the marker, so that what comes out parses back to the same
// thing in any command.
func (s Spec) String() string {
	var b strings.Builder
	if s.Kind == Local {
		b.WriteString(s.Path)
	} else {
		if s.As != "" {
			b.WriteString(s.As + "@")
		}
		b.WriteString(RemotePrefix)
		switch {
		case s.Space == HomeAlias:
			b.WriteString(HomeShorthand)
			if s.Path != "" {
				b.WriteString("/" + s.Path)
			}
		case s.Space != "":
			b.WriteString(s.Space + ":" + s.Path)
		default:
			b.WriteString(s.Path)
		}
	}
	if s.TrailingSlash && !strings.HasSuffix(b.String(), "/") {
		b.WriteString("/")
	}
	return b.String()
}

// SpaceResolver maps a space alias to the absolute CERNBox path of that space's
// root. pkg/client implements it against the spaces listing; tests use a stub.
type SpaceResolver interface {
	ResolveSpace(ctx context.Context, alias string) (string, error)
}

// Resolve returns the absolute CERNBox path for a remote spec, consulting r
// only when the spec was written with a space alias. It is an error to call
// Resolve on a local spec.
func (s Spec) Resolve(ctx context.Context, r SpaceResolver) (string, error) {
	if s.Kind != Remote {
		return "", fmt.Errorf("%q is a local path, it has no CERNBox location", s.Raw)
	}
	if s.Space == "" {
		return s.Path, nil
	}
	if r == nil {
		return "", fmt.Errorf("cannot resolve %q: no space resolver available", s.Raw)
	}
	root, err := r.ResolveSpace(ctx, s.Space)
	if err != nil {
		return "", fmt.Errorf("cannot resolve space %q from %q: %w", s.Space, s.Raw, err)
	}
	if s.Path == "" || s.Path == "." {
		return cleanAbs(root), nil
	}
	return cleanAbs(path.Join(root, s.Path)), nil
}

// ParseRemote parses an argument for a command that only ever operates on
// CERNBox. The marker is optional: an absolute path, a space alias, "~/" for the
// home space, and a relative path, which is taken relative to the home space,
// are all remote. "marie@cb:" in front makes it marie's view.
func ParseRemote(arg string) (Spec, error) {
	if arg == "" {
		return Spec{}, fmt.Errorf("empty path")
	}
	if rest, ok := strings.CutPrefix(arg, LocalPrefix); ok {
		return Spec{}, fmt.Errorf("%q names a local path, but this command only works on CERNBox paths", rest)
	}
	as, rest, _ := splitMarker(arg)
	return parseLocation(rest, as, arg)
}

// ParseTransfer parses an argument for a command that moves data between the
// local filesystem and CERNBox. The argument is remote only when it says so,
// with "cb:" or "USER@cb:". Everything else is local, including an absolute
// /eos path, which on lxplus is a real local mount.
func ParseTransfer(arg string) (Spec, error) {
	if arg == "" {
		return Spec{}, fmt.Errorf("empty path")
	}
	if rest, ok := strings.CutPrefix(arg, LocalPrefix); ok {
		if rest == "" {
			return Spec{}, fmt.Errorf("%q has no path after %q", arg, LocalPrefix)
		}
		return localSpec(rest, arg), nil
	}
	if as, rest, ok := splitMarker(arg); ok {
		return parseLocation(rest, as, arg)
	}
	return localSpec(arg, arg), nil
}

// parseLocation parses what follows the marker: a CERNBox location. An empty
// one is the home space, so that "cb:" and "marie@cb:" name a home the way "~"
// does in a shell.
func parseLocation(arg, as, raw string) (Spec, error) {
	trailing := hasTrailingSlash(arg)
	remote := func(space, p string) Spec {
		return Spec{Kind: Remote, Space: space, Path: p, As: as, TrailingSlash: trailing, Raw: raw}
	}

	if arg == "" || arg == HomeShorthand {
		return remote(HomeAlias, ""), nil
	}
	if rest, ok := strings.CutPrefix(arg, HomeShorthand+"/"); ok {
		cleaned, err := cleanRel(rest)
		if err != nil {
			return Spec{}, fmt.Errorf("%q: %w", raw, err)
		}
		return remote(HomeAlias, cleaned), nil
	}

	if alias, rest, ok := splitAlias(arg); ok {
		cleaned, err := cleanRel(rest)
		if err != nil {
			return Spec{}, fmt.Errorf("%q: %w", raw, err)
		}
		return remote(alias, cleaned), nil
	}

	if strings.HasPrefix(arg, "/") {
		cleaned, err := cleanAbsChecked(arg)
		if err != nil {
			return Spec{}, fmt.Errorf("%q: %w", raw, err)
		}
		return remote("", cleaned), nil
	}

	// A bare relative path is relative to the home space, so that
	// "cernbox ls Documents" does what it looks like it does.
	cleaned, err := cleanRel(arg)
	if err != nil {
		return Spec{}, fmt.Errorf("%q: %w", raw, err)
	}
	return remote(HomeAlias, cleaned), nil
}

// splitMarker recognises the remote marker at the start of arg, "cb:" or
// "USER@cb:", and returns the user it names, if any, and what follows it. An
// argument without the marker comes back whole, with ok false.
func splitMarker(arg string) (as, rest string, ok bool) {
	if rest, ok := strings.CutPrefix(arg, RemotePrefix); ok {
		return "", rest, true
	}
	user, rest, found := strings.Cut(arg, "@"+RemotePrefix)
	if !found || !userRe.MatchString(user) {
		return "", arg, false
	}
	return user, rest, true
}

// IsMarkedRemote reports whether arg carries the remote marker, "cb:" or
// "USER@cb:".
func IsMarkedRemote(arg string) bool {
	_, _, ok := splitMarker(arg)
	return ok
}

// Identity returns the user arg is written for, "marie" for "marie@cb:~/x".
func Identity(arg string) (string, bool) {
	as, _, _ := splitMarker(arg)
	return as, as != ""
}

// localSpec builds the spec for a local path. It is cleaned by the platform's
// rules rather than by CERNBox's, so that on Windows "C:\data\" keeps its
// volume and its trailing separator.
func localSpec(p, raw string) Spec {
	trailing := len(p) > 1 && (strings.HasSuffix(p, "/") || strings.HasSuffix(p, string(os.PathSeparator)))
	return Spec{Kind: Local, Path: filepath.Clean(p), TrailingSlash: trailing, Raw: raw}
}

// ParseTransferPair parses a source and destination and requires exactly one of
// them to be remote. Two local paths are a job for cp(1); two remote paths are
// a server-side copy, which the CLI exposes as "cernbox cp" on bare paths
// rather than through the transfer engine.
func ParseTransferPair(src, dst string) (Spec, Spec, error) {
	s, err := ParseTransfer(src)
	if err != nil {
		return Spec{}, Spec{}, err
	}
	d, err := ParseTransfer(dst)
	if err != nil {
		return Spec{}, Spec{}, err
	}
	if s.IsLocal() && d.IsLocal() {
		return Spec{}, Spec{}, fmt.Errorf(
			"both %q and %q are local paths: mark the CERNBox side with %q, for example %s%s",
			src, dst, RemotePrefix, RemotePrefix, dst)
	}
	return s, d, nil
}

// SplitForCompletion splits a partially typed argument into the text naming a
// directory and the fragment being typed inside it. The directory part comes
// back exactly as it was typed, prefix and alias included, so that a caller can
// join a candidate name onto it and get something the user could have typed.
//
//	"cb:/eos/user/g/gdelmont/Doc" -> "cb:/eos/user/g/gdelmont/", "Doc"
//	"cb:~/notes/dr"               -> "cb:~/notes/",              "dr"
//	"marie@cb:~/Do"               -> "marie@cb:~/",              "Do"
//	"home:"                       -> "home:",                    ""
//	"Doc"                         -> "",                         "Doc"
//
// The directory part names the home space when it is empty or only the marker,
// which is what ParseRemote makes of it too.
func SplitForCompletion(arg string) (dir, frag string) {
	prefix := ""
	if _, rest, ok := splitMarker(arg); ok {
		prefix, arg = arg[:len(arg)-len(rest)], rest
	}
	if rest, ok := strings.CutPrefix(arg, HomeShorthand+"/"); ok {
		prefix, arg = prefix+HomeShorthand+"/", rest
	} else if alias, rest, ok := splitAlias(arg); ok {
		prefix, arg = prefix+alias+":", rest
	}
	if i := strings.LastIndex(arg, "/"); i >= 0 {
		return prefix + arg[:i+1], arg[i+1:]
	}
	return prefix, arg
}

// Join returns a spec for a child of s.
func Join(s Spec, elems ...string) Spec {
	out := s
	if s.Kind == Local {
		out.Path = filepath.Join(append([]string{s.Path}, elems...)...)
	} else {
		out.Path = path.Join(append([]string{s.Path}, elems...)...)
	}
	if s.Kind == Remote && s.Space == "" && !strings.HasPrefix(out.Path, "/") {
		out.Path = "/" + out.Path
	}
	out.TrailingSlash = false
	out.Raw = out.String()
	return out
}

// Base returns the final element of the spec's path. For a space alias with an
// empty path it returns the last segment of the alias, so that
// "cernbox cp cb:project/cernbox: ." lands in a directory named "cernbox".
func Base(s Spec) string {
	if s.Path == "" || s.Path == "." || s.Path == "/" {
		if s.Space != "" {
			return path.Base(s.Space)
		}
		return ""
	}
	if s.Kind == Local {
		return filepath.Base(s.Path)
	}
	return path.Base(s.Path)
}

// splitAlias splits "home:Documents" into ("home", "Documents", true). It
// returns ok=false when arg carries no alias, or when the text before the colon
// is not shaped like one.
func splitAlias(arg string) (alias, rest string, ok bool) {
	i := strings.Index(arg, ":")
	if i <= 0 {
		return "", "", false
	}
	// A slash before the colon means the colon is part of a file name, as in
	// "./weird:name" — not an alias separator.
	if strings.Contains(arg[:i], "/") && !aliasRe.MatchString(arg[:i]) {
		return "", "", false
	}
	alias = arg[:i]
	if !aliasRe.MatchString(alias) {
		return "", "", false
	}
	return alias, strings.TrimPrefix(arg[i+1:], "/"), true
}

func hasTrailingSlash(arg string) bool {
	return len(arg) > 1 && strings.HasSuffix(arg, "/")
}

// cleanRel cleans a path that is relative to a space root and rejects any
// attempt to climb above it.
func cleanRel(p string) (string, error) {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", nil
	}
	cleaned := path.Clean(p)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes the space root")
	}
	if cleaned == "." {
		return "", nil
	}
	return cleaned, nil
}

// cleanAbsChecked cleans an absolute path and rejects one that climbs above the
// namespace root.
func cleanAbsChecked(p string) (string, error) {
	// path.Clean already collapses "/.." to "/", which would silently turn an
	// escaping path into the root, so count the climb before cleaning.
	if escapesRoot(p) {
		return "", fmt.Errorf("path escapes the namespace root")
	}
	return cleanAbs(p), nil
}

func escapesRoot(p string) bool {
	depth := 0
	for seg := range strings.SplitSeq(strings.TrimPrefix(p, "/"), "/") {
		switch seg {
		case "", ".":
		case "..":
			depth--
			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}
	return false
}

func cleanAbs(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}
