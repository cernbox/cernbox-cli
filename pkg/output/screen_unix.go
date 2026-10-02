//go:build !windows

package output

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize calls fire whenever the terminal changes size, until stop is
// called. Unix terminals say so with SIGWINCH.
func watchResize(_ *os.File, fire func()) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		for range sig {
			fire()
		}
	}()
	return func() { signal.Stop(sig) }
}
