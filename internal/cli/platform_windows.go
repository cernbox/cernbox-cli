package cli

import (
	"os"
	"path/filepath"
)

// fallbackEditor is what "cernbox edit" runs when nothing else is configured.
// Windows has no vi, and Notepad is always there.
const fallbackEditor = "notepad"

// siteConfigPath is the machine-wide configuration file, in the directory
// Windows sets aside for data shared by every user.
func siteConfigPath() string {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		dir = `C:\ProgramData`
	}
	return filepath.Join(dir, "cernbox", "config.yaml")
}
