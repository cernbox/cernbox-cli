#!/bin/bash
# Simple WebDAV client with Basic Auth
# Usage: webdav.sh <command> [args...]
#
# Environment variables:
#   WEBDAV_URL   — base URL, e.g. http://localhost:443/webdav
#   WEBDAV_USER  — username
#   WEBDAV_PASS  — password
#
# Commands:
#   list   <remote-path>                  List contents of a remote directory
#   get    <remote-path> [local-path]     Download a file
#   put    <local-path>  <remote-path>    Upload a file
#   mkdir  <remote-path>                  Create a remote directory (and parents)
#   delete <remote-path>                  Delete a file or directory
#   move   <remote-src>  <remote-dst>     Move/rename a remote resource
#   help                                  Show this help

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration — override via env vars or edit defaults below
# ---------------------------------------------------------------------------
BASE_URL="${WEBDAV_URL:-http://localhost/remote.php/webdav}"
USER="${WEBDAV_USER:-einstein}"
PASS="${WEBDAV_PASS:-relativity}"

# Strip trailing slash from base URL
BASE_URL="${BASE_URL%/}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
usage() {
    sed -n '/^# Commands:/,/^$/p' "$0" | grep -v '^#$' | sed 's/^# //'
    exit 1
}

die() { echo "ERROR: $*" >&2; exit 1; }

# Build the full URL for a remote path
url() {
    local path="$1"
    # Ensure path starts with /
    [[ "$path" == /* ]] || path="/$path"
    echo "${BASE_URL}${path}"
}

# Shared curl options: basic auth, follow redirects, fail on HTTP errors
CURL=(curl --silent --show-error --fail-with-body --user "${USER}:${PASS}" --location)

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
cmd_list() {
    [[ $# -ge 1 ]] || die "Usage: list <remote-path>"
    local remote_url
    remote_url="$(url "$1")"
    # Ensure directory URL ends with /
    [[ "$remote_url" == */ ]] || remote_url="${remote_url}/"

    local response
    response=$(
        "${CURL[@]}" \
            --request PROPFIND \
            --header "Depth: 1" \
            --header "Content-Type: application/xml" \
            --data '<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/><D:resourcetype/><D:getcontentlength/><D:getlastmodified/></D:prop></D:propfind>' \
            "$remote_url"
    )

    # Parse with xmllint if available, otherwise raw output
    if command -v xmllint &>/dev/null; then
        echo "$response" | xmllint --format - 2>/dev/null \
            | grep -E "<d:href>|<d:getcontentlength>|<d:getlastmodified>|<d:collection" \
            | sed \
                -e 's|.*<d:href>\(.*\)</d:href>.*|\1|' \
                -e 's|.*<d:getcontentlength>\(.*\)</d:getcontentlength>.*|  size: \1|' \
                -e 's|.*<d:getlastmodified>\(.*\)</d:getlastmodified>.*|  modified: \1|' \
                -e 's|.*<d:collection/>.*|  [directory]|' \
            || echo "$response"
    else
        echo "$response"
    fi
}

cmd_get() {
    [[ $# -ge 1 ]] || die "Usage: get <remote-path> [local-path]"
    local remote_path="$1"
    local remote_url
    remote_url="$(url "$remote_path")"

    if [[ $# -ge 2 ]]; then
        echo "Downloading $remote_url -> $2" >&2
        "${CURL[@]}" --output "$2" "$remote_url"
        echo "Done." >&2
    else
        "${CURL[@]}" --output - "$remote_url"
    fi
}

cmd_put() {
    [[ $# -ge 2 ]] || die "Usage: put <local-path> <remote-path>"
    local local_path="$1"
    local remote_path="$2"
    [[ -f "$local_path" ]] || die "Local file not found: $local_path"
    local remote_url
    remote_url="$(url "$remote_path")"

    echo "Uploading $local_path -> $remote_url"
    "${CURL[@]}" --request PUT --upload-file "$local_path" "$remote_url"
    echo "Done."
}

cmd_mkdir() {
    [[ $# -ge 1 ]] || die "Usage: mkdir <remote-path>"
    local path="$1"
    # Remove trailing slash for consistent splitting
    path="${path%/}"
    local parts=()
    IFS='/' read -ra parts <<< "${path#/}"

    # Create each path component in order (like mkdir -p)
    local cumulative=""
    for part in "${parts[@]}"; do
        [[ -z "$part" ]] && continue
        cumulative="${cumulative}/${part}"
        local remote_url
        remote_url="$(url "$cumulative")"
        local http_code
        http_code=$(
            curl --silent --output /dev/null --write-out "%{http_code}" \
                --user "${USER}:${PASS}" \
                --request MKCOL \
                "$remote_url"
        )
        case "$http_code" in
            201) echo "Created: $cumulative" ;;
            301|405) echo "Exists:  $cumulative" ;;   # 405 = already exists on many servers
            *) die "MKCOL $cumulative returned HTTP $http_code" ;;
        esac
    done
}

cmd_delete() {
    [[ $# -ge 1 ]] || die "Usage: delete <remote-path>"
    local remote_url
    remote_url="$(url "$1")"

    echo "Deleting $remote_url"
    "${CURL[@]}" --request DELETE "$remote_url"
    echo "Done."
}

cmd_move() {
    [[ $# -ge 2 ]] || die "Usage: move <remote-src> <remote-dst>"
    local src_url dst_url
    src_url="$(url "$1")"
    dst_url="$(url "$2")"

    echo "Moving $src_url -> $dst_url"
    "${CURL[@]}" \
        --request MOVE \
        --header "Destination: $dst_url" \
        --header "Overwrite: T" \
        "$src_url"
    echo "Done."
}

# ---------------------------------------------------------------------------
# Dispatch
# ---------------------------------------------------------------------------
CMD="${1:-help}"
shift || true

case "$CMD" in
    list)   cmd_list   "$@" ;;
    get)    cmd_get    "$@" ;;
    put)    cmd_put    "$@" ;;
    mkdir)  cmd_mkdir  "$@" ;;
    delete) cmd_delete "$@" ;;
    move)   cmd_move   "$@" ;;
    help|-h|--help) usage ;;
    *) die "Unknown command: $CMD. Run 'webdav.sh help' for usage." ;;
esac
