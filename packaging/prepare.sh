#!/bin/sh
# Prepare the gaia workspace of the cernbox build, which goreleaser and
# "make build" then compile. cernbox is reva's CLI with the CERNBox CLI
# plugins and brand. gaia checks the brand here, so a bad brand fails in the
# build and not on a user's machine. bflags holds the -X flags of the version.
#
# The versions come from packaging/versions.env. The environment overrides it:
#   GAIA                  the gaia binary (default: gaia on PATH)
#   WS                    the workspace (default: ws, at the root of the repo)
#   REVA_VERSION          reva, a Go module query
#   REVA_PLUGINS_VERSION  reva-plugins, a Go module query
#   REVA_DIR              a local reva checkout, instead of REVA_VERSION
#   REVA_PLUGINS_DIR      a local reva-plugins checkout, instead of
#                         REVA_PLUGINS_VERSION
#   BRAND                 the brand file (default: cli/cernbox.toml of
#                         reva-plugins)
#
# A local checkout writes its absolute path into the go.mod of the workspace:
# a release must use published versions only.
set -eu

die() { printf 'prepare: %s\n' "$*" >&2; exit 1; }

root="$(cd "$(dirname "$0")/.." && pwd)"

# shellcheck disable=SC2034 # value is read by the eval
while IFS='=' read -r key value; do
    case "$key" in '' | \#*) continue ;; esac
    eval "current=\${$key:-}"
    [ -n "$current" ] || eval "$key=\$value"
done < "$root/packaging/versions.env"

GAIA="${GAIA:-gaia}"
WS="${WS:-$root/ws}"
REVA_DIR="${REVA_DIR:-}"
REVA_PLUGINS_DIR="${REVA_PLUGINS_DIR:-}"
BRAND="${BRAND:-}"

command -v "$GAIA" >/dev/null 2>&1 ||
    die "gaia is not installed: go install github.com/cs3org/gaia@$GAIA_VERSION, or set GAIA"

plugins=github.com/cernbox/reva-plugins/cli
if [ -n "$REVA_PLUGINS_DIR" ]; then
    with_plugins="$plugins=$REVA_PLUGINS_DIR/cli"
    BRAND="${BRAND:-$REVA_PLUGINS_DIR/cli/cernbox.toml}"
else
    with_plugins="$plugins@$REVA_PLUGINS_VERSION"
    if [ -z "$BRAND" ]; then
        # Take the brand from the module cache, at the build version. Run
        # outside the repo, so go.mod and go.sum stay as they are.
        dir="$(cd "$(mktemp -d)" && GOWORK=off GOFLAGS=-mod=mod \
            go mod download -json "github.com/cernbox/reva-plugins@$REVA_PLUGINS_VERSION" |
            sed -n 's/^[[:space:]]*"Dir": "\(.*\)",*$/\1/p')" ||
            die "cannot download github.com/cernbox/reva-plugins@$REVA_PLUGINS_VERSION"
        [ -n "$dir" ] || die "cannot find github.com/cernbox/reva-plugins@$REVA_PLUGINS_VERSION in the module cache"
        BRAND="$dir/cli/cernbox.toml"
    fi
fi
[ -f "$BRAND" ] || die "no brand at $BRAND"

set -- --only-prepare --workspace "$WS" --binary reva --brand "$BRAND" --with "$with_plugins"
if [ -n "$REVA_DIR" ]; then
    set -- "$@" --with "github.com/cs3org/reva/v3=$REVA_DIR"
else
    set -- "$REVA_VERSION" "$@"
fi

# gaia runs "go mod init" and refuses a workspace that has a go.mod already,
# so an earlier workspace goes. Anything else in its place stays.
if [ -e "$WS" ] && [ -n "$(ls -A "$WS")" ] && ! { [ -f "$WS/go.mod" ] && [ -f "$WS/main.go" ]; }; then
    die "$WS is not empty and is not a workspace: set WS to another directory"
fi
rm -rf "$WS"
mkdir -p "$WS"

# gaia does not pass GOWORK to go: a go.work above the workspace would take
# over the build.
gowork="$(cd "$WS" && env -u GOWORK go env GOWORK)"
[ -z "$gowork" ] || die "$WS is under $gowork: move the workspace (WS) out of that tree"

"$GAIA" build "$@"

[ -s "$WS/bflags" ] || die "gaia did not write $WS/bflags"
printf 'prepare: workspace %s\nprepare: brand %s\nprepare: bflags %s\n' "$WS" "$BRAND" "$(cat "$WS/bflags")"
