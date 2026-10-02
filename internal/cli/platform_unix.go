//go:build !windows

package cli

// fallbackEditor is what "cernbox edit" runs when nothing else is configured.
const fallbackEditor = "vi"

func siteConfigPath() string { return "/etc/cernbox/config.yaml" }
