#!/usr/bin/env bash
# Smoke test: post a test message to the Slack webhook in .env
# (SLACK_WEBHOOK_URL), to confirm the webhook works. Slack is optional; this
# only matters when you configured it. Talks to Slack only, not the cluster.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"
load_env

if [[ -z "${SLACK_WEBHOOK_URL:-}" ]]; then
  die "SLACK_WEBHOOK_URL is not set in .env. Slack is optional; see .env.example to turn it on."
fi

PAYLOAD=$(cat <<EOF
{
  "text": ":white_check_mark: K8sGPT demo cluster: Slack webhook test, $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
}
EOF
)

OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT
# The webhook URL is the secret, so it goes to curl on stdin (--config -),
# not as an argument other users could see in ps.
HTTP_STATUS=$(printf 'url = "%s"\n' "$SLACK_WEBHOOK_URL" | curl -sS -o "$OUT" -w "%{http_code}" \
  -X POST -H "Content-Type: application/json" \
  -d "$PAYLOAD" --config - || true)

if [[ "$HTTP_STATUS" == "200" ]]; then
  echo "OK: Slack webhook accepted the test message."
else
  echo "FAIL: webhook returned ${HTTP_STATUS:-no response}"
  cat "$OUT"
  echo
  exit 1
fi
