package output

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableVirtualTerminal turns on ANSI escape handling for the console behind
// standard output and standard error.
//
// Colour, progress bars and the full-screen browsers are all written as escape
// sequences. Windows Terminal interprets them regardless, but the classic
// console host prints them as literal "←[33m" unless a program opts in, and Go
// does not opt in on a program's behalf. A handle that is not a console, or a
// console too old to understand the mode, is left as it was.
func EnableVirtualTerminal() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		h := windows.Handle(f.Fd())
		var mode uint32
		if windows.GetConsoleMode(h, &mode) != nil {
			continue
		}
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
