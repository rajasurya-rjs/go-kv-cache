#!/usr/bin/env sh
# Smoke test a running instance (local binary or container).
# Exits non-zero on the first failed check, so it is safe to use in CI.
#
# Usage: scripts/smoke-test.sh [base-url]   (default http://localhost:7171)
set -eu

BASE_URL="${1:-http://localhost:7171}"

# Wait up to ~10s for the service to come up.
i=0
until curl -sf "$BASE_URL/health" > /dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 20 ]; then
    echo "Service at $BASE_URL did not become healthy" >&2
    exit 1
  fi
  sleep 0.5
done
echo "-> health OK"

curl -sf -X POST "$BASE_URL/put" \
  -H "Content-Type: application/json" \
  -d '{"key":"test","value":"smoke"}' | grep -q '"status":"OK"'
echo "-> put OK"

curl -sf "$BASE_URL/get?key=test" | grep -q '"value":"smoke"'
echo "-> get OK"

curl -sf "$BASE_URL/get?key=missing-key" | grep -q 'Key not found'
echo "-> miss OK"

echo "Smoke test passed!"
