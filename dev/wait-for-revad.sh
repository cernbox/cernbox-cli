#!/usr/bin/env bash
# Wait for the dev environment to answer. EOS takes a while to come up behind
# revad, and the federation partner has to be reachable before the OCM tests
# can run, so a bare "docker compose up" is not enough to start testing.
set -euo pipefail

ATTEMPTS="${CERNBOX_DEV_ATTEMPTS:-60}"
SLEEP="${CERNBOX_DEV_SLEEP:-5}"
DIR="$(cd "$(dirname "$0")" && pwd)"

wait_for() {
    local name="$1" url="$2"
    for i in $(seq 1 "$ATTEMPTS"); do
        if curl -skf -o /dev/null "$url"; then
            echo "$name is up"
            return 0
        fi
        echo "waiting for $name... ($i/$ATTEMPTS)"
        sleep "$SLEEP"
    done

    echo "$name did not come up within $((ATTEMPTS * SLEEP))s" >&2
    docker compose -f "$DIR/docker-compose.yaml" logs --tail=50 >&2 || true
    return 1
}

wait_for revad "${CERNBOX_DEV_URL:-https://localhost/status.php}"
wait_for "the federation partner" "${CERNBOX_PARTNER_URL:-https://localhost:8081/status.php}"
