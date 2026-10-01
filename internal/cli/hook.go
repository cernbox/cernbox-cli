package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// Hooks are what makes an outbox or an inbox worth pointing at a folder: the
// transfer is rarely the whole job. An inbox runs one for each file it
// collected, an outbox for each file it is about to send.
//
// Both go through here so the two cannot drift. The rules they share are worth
// stating once:
//
// The command is split on spaces and started directly, never handed to a shell.
// The names come from whoever is putting things in the folder — with an upload
// link, a stranger — so a file name must not be able to reach an interpreter.
// Anything that needs shell features goes in a script.
//
// Both output streams go to standard error, so a hook that prints cannot land
// in the middle of a --output json stream.
//
// And a failure means the step the hook was a precondition for does not happen.
// What that leaves behind differs — an inbox keeps the CERNBox copy, an outbox
// keeps the local one — but in both cases the file stays where it was, which is
// what tells somebody that it is unfinished.

const (
	// defaultExecTimeout bounds a hook. One that hangs would otherwise stop an
	// unattended run for ever, and the folder would quietly stop being handled
	// with nothing to show why.
	defaultExecTimeout = 5 * time.Minute

	// hookWaitDelay is how long Wait may go on waiting after the process has
	// been killed.
	//
	// Killing it is not enough on its own: Wait also waits for the pipes behind
	// the hook's output to close, and a grandchild keeps them open after its
	// parent is gone. A hook that ran "sleep 60" took the whole minute despite a
	// timeout of 300 milliseconds.
	hookWaitDelay = time.Second
)

// runHook runs one hook over one file, with path as the last argument.
//
// env holds the hook's own variables, which the caller names: a script gets the
// details without having to parse anything out of its arguments.
func (a *App) runHook(ctx context.Context, command []string, timeout time.Duration, path string, env []string) error {
	if len(command) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append(append([]string{}, command[1:]...), path)
	cmd := exec.CommandContext(ctx, command[0], args...)
	cmd.Stdout, cmd.Stderr = a.stderr, a.stderr
	cmd.WaitDelay = hookWaitDelay
	cmd.Env = append(os.Environ(), env...)

	err := cmd.Run()
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return cberr.New(cberr.KindOther, "", "",
			command[0]+" did not finish within "+timeout.String())
	default:
		// Told apart because they mean different things to whoever reads it: a
		// hook that could not be started is a path or a permission to fix, and
		// one that exited non-zero did its job and said no.
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return cberr.New(cberr.KindOther, "", "",
				fmt.Sprintf("%s exited with status %d", command[0], exit.ExitCode()))
		}
		return cberr.Wrap(cberr.KindOther, "run", command[0], err)
	}
}
