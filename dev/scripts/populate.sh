#!/bin/bash
# Populate /eos/user/e/einstein with a test folder structure via WebDAV.
# Delegates all WebDAV operations to webdav.sh.
#
# Usage: populate.sh [command]
#
# Commands:
#   populate   Create test folders and files (default)
#   cleanup    Remove all created test data
#   help       Show this help
#
# Environment variables (forwarded to webdav.sh):
#   WEBDAV_URL   — base URL, e.g. http://localhost/remote.php/webdav
#   WEBDAV_USER  — username (default: einstein)
#   WEBDAV_PASS  — password (default: relativity)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WEBDAV="${SCRIPT_DIR}/webdav.sh"

die() { echo "ERROR: $*" >&2; exit 1; }

[[ -x "$WEBDAV" ]] || die "webdav.sh not found or not executable at $WEBDAV"

# ---------------------------------------------------------------------------
# Test data definition
# ---------------------------------------------------------------------------
BASE_PATH="/eos/user/e/einstein"

DIRS=(
    "$BASE_PATH/documents"
    "$BASE_PATH/documents/reports"
    "$BASE_PATH/documents/reports/2024"
    "$BASE_PATH/documents/reports/2025"
    "$BASE_PATH/documents/notes"
    "$BASE_PATH/pictures"
    "$BASE_PATH/pictures/vacation"
    "$BASE_PATH/pictures/vacation/italy"
    "$BASE_PATH/pictures/work"
    "$BASE_PATH/projects"
    "$BASE_PATH/projects/alpha"
    "$BASE_PATH/projects/alpha/src"
    "$BASE_PATH/projects/alpha/docs"
    "$BASE_PATH/projects/beta"
    "$BASE_PATH/shared"
    "$BASE_PATH/shared/team"
)

# Each entry: "remote_path|file content"
FILES=(
    "$BASE_PATH/readme.txt|Welcome to the Einstein test space.
This folder is used for cernbox-sync integration tests.
"
    "$BASE_PATH/documents/notes/meeting-2025-01-10.txt|Meeting notes - 2025-01-10
Attendees: Einstein, Curie, Feynman
Topics: relativity, quantum mechanics
Action items: write papers
"
    "$BASE_PATH/documents/notes/todo.txt|TODO list:
- finish relativity paper
- review quantum proposals
- reply to Bohr
"
    "$BASE_PATH/documents/reports/2024/annual-report.txt|Annual Report 2024
Institute of Advanced Study
Summary: excellent progress on all fronts.
"
    "$BASE_PATH/documents/reports/2025/q1-report.txt|Q1 2025 Report
Progress: on track
Highlights: new discoveries in spacetime
"
    "$BASE_PATH/documents/reports/2025/q2-report.txt|Q2 2025 Report
Progress: ahead of schedule
Highlights: unified field theory draft
"
    "$BASE_PATH/pictures/vacation/italy/captions.txt|Photos from Italy trip, summer 2024.
Florence, Rome, Venice.
"
    "$BASE_PATH/pictures/work/conference-notes.txt|Photos from the Solvay Conference.
Group photo with all participants.
"
    "$BASE_PATH/projects/alpha/src/main.go|package main

import \"fmt\"

func main() {
	fmt.Println(\"hello from alpha\")
}
"
    "$BASE_PATH/projects/alpha/docs/design.txt|Alpha project design document.
Architecture: microservices
Language: Go
Status: in progress
"
    "$BASE_PATH/projects/beta/README.txt|Beta project - experimental
Do not use in production.
"
    "$BASE_PATH/shared/team/guidelines.txt|Team guidelines:
1. Commit early, commit often
2. Write tests
3. Document your code
4. Be kind
"
)

# Top-level entries to remove on cleanup (server deletes subtrees recursively)
CLEANUP_ROOTS=(
    "$BASE_PATH/documents"
    "$BASE_PATH/pictures"
    "$BASE_PATH/projects"
    "$BASE_PATH/shared"
    "$BASE_PATH/readme.txt"
)

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
cmd_populate() {
    echo "Creating test directory structure..."
    for dir in "${DIRS[@]}"; do
        "$WEBDAV" mkdir "$dir"
    done

    # webdav.sh put requires a local file path, so write content to a temp file
    local tmpfile
    tmpfile="$(mktemp)"
    # Use double quotes so $tmpfile is expanded now, not at trap fire time
    # shellcheck disable=SC2064
    trap "rm -f '${tmpfile}'" RETURN

    echo "Uploading test files..."
    for entry in "${FILES[@]}"; do
        local path="${entry%%|*}"
        local content="${entry#*|}"
        printf '%s' "$content" > "$tmpfile"
        "$WEBDAV" put "$tmpfile" "$path"
    done

    echo "Done. Test data populated."
}

cmd_cleanup() {
    echo "Removing test data..."
    for root in "${CLEANUP_ROOTS[@]}"; do
        "$WEBDAV" delete "$root" || true
    done
    echo "Done. Test data removed."
}

# ---------------------------------------------------------------------------
# Dispatch
# ---------------------------------------------------------------------------
CMD="${1:-populate}"
shift || true

case "$CMD" in
    populate) cmd_populate ;;
    cleanup)  cmd_cleanup  ;;
    help|-h|--help)
        sed -n '/^# Commands:/,/^$/p' "$0" | grep -v '^#$' | sed 's/^# //'
        ;;
    *) die "Unknown command: $CMD. Use: populate | cleanup | help" ;;
esac
