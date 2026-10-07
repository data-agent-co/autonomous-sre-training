#!/usr/bin/env bash
# Live memory bar for the recommend-svc leak-fixture container: one line,
# updated in place, showing its working set against its memory limit. Run
# it in a spare terminal after ./scripts/trigger-leak.sh or
# ./scripts/trigger-slowleak.sh. Ctrl-C quits.
#
# Loops forever polling `kubectl top pod` (needs metrics-server, which
# up.sh installs); a transient kubectl error shows a waiting line instead of
# exiting.
#
# Env knobs (rarely needed):
#   NS=demo-apps  LABEL='app=recommend-svc'  CONTAINER=leak-fixture
#   LIMIT_MI=<read from the Deployment, else 128>  INTERVAL_S=2
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"
require_demo_cluster

NS="${NS:-demo-apps}"
LABEL="${LABEL:-app=recommend-svc}"
CONTAINER="${CONTAINER:-leak-fixture}"
INTERVAL_S="${INTERVAL_S:-2}"

# Read the container's memory limit from the Deployment so the bar shows the
# real saturation. Falls back to 128Mi if it can't be read.
if [[ -z "${LIMIT_MI:-}" ]]; then
  detected=$(demo_kubectl -n "$NS" get deploy recommend-svc \
    -o jsonpath="{.spec.template.spec.containers[?(@.name=='${CONTAINER}')].resources.limits.memory}" 2>/dev/null \
    | sed 's/Mi$//')
  if [[ "$detected" =~ ^[0-9]+$ ]]; then
    LIMIT_MI="$detected"
  else
    LIMIT_MI=128
  fi
fi

NC=$'\033[0m'
BOLD=$'\033[1m'
DIM=$'\033[2m'
# Bright variants stand out on dark terminal backgrounds.
GREEN=$'\033[1;92m'
YELLOW=$'\033[1;93m'
RED=$'\033[1;91m'
BG_RED=$'\033[1;97;41m' # white on red, for OOM IMMINENT

trap 'printf "\n"; exit 0' INT TERM

# Single-line in-place update: \r returns to column 0, \033[K clears to the
# end of the line.
while :; do
  # Per-container memory: pick the leak container, not the app container.
  # `kubectl top pod --containers` columns: POD  NAME  CPU  MEMORY
  read -r mib pct < <(
    demo_kubectl -n "$NS" top pod -l "$LABEL" --containers --no-headers 2>/dev/null | \
      awk -v c="$CONTAINER" -v limit="$LIMIT_MI" '
        $2 == c {
          gsub(/Mi/,"",$4);
          printf "%d %d", $4, int(($4/limit)*100);
          exit
        }
      '
  ) || true

  printf '\r\033[K'

  if [[ -z "${mib:-}" ]]; then
    printf "%s  waiting for metrics-server / %s container…%s" "$DIM" "$CONTAINER" "$NC"
    sleep "$INTERVAL_S"
    continue
  fi

  color="$GREEN"; label="healthy"
  if   (( pct >= 90 )); then color="$BG_RED"; label="OOM IMMINENT"
  elif (( pct >= 70 )); then color="$RED";    label="critical"
  elif (( pct >= 50 )); then color="$YELLOW"; label="warming"
  fi

  # Reserved width: "  leak-fixture " (15) + " NNN% " (6) + " NNN/128Mi "
  # (12) + " OOM IMMINENT" (13) + safety (2) = 48 chars.
  cols=$(tput cols 2>/dev/null || echo 80)
  metadata_w=48
  bar_w=$(( cols - metadata_w ))
  (( bar_w < 10 )) && bar_w=10
  filled=$(( bar_w * pct / 100 ))
  (( filled > bar_w )) && filled=$bar_w
  empty=$(( bar_w - filled ))
  bar=""
  for ((i=0; i<filled; i++)); do bar+="█"; done
  for ((i=0; i<empty; i++));  do bar+="░"; done

  printf "  %s%-13s%s %s%s%s %s%3d%%%s %s%d/%dMi%s %s%s%s" \
    "$BOLD" "$CONTAINER" "$NC" \
    "$color" "$bar" "$NC" \
    "$BOLD" "$pct" "$NC" \
    "$BOLD" "$mib" "$LIMIT_MI" "$NC" \
    "$color" "$label" "$NC"

  sleep "$INTERVAL_S"
done
