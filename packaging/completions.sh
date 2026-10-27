#!/bin/sh
# Write the shell completion scripts of a cernbox binary into
# packaging/completions, for the packages.
#
#   packaging/completions.sh [binary]
#
# Without a binary, it builds one from the prepared workspace.
set -eu

die() { printf 'completions: %s\n' "$*" >&2; exit 1; }

root="$(cd "$(dirname "$0")/.." && pwd)"
WS="${WS:-$root/ws}"
out="$root/packaging/completions"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

binary="${1:-}"
if [ -z "$binary" ]; then
    [ -f "$WS/bflags" ] || die "no workspace at $WS: run packaging/prepare.sh first"
    binary="$tmp/cernbox"
    (cd "$WS" && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags "-s -w $(cat bflags)" -o "$binary" .)
fi

# Use an empty home: the CLI copies a cernbox-cli config.yaml that it finds
# into config.toml, and a build must not do that to the machine.
mkdir -p "$out" "$tmp/home"
for shell in bash zsh fish; do
    HOME="$tmp/home" XDG_CONFIG_HOME="$tmp/home/.config" APPDATA="$tmp/home" \
        "$binary" completion "$shell" > "$out/cernbox.$shell"
done
