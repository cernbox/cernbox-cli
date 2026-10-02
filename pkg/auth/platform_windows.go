package auth

import (
	"io/fs"
	"os"
	"path/filepath"
)

// userSuffix is empty: the temporary directory on Windows is already the
// user's own, under their profile, so there is nobody to tell apart. A uid
// would not work anyway, because Windows has none and os.Getuid returns -1.
func userSuffix() string { return "" }

// privateMode always holds. Windows does not keep Unix permission bits — a
// file reports 0666, or 0444 when read-only — so checking them would reject
// every cache and force a login on every command. What keeps the cache private
// there is the ACL on the user's temporary directory, which only the user and
// administrators can read.
func privateMode(fs.FileMode) bool { return true }

// defaultKrb5Config is where MIT Kerberos for Windows keeps its configuration.
func defaultKrb5Config() string {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		dir = `C:\ProgramData`
	}
	return filepath.Join(dir, "MIT", "Kerberos5", "krb5.ini")
}
