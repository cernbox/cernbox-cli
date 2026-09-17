#!/bin/bash
# LibreGraph API client with Basic Auth
# Usage: graph.sh <command> [options] [args...]
#
# Environment variables:
#   GRAPH_URL    — base URL, e.g. http://localhost/graph  (default: http://localhost/graph)
#   GRAPH_USER   — username                                   (default: einstein)
#   GRAPH_PASS   — password                                   (default: relativity)
#
# Global options (must come before the command):
#   -v           Verbose: show HTTP method and URL before the request
#
# ── v1.0 · Users ────────────────────────────────────────────────────────────
#
#   me
#       GET /v1.0/me
#       Return the current authenticated user's profile.
#
#   me-change-password --current <pwd> --new <pwd>
#       POST /v1.0/me/changePassword
#       Change the authenticated user's password.
#         --current <pwd>   current password (required)
#         --new     <pwd>   new password     (required)
#
#   users [--expand-groups]
#       GET /v1.0/users
#       List all system users.
#         --expand-groups   include group memberships ($expand=memberOf)
#
#   user <id-or-name> [--expand-groups]
#       GET /v1.0/users/{id-or-name}
#       Get a specific user by UUID or account name.
#         --expand-groups   include group memberships ($expand=memberOf)
#
#   user-create --display-name <n> --mail <m> --account <a> --password <p>
#       POST /v1.0/users
#       Create a new user account.
#         --display-name <n>   full display name  (required)
#         --mail         <m>   email address      (required)
#         --account      <a>   login/account name (required)
#         --password     <p>   initial password   (required)
#
#   user-update <id> [--display-name <n>] [--mail <m>] [--account <a>]
#       PATCH /v1.0/users/{id}
#       Update user attributes (UUID required; supply any fields to change).
#         --display-name <n>   new display name
#         --mail         <m>   new email address
#         --account      <a>   new login/account name
#
#   user-delete <id>
#       DELETE /v1.0/users/{id}
#       Permanently delete a user account.
#
# ── v1.0 · Groups ───────────────────────────────────────────────────────────
#
#   groups
#       GET /v1.0/groups
#       List all groups.
#
# ── v1.0 · Spaces ───────────────────────────────────────────────────────────
#
#   spaces [--filter <expr>] [--expand <expr>]
#       GET /v1.0/drives
#       List all available spaces (requires elevated permissions for spaces
#       the user is not a member of).
#         --filter <expr>   OData $filter  e.g. "driveType eq 'project'"
#         --expand <expr>   OData $expand  e.g. "root($expand=children)"
#
#   space-create --name <n> [--description <d>] [--quota <bytes>]
#       POST /v1.0/drives
#       Create a new space.
#         --name        <n>      space display name (required)
#         --description <d>      optional description
#         --quota       <bytes>  total quota in bytes
#
#   space <space-id>
#       GET /v1.0/drives/{space-id}
#       Get a specific space by ID.
#
#   space-update <space-id> [--name <n>] [--description <d>] [--alias <a>] [--quota <bytes>]
#       PATCH /v1.0/drives/{space-id}
#       Update space properties.
#         --name        <n>      new display name
#         --description <d>      new description
#         --alias       <a>      new drive alias
#         --quota       <bytes>  new total quota in bytes
#
#   space-disable <space-id>
#       DELETE /v1.0/drives/{space-id}
#       Disable (soft-delete) a space — content is preserved but unavailable.
#
#   space-restore <space-id>
#       PATCH /v1.0/drives/{space-id}  (with Restore: T header)
#       Re-enable a previously disabled space.
#
#   space-purge <space-id>
#       DELETE /v1.0/drives/{space-id}  (with Purge: T header)
#       Permanently delete a disabled space and all its contents.
#       WARNING: irreversible. Space must be disabled first.
#
# ── v1beta1 · My spaces & shares ────────────────────────────────────────────
#
#   my-spaces
#       GET /v1beta1/me/drives
#       List spaces where the authenticated user is a member.
#
#   shared-with-me
#       GET /v1beta1/me/drive/sharedWithMe
#       List items shared with the authenticated user.
#
#   shared-by-me
#       GET /v1beta1/me/drive/sharedByMe
#       List items shared by the authenticated user.
#
# ── v1beta1 · Roles ─────────────────────────────────────────────────────────
#
#   role-definitions
#       GET /v1beta1/roleManagement/permissions/roleDefinitions
#       List all available permission role definitions.
#
# ── v1beta1 · Space permissions ─────────────────────────────────────────────
#
#   space-permissions <space-id>
#       GET /v1beta1/drives/{space-id}/root/permissions
#       List all permissions on a space root.
#
#   space-invite <space-id> --recipients <json> --roles <json> [--expiry <date>]
#       POST /v1beta1/drives/{space-id}/root/invite
#       Add members to a space.
#         --recipients <json>   array of {objectId,@libre.graph.recipient.type}
#         --roles      <json>   array of {roleId} objects (use role-definitions)
#         --expiry     <date>   optional expiry date (ISO-8601)
#
#   space-create-link <space-id> --type <t> [--password <p>] [--expiry <date>]
#       POST /v1beta1/drives/{space-id}/root/createLink
#       Create a shareable link for a space.
#         --type     <t>   link type: view|edit|upload|createOnly|blocksDownload
#         --password <p>   optional link password
#         --expiry   <d>   optional expiry date (ISO-8601)
#
#   space-update-permission <space-id> <perm-id> --roles <json> [--expiry <date>]
#       PATCH /v1beta1/drives/{space-id}/root/permissions/{perm-id}
#       Update a space permission entry.
#         --roles  <json>   array of {roleId} objects
#         --expiry <date>   optional new expiry date (ISO-8601)
#
#   space-delete-permission <space-id> <perm-id>
#       DELETE /v1beta1/drives/{space-id}/root/permissions/{perm-id}
#       Remove a space permission (user or link).
#
#   space-set-link-password <space-id> <perm-id> --password <p>
#       POST /v1beta1/drives/{space-id}/root/permissions/{perm-id}/setPassword
#       Set or update a link share password.
#         --password <p>   new password (required)
#
# ── v1beta1 · Drive item permissions ────────────────────────────────────────
#
#   drive-permissions <space-id> <resource-id>
#       GET /v1beta1/drives/{space-id}/items/{resource-id}/permissions
#       List all permissions on a drive item.
#
#   drive-invite <space-id> <resource-id> --recipients <json> --roles <json> [--expiry <date>]
#       POST /v1beta1/drives/{space-id}/items/{resource-id}/invite
#       Share a drive item with users or groups.
#         --recipients <json>   array of {objectId,@libre.graph.recipient.type}
#         --roles      <json>   array of {roleId} objects (use role-definitions)
#         --expiry     <date>   optional expiry date (ISO-8601)
#
#   drive-create-link <space-id> <resource-id> --type <t> [--password <p>] [--expiry <date>]
#       POST /v1beta1/drives/{space-id}/items/{resource-id}/createLink
#       Create a shareable link for a drive item.
#         --type     <t>   link type: view|edit|upload|createOnly|blocksDownload
#         --password <p>   optional link password
#         --expiry   <d>   optional expiry date (ISO-8601)
#
#   update-received-share <space-id> <resource-id> --hidden <bool>
#       PATCH /v1beta1/drives/{space-id}/items/{resource-id}
#       Update a received share (e.g. hide/unhide it).
#         --hidden <bool>   true or false
#
#   update-permission <space-id> <resource-id> <perm-id> --roles <json> [--expiry <date>]
#       PATCH /v1beta1/drives/{space-id}/items/{resource-id}/permissions/{perm-id}
#       Update a drive item permission entry.
#         --roles  <json>   array of {roleId} objects
#         --expiry <date>   optional new expiry date (ISO-8601)
#
#   delete-permission <space-id> <resource-id> <perm-id>
#       DELETE /v1beta1/drives/{space-id}/items/{resource-id}/permissions/{perm-id}
#       Remove a permission from a drive item.
#
#   set-link-password <space-id> <resource-id> <perm-id> --password <p>
#       POST /v1beta1/drives/{space-id}/items/{resource-id}/permissions/{perm-id}/setPassword
#       Set or update a link share password on a drive item.
#         --password <p>   new password (required)
#
#   help
#       Show this help text.
#

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration — override via env vars or edit defaults below
# ---------------------------------------------------------------------------
BASE_URL="${GRAPH_URL:-http://localhost/graph}"
USER="${GRAPH_USER:-einstein}"
PASS="${GRAPH_PASS:-relativity}"
VERBOSE=0

# Strip trailing slash from base URL
BASE_URL="${BASE_URL%/}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
usage() {
    sed -n '/^# ── v1\.0/,/^[^#]/p' "$0" \
        | grep '^#' \
        | sed 's/^# \{0,1\}//'
    exit 1
}

die()  { echo "ERROR: $*" >&2; exit 1; }
info() { echo "$*" >&2; }

url() { echo "${BASE_URL}/$1"; }

# Pretty-print JSON; pass through gracefully if jq is absent
pretty_print() {
    if command -v jq &>/dev/null; then
        jq --sort-keys .
    else
        cat
    fi
}

# Pretty-print a space object: id, name, type, quota, webUrl
fmt_space() {
    if command -v jq &>/dev/null; then
        jq --sort-keys '
            if .value then .value[] else . end
            | {
                id,
                name,
                driveType,
                driveAlias,
                webUrl,
                quota: (.quota // {} | {
                    total:     .total,
                    used:      .used,
                    remaining: .remaining,
                    state:     .state
                }),
                owner: (.owner.user // {} | {displayName, id})
              }'
    else
        cat
    fi
}

# Pretty-print a user object
fmt_user() {
    if command -v jq &>/dev/null; then
        jq --sort-keys '
            if .value then .value[] else . end
            | {id, displayName, mail, onPremisesSamAccountName, memberOf}'
    else
        cat
    fi
}

# Pretty-print a permission list
fmt_permissions() {
    if command -v jq &>/dev/null; then
        jq --sort-keys '
            if .value then .value[] else . end
            | {
                id,
                roles,
                expirationDateTime,
                grantedToV2,
                link: (.link // {} | {type, webUrl})
              }'
    else
        cat
    fi
}

# Shared curl options: basic auth, follow redirects, fail on HTTP errors
CURL=(curl --silent --show-error --fail-with-body --user "${USER}:${PASS}" --location)
JSON_H=(--header "Content-Type: application/json" --header "Accept: application/json")

# Wrapper that optionally prints verb + URL, then runs curl
do_curl() {
    local method="$1"; shift
    local endpoint="$1"; shift
    local full_url
    full_url="$(url "$endpoint")"
    [[ $VERBOSE -eq 1 ]] && info "$method $full_url"
    "${CURL[@]}" "${JSON_H[@]}" --request "$method" "$@" "$full_url"
}

# Build an OData query string from optional --filter / --expand args.
# Usage: build_query [--filter <expr>] [--expand <expr>]
# Prints "?..." or "" (nothing) to stdout.
build_query() {
    local filter="" expand="" parts=()
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --filter) filter="$2"; shift 2 ;;
            --expand) expand="$2"; shift 2 ;;
            *) shift ;;
        esac
    done
    [[ -n "$filter" ]] && parts+=("\$filter=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$filter" 2>/dev/null || printf '%s' "$filter")")
    [[ -n "$expand" ]] && parts+=("\$expand=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$expand" 2>/dev/null || printf '%s' "$expand")")
    if [[ ${#parts[@]} -gt 0 ]]; then
        local IFS='&'
        echo "?${parts[*]}"
    fi
}

# ---------------------------------------------------------------------------
# v1.0 · Users
# ---------------------------------------------------------------------------
cmd_me() {
    do_curl GET "v1.0/me" | fmt_user
}

cmd_me_change_password() {
    local current="" newpwd=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --current) current="$2"; shift 2 ;;
            --new)     newpwd="$2";  shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ -n "$current" ]] || die "me-change-password: --current is required"
    [[ -n "$newpwd"  ]] || die "me-change-password: --new is required"
    local body
    body=$(printf '{"currentPassword":"%s","newPassword":"%s"}' "$current" "$newpwd")
    do_curl POST "v1.0/me/changePassword" --data "$body"
    info "Password changed."
}

cmd_users() {
    local expand_groups=0
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --expand-groups) expand_groups=1; shift ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    local qs=""
    [[ $expand_groups -eq 1 ]] && qs="?\$expand=memberOf"
    do_curl GET "v1.0/users${qs}" | fmt_user
}

cmd_user() {
    [[ $# -ge 1 ]] || die "Usage: user <id-or-name> [--expand-groups]"
    local id="$1"; shift
    local expand_groups=0
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --expand-groups) expand_groups=1; shift ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    local qs=""
    [[ $expand_groups -eq 1 ]] && qs="?\$expand=memberOf"
    do_curl GET "v1.0/users/${id}${qs}" | fmt_user
}

cmd_user_create() {
    local display_name="" mail="" account="" password=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --display-name) display_name="$2"; shift 2 ;;
            --mail)         mail="$2";         shift 2 ;;
            --account)      account="$2";      shift 2 ;;
            --password)     password="$2";     shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ -n "$display_name" ]] || die "user-create: --display-name is required"
    [[ -n "$mail"         ]] || die "user-create: --mail is required"
    [[ -n "$account"      ]] || die "user-create: --account is required"
    [[ -n "$password"     ]] || die "user-create: --password is required"
    local body
    body=$(printf '{"displayName":"%s","mail":"%s","onPremisesSamAccountName":"%s","passwordProfile":{"password":"%s"}}' \
        "$display_name" "$mail" "$account" "$password")
    do_curl POST "v1.0/users" --data "$body" | fmt_user
}

cmd_user_update() {
    [[ $# -ge 1 ]] || die "Usage: user-update <id> [--display-name <n>] [--mail <m>] [--account <a>]"
    local id="$1"; shift
    local fields=()
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --display-name) fields+=("\"displayName\":\"$2\"");             shift 2 ;;
            --mail)         fields+=("\"mail\":\"$2\"");                    shift 2 ;;
            --account)      fields+=("\"onPremisesSamAccountName\":\"$2\""); shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ ${#fields[@]} -gt 0 ]] || die "user-update: supply at least one field to update"
    local IFS=','
    local body="{${fields[*]}}"
    do_curl PATCH "v1.0/users/${id}" --data "$body" | fmt_user
}

cmd_user_delete() {
    [[ $# -ge 1 ]] || die "Usage: user-delete <id>"
    do_curl DELETE "v1.0/users/$1"
    info "User $1 deleted."
}

# ---------------------------------------------------------------------------
# v1.0 · Groups
# ---------------------------------------------------------------------------
cmd_groups() {
    do_curl GET "v1.0/groups" | pretty_print
}

# ---------------------------------------------------------------------------
# v1.0 · Spaces
# ---------------------------------------------------------------------------
cmd_spaces() {
    local qs
    qs="$(build_query "$@")"
    do_curl GET "v1.0/drives${qs}" | fmt_space
}

cmd_space_create() {
    local name="" description="" quota=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --name)        name="$2";        shift 2 ;;
            --description) description="$2"; shift 2 ;;
            --quota)       quota="$2";       shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ -n "$name" ]] || die "space-create: --name is required"
    local fields=("\"name\":\"$name\"")
    [[ -n "$description" ]] && fields+=("\"description\":\"$description\"")
    [[ -n "$quota"       ]] && fields+=("\"quota\":{\"total\":$quota}")
    local IFS=','
    local body="{${fields[*]}}"
    do_curl POST "v1.0/drives" --data "$body" | fmt_space
}

cmd_space() {
    [[ $# -ge 1 ]] || die "Usage: space <space-id>"
    do_curl GET "v1.0/drives/$1" | fmt_space
}

cmd_space_update() {
    [[ $# -ge 1 ]] || die "Usage: space-update <space-id> [--name <n>] [--description <d>] [--alias <a>] [--quota <bytes>]"
    local id="$1"; shift
    local fields=()
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --name)        fields+=("\"name\":\"$2\"");        shift 2 ;;
            --description) fields+=("\"description\":\"$2\""); shift 2 ;;
            --alias)       fields+=("\"driveAlias\":\"$2\"");  shift 2 ;;
            --quota)       fields+=("\"quota\":{\"total\":$2}"); shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ ${#fields[@]} -gt 0 ]] || die "space-update: supply at least one field to update"
    local IFS=','
    local body="{${fields[*]}}"
    do_curl PATCH "v1.0/drives/${id}" --data "$body" | fmt_space
}

cmd_space_disable() {
    [[ $# -ge 1 ]] || die "Usage: space-disable <space-id>"
    "${CURL[@]}" "${JSON_H[@]}" --request DELETE "$(url "v1.0/drives/$1")"
    info "Space $1 disabled."
}

cmd_space_restore() {
    [[ $# -ge 1 ]] || die "Usage: space-restore <space-id>"
    "${CURL[@]}" "${JSON_H[@]}" \
        --request PATCH \
        --header "Restore: T" \
        --data '{}' \
        "$(url "v1.0/drives/$1")" | fmt_space
}

cmd_space_purge() {
    [[ $# -ge 1 ]] || die "Usage: space-purge <space-id>"
    "${CURL[@]}" "${JSON_H[@]}" \
        --request DELETE \
        --header "Purge: T" \
        "$(url "v1.0/drives/$1")"
    info "Space $1 permanently deleted."
}

# ---------------------------------------------------------------------------
# v1beta1 · My spaces & shares
# ---------------------------------------------------------------------------
cmd_my_spaces() {
    do_curl GET "v1beta1/me/drives" | fmt_space
}

cmd_shared_with_me() {
    do_curl GET "v1beta1/me/drive/sharedWithMe" | pretty_print
}

cmd_shared_by_me() {
    do_curl GET "v1beta1/me/drive/sharedByMe" | pretty_print
}

# ---------------------------------------------------------------------------
# v1beta1 · Roles
# ---------------------------------------------------------------------------
cmd_role_definitions() {
    do_curl GET "v1beta1/roleManagement/permissions/roleDefinitions" | pretty_print
}

# ---------------------------------------------------------------------------
# v1beta1 · Space permissions
# ---------------------------------------------------------------------------
cmd_space_permissions() {
    [[ $# -ge 1 ]] || die "Usage: space-permissions <space-id>"
    do_curl GET "v1beta1/drives/$1/root/permissions" | fmt_permissions
}

# Build a share invite body from --recipients / --roles / --expiry flags
_build_invite_body() {
    local recipients="" roles="" expiry=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --recipients) recipients="$2"; shift 2 ;;
            --roles)      roles="$2";      shift 2 ;;
            --expiry)     expiry="$2";     shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ -n "$recipients" ]] || die "missing --recipients"
    [[ -n "$roles"      ]] || die "missing --roles"
    local body="{\"recipients\":${recipients},\"roles\":${roles}"
    [[ -n "$expiry" ]] && body+=",\"expirationDateTime\":\"$expiry\""
    body+="}"
    echo "$body"
}

# Build a createLink body from --type / --password / --expiry flags
_build_link_body() {
    local type="" password="" expiry=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --type)     type="$2";     shift 2 ;;
            --password) password="$2"; shift 2 ;;
            --expiry)   expiry="$2";   shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ -n "$type" ]] || die "missing --type"
    local body="{\"type\":\"$type\""
    [[ -n "$password" ]] && body+=",\"password\":\"$password\""
    [[ -n "$expiry"   ]] && body+=",\"expirationDateTime\":\"$expiry\""
    body+="}"
    echo "$body"
}

# Build a permission update body from --roles / --expiry flags
_build_permission_body() {
    local roles="" expiry=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --roles)  roles="$2";  shift 2 ;;
            --expiry) expiry="$2"; shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ -n "$roles" ]] || die "missing --roles"
    local body="{\"roles\":${roles}"
    [[ -n "$expiry" ]] && body+=",\"expirationDateTime\":\"$expiry\""
    body+="}"
    echo "$body"
}

cmd_space_invite() {
    [[ $# -ge 1 ]] || die "Usage: space-invite <space-id> --recipients <json> --roles <json> [--expiry <date>]"
    local space_id="$1"; shift
    local body; body="$(_build_invite_body "$@")"
    do_curl POST "v1beta1/drives/${space_id}/root/invite" --data "$body" | fmt_permissions
}

cmd_space_create_link() {
    [[ $# -ge 1 ]] || die "Usage: space-create-link <space-id> --type <t> [--password <p>] [--expiry <date>]"
    local space_id="$1"; shift
    local body; body="$(_build_link_body "$@")"
    do_curl POST "v1beta1/drives/${space_id}/root/createLink" --data "$body" | fmt_permissions
}

cmd_space_update_permission() {
    [[ $# -ge 2 ]] || die "Usage: space-update-permission <space-id> <perm-id> --roles <json> [--expiry <date>]"
    local space_id="$1" perm_id="$2"; shift 2
    local body; body="$(_build_permission_body "$@")"
    do_curl PATCH "v1beta1/drives/${space_id}/root/permissions/${perm_id}" --data "$body" | fmt_permissions
}

cmd_space_delete_permission() {
    [[ $# -ge 2 ]] || die "Usage: space-delete-permission <space-id> <perm-id>"
    do_curl DELETE "v1beta1/drives/$1/root/permissions/$2"
    info "Permission $2 removed from space $1."
}

cmd_space_set_link_password() {
    [[ $# -ge 2 ]] || die "Usage: space-set-link-password <space-id> <perm-id> --password <p>"
    local space_id="$1" perm_id="$2"; shift 2
    local password=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --password) password="$2"; shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ -n "$password" ]] || die "space-set-link-password: --password is required"
    local body="{\"password\":\"$password\"}"
    do_curl POST "v1beta1/drives/${space_id}/root/permissions/${perm_id}/setPassword" --data "$body" | fmt_permissions
}

# ---------------------------------------------------------------------------
# v1beta1 · Drive item permissions
# ---------------------------------------------------------------------------
cmd_drive_permissions() {
    [[ $# -ge 2 ]] || die "Usage: drive-permissions <space-id> <resource-id>"
    do_curl GET "v1beta1/drives/$1/items/$2/permissions" | fmt_permissions
}

cmd_drive_invite() {
    [[ $# -ge 2 ]] || die "Usage: drive-invite <space-id> <resource-id> --recipients <json> --roles <json> [--expiry <date>]"
    local space_id="$1" resource_id="$2"; shift 2
    local body; body="$(_build_invite_body "$@")"
    do_curl POST "v1beta1/drives/${space_id}/items/${resource_id}/invite" --data "$body" | fmt_permissions
}

cmd_drive_create_link() {
    [[ $# -ge 2 ]] || die "Usage: drive-create-link <space-id> <resource-id> --type <t> [--password <p>] [--expiry <date>]"
    local space_id="$1" resource_id="$2"; shift 2
    local body; body="$(_build_link_body "$@")"
    do_curl POST "v1beta1/drives/${space_id}/items/${resource_id}/createLink" --data "$body" | fmt_permissions
}

cmd_update_received_share() {
    [[ $# -ge 2 ]] || die "Usage: update-received-share <space-id> <resource-id> --hidden <bool>"
    local space_id="$1" resource_id="$2"; shift 2
    local hidden=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --hidden) hidden="$2"; shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ -n "$hidden" ]] || die "update-received-share: --hidden is required (true|false)"
    local body="{\"@client.synchronize\":$hidden}"
    do_curl PATCH "v1beta1/drives/${space_id}/items/${resource_id}" --data "$body" | pretty_print
}

cmd_update_permission() {
    [[ $# -ge 3 ]] || die "Usage: update-permission <space-id> <resource-id> <perm-id> --roles <json> [--expiry <date>]"
    local space_id="$1" resource_id="$2" perm_id="$3"; shift 3
    local body; body="$(_build_permission_body "$@")"
    do_curl PATCH "v1beta1/drives/${space_id}/items/${resource_id}/permissions/${perm_id}" --data "$body" | fmt_permissions
}

cmd_delete_permission() {
    [[ $# -ge 3 ]] || die "Usage: delete-permission <space-id> <resource-id> <perm-id>"
    do_curl DELETE "v1beta1/drives/$1/items/$2/permissions/$3"
    info "Permission $3 removed from item $2."
}

cmd_set_link_password() {
    [[ $# -ge 3 ]] || die "Usage: set-link-password <space-id> <resource-id> <perm-id> --password <p>"
    local space_id="$1" resource_id="$2" perm_id="$3"; shift 3
    local password=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --password) password="$2"; shift 2 ;;
            *) die "Unknown option: $1" ;;
        esac
    done
    [[ -n "$password" ]] || die "set-link-password: --password is required"
    local body="{\"password\":\"$password\"}"
    do_curl POST "v1beta1/drives/${space_id}/items/${resource_id}/permissions/${perm_id}/setPassword" --data "$body" | fmt_permissions
}

# ---------------------------------------------------------------------------
# Parse global options, then dispatch
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        -v) VERBOSE=1; shift ;;
        *)  break ;;
    esac
done

CMD="${1:-help}"
shift || true

case "$CMD" in
    # Users
    me)                     cmd_me                     "$@" ;;
    me-change-password)     cmd_me_change_password     "$@" ;;
    users)                  cmd_users                  "$@" ;;
    user)                   cmd_user                   "$@" ;;
    user-create)            cmd_user_create            "$@" ;;
    user-update)            cmd_user_update            "$@" ;;
    user-delete)            cmd_user_delete            "$@" ;;
    # Groups
    groups)                 cmd_groups                 "$@" ;;
    # Spaces (v1.0)
    spaces)                 cmd_spaces                 "$@" ;;
    space-create)           cmd_space_create           "$@" ;;
    space)                  cmd_space                  "$@" ;;
    space-update)           cmd_space_update           "$@" ;;
    space-disable)          cmd_space_disable          "$@" ;;
    space-restore)          cmd_space_restore          "$@" ;;
    space-purge)            cmd_space_purge            "$@" ;;
    # My spaces & shares (v1beta1)
    my-spaces)              cmd_my_spaces              "$@" ;;
    shared-with-me)         cmd_shared_with_me         "$@" ;;
    shared-by-me)           cmd_shared_by_me           "$@" ;;
    # Roles
    role-definitions)       cmd_role_definitions       "$@" ;;
    # Space permissions (v1beta1)
    space-permissions)      cmd_space_permissions      "$@" ;;
    space-invite)           cmd_space_invite           "$@" ;;
    space-create-link)      cmd_space_create_link      "$@" ;;
    space-update-permission) cmd_space_update_permission "$@" ;;
    space-delete-permission) cmd_space_delete_permission "$@" ;;
    space-set-link-password) cmd_space_set_link_password "$@" ;;
    # Drive item permissions (v1beta1)
    drive-permissions)      cmd_drive_permissions      "$@" ;;
    drive-invite)           cmd_drive_invite           "$@" ;;
    drive-create-link)      cmd_drive_create_link      "$@" ;;
    update-received-share)  cmd_update_received_share  "$@" ;;
    update-permission)      cmd_update_permission      "$@" ;;
    delete-permission)      cmd_delete_permission      "$@" ;;
    set-link-password)      cmd_set_link_password      "$@" ;;
    help|-h|--help)         usage ;;
    *) die "Unknown command: $CMD. Run 'graph.sh help' for usage." ;;
esac
