#!/bin/sh
# Install the CERNBox command-line client.
#
#   curl cli.cernbox.cern.ch | sh
#
# This is written for /bin/sh rather than bash: it has to run on whatever the
# machine has, and on lxplus that is not always the shell you are typing in.
#
# It does not use sudo. A release installed into a directory you own is a release
# you can replace without asking anybody, and a script piped from the network is
# the last thing that should be asking for a root password. If /usr/local/bin is
# writable it goes there; otherwise it goes to ~/.local/bin, and the script says
# so and says what to add to PATH if it is not already there.
#
# Environment:
#   CERNBOX_VERSION      install this version instead of the latest ("v1.2.3")
#   CERNBOX_INSTALL_DIR  install here instead of the default
#   CERNBOX_BASE_URL     download from here instead of the GitHub release, for a
#                        local mirror — and for the test that exercises this
#                        script without a network
set -eu

REPO="cernbox/cernbox-cli"
BINARY="cernbox"

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

need() {
    command -v "$1" >/dev/null 2>&1 || die "this needs $1, which is not installed"
}

need uname
need tar
need mktemp

# curl or wget, whichever is there. Both are common; neither is universal.
if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1" -o "$2"; }
    resolve() { curl -fsSLI -o /dev/null -w '%{url_effective}' "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO "$2" "$1"; }
    resolve() { wget -q --max-redirect=10 --server-response -O /dev/null "$1" 2>&1 |
        awk '/^  Location: /{u=$2} END{print u}'; }
else
    die "this needs curl or wget, and neither is installed"
fi

# ── what to download ─────────────────────────────────────────────────────────

case "$(uname -s)" in
    Linux)  os=linux ;;
    Darwin) os=darwin ;;
    *) die "$(uname -s) is not one of the systems this is built for (Linux, macOS)" ;;
esac

case "$(uname -m)" in
    x86_64 | amd64)  arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "$(uname -m) is not one of the architectures this is built for (x86_64, arm64)" ;;
esac

version="${CERNBOX_VERSION:-}"
if [ -z "$version" ]; then
    # The redirect from /releases/latest rather than the API: the API allows
    # sixty unauthenticated calls an hour per address, which a shared machine
    # like lxplus can get through without anybody noticing.
    latest_url="$(resolve "https://github.com/$REPO/releases/latest")" ||
        die "cannot reach GitHub to find the latest release"
    version="${latest_url##*/tag/}"
    case "$version" in
        v*) ;;
        *) die "cannot tell the latest version from $latest_url" ;;
    esac
fi
# The archive is named with the bare version, the tag carries a leading v.
bare="${version#v}"

archive="cernbox-cli_${bare}_${os}_${arch}.tar.gz"
base="${CERNBOX_BASE_URL:-https://github.com/$REPO/releases/download/$version}"

# ── where to put it ──────────────────────────────────────────────────────────

if [ -n "${CERNBOX_INSTALL_DIR:-}" ]; then
    dir="$CERNBOX_INSTALL_DIR"
    mkdir -p "$dir" || die "cannot create $dir"
elif [ -w /usr/local/bin ] 2>/dev/null; then
    dir=/usr/local/bin
else
    dir="$HOME/.local/bin"
    mkdir -p "$dir" || die "cannot create $dir"
fi
[ -w "$dir" ] || die "$dir is not writable. Set CERNBOX_INSTALL_DIR to somewhere it is"

# ── download, check, install ─────────────────────────────────────────────────

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $archive"
fetch "$base/$archive" "$tmp/$archive" ||
    die "cannot download $base/$archive. If $version is not a release, check https://github.com/$REPO/releases"

# The checksum is not ceremony: this arrived over the network, and the release
# publishes the list precisely so that what lands on disk can be checked against
# it. A missing checksum tool is a warning rather than a failure — refusing to
# install on a machine without sha256sum would help nobody.
if fetch "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
    if command -v sha256sum >/dev/null 2>&1; then
        sum="$(sha256sum "$tmp/$archive" | cut -d' ' -f1)"
    elif command -v shasum >/dev/null 2>&1; then
        sum="$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)"
    else
        sum=""
        say "warning: no sha256sum or shasum, so the download was not verified"
    fi
    if [ -n "$sum" ]; then
        want="$(awk -v f="$archive" '$2 == f || $2 == "*"f {print $1}' "$tmp/checksums.txt")"
        [ -n "$want" ] || die "$archive is not listed in checksums.txt"
        [ "$sum" = "$want" ] ||
            die "the download does not match its checksum. Expected $want, got $sum"
        say "Checksum verified"
    fi
else
    say "warning: no checksums.txt in this release, so the download was not verified"
fi

tar -xzf "$tmp/$archive" -C "$tmp" ||
    die "cannot unpack $archive"
[ -f "$tmp/$BINARY" ] || die "$archive does not contain $BINARY"

# Installed by rename so that a running copy is replaced rather than written
# through, and into a temporary name first so a failure leaves the old one.
chmod 755 "$tmp/$BINARY"
mv -f "$tmp/$BINARY" "$dir/$BINARY.new" || die "cannot write to $dir"
mv -f "$dir/$BINARY.new" "$dir/$BINARY" || die "cannot install into $dir"

say "Installed $BINARY $bare to $dir"

# ── tell them what to do next ────────────────────────────────────────────────

case ":$PATH:" in
    *":$dir:"*) ;;
    *)
        say ""
        say "$dir is not on your PATH. Add it with:"
        say ""
        say "    echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile"
        say ""
        ;;
esac

if [ -x "$dir/$BINARY" ]; then
    "$dir/$BINARY" --help >/dev/null 2>&1 ||
        say "warning: $dir/$BINARY does not run on this machine"
fi

say "Run '$BINARY status' to see how you are signed in, or '$BINARY --help'."
say "Shell completion: source <($BINARY completion bash)   # or zsh, or fish"
