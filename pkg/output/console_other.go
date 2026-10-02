//go:build !windows

package output

// EnableVirtualTerminal is a no-op outside Windows: every terminal there
// interprets escape sequences already.
func EnableVirtualTerminal() {}
