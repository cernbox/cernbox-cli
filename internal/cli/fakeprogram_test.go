package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Hooks and editors are somebody else's program run against a file on disk,
// and the tests that cover them run a real one rather than stubbing the call
// out: what is most likely to be wrong is how the process is started, what it
// is handed, and what it leaves behind.
//
// The program is the test binary itself. A shell script would be simpler to
// write, but Windows cannot start one, and a stand-in that only runs on some
// platforms leaves the hook and editor code untested on the others. So the test
// binary checks for a marker before running any test, and when it finds one it
// acts out a small script instead:
//
//	write   FILE TEXT    replace FILE's content with TEXT
//	append  FILE TEXT    add TEXT to the end of FILE
//	copy    FROM TO      replace TO's content with FROM's
//	rename  FROM TO      move FROM over TO
//	touch   FILE         set FILE's modification time to now
//	sleep   DURATION     wait, as in "400ms"
//	exit    CODE         stop with that exit status
//	true                 do nothing
//
// One command per line. An argument with spaces or escapes is written as a Go
// string literal ("one\n"). $1 is the path the CLI appended, and $NAME or
// ${NAME} an environment variable, which is how a hook sees CERNBOX_INBOX_*.
// A command that fails stops the script with status 1, as "set -e" would.
const fakeProgramMarker = "-cernbox-fake-program"

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == fakeProgramMarker {
		os.Exit(runFakeProgram(os.Args[2], os.Args[3:]))
	}
	os.Exit(m.Run())
}

// fakeProgram writes a script and returns the command that runs it, for
// --editor or --exec.
func fakeProgram(t *testing.T, script string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(p, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	// --editor and --exec split their value on spaces.
	if strings.ContainsAny(exe+p, " \t") {
		t.Skipf("the test binary or its temporary directory has a space in its path (%s), "+
			"which a command split on spaces cannot carry", exe)
	}
	return exe + " " + fakeProgramMarker + " " + p
}

// quote renders s as a script argument.
func quote(s string) string { return strconv.Quote(s) }

func runFakeProgram(script string, args []string) int {
	body, err := os.ReadFile(script)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	expand := func(s string) string {
		return os.Expand(s, func(name string) string {
			if name == "1" && len(args) > 0 {
				return args[len(args)-1]
			}
			return os.Getenv(name)
		})
	}
	for n, line := range strings.Split(string(body), "\n") {
		words, err := splitScriptLine(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "line %d: %v\n", n+1, err)
			return 1
		}
		if len(words) == 0 {
			continue
		}
		for i := range words {
			words[i] = expand(words[i])
		}
		code, err := runScriptCommand(words)
		if err != nil {
			fmt.Fprintf(os.Stderr, "line %d: %s: %v\n", n+1, words[0], err)
			return 1
		}
		if code >= 0 {
			return code
		}
	}
	return 0
}

// runScriptCommand runs one command. A non-negative code means stop with it.
func runScriptCommand(w []string) (int, error) {
	need := func(n int) error {
		if len(w) != n+1 {
			return fmt.Errorf("takes %d arguments, got %d", n, len(w)-1)
		}
		return nil
	}
	switch w[0] {
	case "true":
		return -1, nil
	case "write":
		if err := need(2); err != nil {
			return 0, err
		}
		return -1, os.WriteFile(w[1], []byte(w[2]), 0o644)
	case "append":
		if err := need(2); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(w[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		if _, err := f.WriteString(w[2]); err != nil {
			f.Close()
			return 0, err
		}
		return -1, f.Close()
	case "copy":
		if err := need(2); err != nil {
			return 0, err
		}
		data, err := os.ReadFile(w[1])
		if err != nil {
			return 0, err
		}
		return -1, os.WriteFile(w[2], data, 0o644)
	case "rename":
		if err := need(2); err != nil {
			return 0, err
		}
		// Windows refuses to replace a file somebody has open, and the CLI
		// polling the file it watches is somebody. It holds it only for a read.
		var err error
		for range 50 {
			if err = os.Rename(w[1], w[2]); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		return -1, err
	case "touch":
		if err := need(1); err != nil {
			return 0, err
		}
		now := time.Now()
		return -1, os.Chtimes(w[1], now, now)
	case "sleep":
		if err := need(1); err != nil {
			return 0, err
		}
		d, err := time.ParseDuration(w[1])
		if err != nil {
			return 0, err
		}
		time.Sleep(d)
		return -1, nil
	case "exit":
		if err := need(1); err != nil {
			return 0, err
		}
		return strconv.Atoi(w[1])
	default:
		return 0, fmt.Errorf("unknown command")
	}
}

// splitScriptLine splits a line on spaces, reading a word that starts with a
// double quote as a Go string literal.
func splitScriptLine(line string) ([]string, error) {
	var words []string
	s := strings.TrimSpace(line)
	for s != "" {
		if s[0] != '"' {
			word, rest, _ := strings.Cut(s, " ")
			words = append(words, word)
			s = strings.TrimSpace(rest)
			continue
		}
		end := 1
		for end < len(s) && s[end] != '"' {
			if s[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(s) {
			return nil, fmt.Errorf("unterminated string")
		}
		word, err := strconv.Unquote(s[:end+1])
		if err != nil {
			return nil, err
		}
		words = append(words, word)
		s = strings.TrimSpace(s[end+1:])
	}
	return words, nil
}
