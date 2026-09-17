// Command cernbox is the CERNBox command-line client.
package main

import (
	"os"

	"github.com/cernbox/cernbox-cli/internal/cli"
	"github.com/cernbox/cernbox-cli/pkg/client"
)

// Stamped in at build time with -ldflags.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	cli.Version = version
	cli.Commit = commit
	cli.BuildDate = buildDate

	// The version travels in the User-Agent so server-side telemetry can see
	// the client version distribution, which is what makes it possible to time
	// a deprecation rather than guess at one.
	client.DefaultUserAgent = "cernbox-cli/" + version + " (rev-" + commit + ")"

	os.Exit(cli.Execute())
}
