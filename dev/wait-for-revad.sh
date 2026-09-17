#!/usr/bin/env bash
# Wait for the dev revad to answer. EOS takes a while to come up behind it, so
# a bare "docker compose up" is not enough to start testing against.
set -euo pipefail

URL="${CERNBOX_DEV_URL:-http://localhost/status.php}"
ATTEMPTS="${CERNBOX_DEV_ATTEMPTS:-60}"
SLEEP="${CERNBOX_DEV_SLEEP:-5}"

for i in $(seq 1 "$ATTEMPTS"); do
    if curl -sf -o /dev/null "$URL"; then
        echo "revad is up"
        exit 0
    fi
    echo "waiting for revad... ($i/$ATTEMPTS)"
    sleep "$SLEEP"
done

echo "revad did not come up within $((ATTEMPTS * SLEEP))s" >&2
docker compose -f "$(dirname "$0")/docker-compose.yaml" logs --tail=50 revad >&2 || true
exit 1
