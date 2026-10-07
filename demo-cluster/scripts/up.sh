#!/usr/bin/env bash
# Bring up the demo cluster end to end: a kind cluster with the sample apps,
# kube-prometheus-stack (Prometheus + Grafana), the K8sGPT operator and the
# watcher. Idempotent: safe to re-run against an existing cluster.
#
# Settings come from demo-cluster/.env, which is optional (see .env.example):
# without an LLM key K8sGPT runs without AI explanations, and without a
# Slack webhook its Results stay in the cluster.
#
# Environment:
#   CLUSTER_NAME=demo-cluster  kind cluster to create or reuse
#   GRAFANA_PORT=3000          local port for the Grafana port-forward
#   ENV_FILE=demo-cluster/.env settings file to load
#   KIND_NODE_IMAGE=...        node image for a new cluster (empty = kind's default)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=k8sgpt-lib.sh
source "$SCRIPT_DIR/k8sgpt-lib.sh"
REPO_ROOT="$(cd "$DEMO_DIR/.." && pwd)"

# --- Pinned versions ---------------------------------------------------------
# Container images in manifests/ are pinned in place.
# Node image built for kind v0.33.0 (Kubernetes v1.37.0); needs kind v0.33.0+.
KIND_NODE_IMAGE="${KIND_NODE_IMAGE-kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5}"
METRICS_SERVER_VERSION="v0.9.0"
KUBE_PROMETHEUS_STACK_VERSION="91.9.0"
K8SGPT_OPERATOR_VERSION="0.2.29"
# Tag of ghcr.io/k8sgpt-ai/k8sgpt, the image the operator runs (set in the CR).
# shellcheck disable=SC2034  # read through ${!var} in do_k8sgpt
K8SGPT_VERSION="v0.4.39"

# --- Settings ----------------------------------------------------------------
# Before the cd below, so a relative ENV_FILE is relative to where you are.
load_env
cd "$DEMO_DIR"

NS_APPS="demo-apps"
NS_MON="monitoring"
NS_K8SGPT="k8sgpt-system"
WATCHER_RELEASE="k8s-watcher"
WATCHER_IMAGE="k8s-watcher:dev"
# The chart names its Deployment k8s-watcher.fullname, which is the release name
# when that already contains the chart name ("k8s-watcher").
WATCHER_DEPLOY="$WATCHER_RELEASE"
GRAFANA_PORT="${GRAFANA_PORT:-3000}"

: "${LLM_API_KEY:=}"
: "${SLACK_WEBHOOK_URL:=}"
: "${K8SGPT_LOCAL:=false}"
if [[ "$K8SGPT_LOCAL" == "true" ]]; then
  # A model served on the host through an OpenAI-compatible API (Ollama by
  # default). It needs no key, so LLM_API_KEY is never sent. Cluster data
  # must stay local: refuse an endpoint that is not a local or private
  # address, such as a cloud K8SGPT_BASE_URL left in .env.
  K8SGPT_AI_ENABLED=true
  K8SGPT_BACKEND=openai
  LLM_API_KEY=""
  : "${K8SGPT_MODEL:=gpt-oss:20b}"
  : "${K8SGPT_BASE_URL:=http://host.docker.internal:11434/v1}"
  llm_host="${K8SGPT_BASE_URL#*://}"
  llm_host="${llm_host%%[:/]*}"
  case "$llm_host" in
    host.docker.internal|localhost|127.*|10.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*|192.168.*) ;;
    *) die "K8SGPT_LOCAL=true, but K8SGPT_BASE_URL=$K8SGPT_BASE_URL is not a local address. Empty K8SGPT_BASE_URL and K8SGPT_MODEL in .env for the Ollama defaults, or set K8SGPT_LOCAL=false." ;;
  esac
  AI_SUMMARY="on: $K8SGPT_MODEL at $K8SGPT_BASE_URL"
elif [[ -n "$LLM_API_KEY" ]]; then
  K8SGPT_AI_ENABLED=true
  : "${K8SGPT_BACKEND:=openai}"
  : "${K8SGPT_MODEL:=gpt-4o-mini}"
  : "${K8SGPT_BASE_URL:=}"
  AI_SUMMARY="on: backend $K8SGPT_BACKEND, model $K8SGPT_MODEL"
else
  # No LLM: K8sGPT still analyzes and writes Results, without explanations.
  K8SGPT_AI_ENABLED=false
  K8SGPT_BACKEND=openai
  K8SGPT_MODEL=""
  K8SGPT_BASE_URL=""
  AI_SUMMARY="off (no LLM_API_KEY in .env): Results without AI explanations"
fi
# The k8sgpt-sample pod reads the key from its secret only when it starts.
# The CR puts this hash of the whole AI setup (key, backend, model, URL) in
# the pod template, so any change to it changes the template, the operator
# replaces the pod, and do_k8sgpt can wait for that exact rollout.
K8SGPT_AI_HASH="$(printf '%s\n' "${LLM_API_KEY:-unused}" "$K8SGPT_AI_ENABLED" \
  "$K8SGPT_BACKEND" "$K8SGPT_MODEL" "$K8SGPT_BASE_URL" | cksum | awk '{print $1}')"
if [[ -n "$SLACK_WEBHOOK_URL" ]]; then
  SLACK_SUMMARY="on: K8sGPT posts each new Result"
else
  SLACK_SUMMARY="off (no SLACK_WEBHOOK_URL in .env): Results stay in the cluster"
fi

# --- Prerequisites -----------------------------------------------------------
missing=()
for tool in docker kind kubectl helm curl; do
  command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
if [[ ${#missing[@]} -gt 0 ]]; then
  die "missing tools: ${missing[*]}"
fi
docker info >/dev/null 2>&1 || die "Docker is not running."

# --- Progress helper ---------------------------------------------------------
SPINNER_FRAMES='◐◓◑◒'
LOG_DIR="$STATE_DIR/up-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$LOG_DIR"
LABEL_WIDTH=34
STEP_NO=0
STEP_PID=""

# Hide / show the cursor on a terminal; best-effort.
hide_cursor() { if [[ -t 1 ]]; then tput civis 2>/dev/null || true; fi; }
show_cursor() { if [[ -t 1 ]]; then tput cnorm 2>/dev/null || true; fi; }

# Steps run in the background, where Ctrl-C does not reach them; stop the
# running one (and its child processes) explicitly.
on_interrupt() {
  if [[ -n "$STEP_PID" ]]; then
    pkill -P "$STEP_PID" 2>/dev/null || true
    kill "$STEP_PID" 2>/dev/null || true
  fi
  show_cursor
  printf '\n\nInterrupted. Step logs: %s\n' "$LOG_DIR" >&2
  exit 130
}
trap show_cursor EXIT
trap on_interrupt INT TERM

# run_step MODE LABEL CMD...: run CMD with a spinner; its stdout+stderr go
# to a per-step log under $LOG_DIR. On failure, prints the log path and its
# last lines, then exits (MODE=required) or carries on (MODE=optional).
# Call it plainly, never as `run_step ... || ...`: in that position bash
# turns `set -e` off inside CMD, so a failing command would not stop it.
run_step() {
  local mode="$1" label="$2"
  shift 2
  STEP_NO=$((STEP_NO + 1))
  local safe="${label//[^A-Za-z0-9.+-]/_}"
  local logfile
  logfile="$LOG_DIR/$(printf '%02d' "$STEP_NO")-${safe}.log"
  local start=$SECONDS

  hide_cursor
  ( "$@" ) >"$logfile" 2>&1 &
  STEP_PID=$!

  local i=0 frame
  while kill -0 "$STEP_PID" 2>/dev/null; do
    if [[ -t 1 ]]; then
      frame="${SPINNER_FRAMES:$((i % ${#SPINNER_FRAMES})):1}"
      printf "\r  %s  %-${LABEL_WIDTH}s" "$frame" "$label"
    fi
    i=$((i + 1))
    sleep 0.15
  done

  # `|| rc=$?` keeps set -e from exiting here, before the report below.
  local rc=0
  wait "$STEP_PID" || rc=$?
  STEP_PID=""
  local elapsed=$((SECONDS - start))
  show_cursor

  if [[ $rc -eq 0 ]]; then
    printf "\r     %-${LABEL_WIDTH}s  done  %3ds\n" "$label" "$elapsed"
    return 0
  fi
  if [[ "$mode" == "optional" ]]; then
    printf "\r     %-${LABEL_WIDTH}s  WARN  %3ds\n" "$label" "$elapsed"
  else
    printf "\r     %-${LABEL_WIDTH}s  FAIL  %3ds\n" "$label" "$elapsed"
  fi
  echo
  echo "    log: $logfile"
  echo "    last lines:"
  tail -20 "$logfile" | sed 's/^/      /'
  echo
  if [[ "$mode" != "optional" ]]; then
    echo "Setup stopped at \"$label\" (exit $rc). Fix the cause and re-run ${RUN_PREFIX}./scripts/up.sh." >&2
    exit "$rc"
  fi
}

step() { run_step required "$@"; }
optional_step() { run_step optional "$@"; }

# --- Step bodies -------------------------------------------------------------
do_cluster() {
  if kind_cluster_exists; then
    echo "kind cluster '$CLUSTER_NAME' already exists, reusing it"
  elif [[ -n "$KIND_NODE_IMAGE" ]]; then
    kind create cluster --name "$CLUSTER_NAME" --config kind-config.yaml \
      --image "$KIND_NODE_IMAGE" --wait 120s
  else
    kind create cluster --name "$CLUSTER_NAME" --config kind-config.yaml --wait 120s
  fi
  require_demo_cluster
}

do_namespaces() {
  for ns in "$NS_APPS" "$NS_MON" "$NS_K8SGPT"; do
    demo_kubectl create namespace "$ns" --dry-run=client -o yaml | demo_kubectl apply -f -
  done
}

do_metrics_server() {
  # metrics-server backs `kubectl top` and scripts/leak-progress.sh. kind's
  # kubelets use self-signed certs, so --kubelet-insecure-tls is mandatory.
  demo_kubectl apply -f \
    "https://github.com/kubernetes-sigs/metrics-server/releases/download/${METRICS_SERVER_VERSION}/components.yaml"
  local args
  args="$(demo_kubectl -n kube-system get deploy metrics-server -o jsonpath='{.spec.template.spec.containers[0].args}')"
  if [[ "$args" != *--kubelet-insecure-tls* ]]; then
    demo_kubectl -n kube-system patch deploy metrics-server --type=json -p='[
      {"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}
    ]'
  fi
  demo_kubectl -n kube-system rollout status deploy/metrics-server --timeout=120s
}

do_build_images() {
  docker build -q -t payments-api:demo manifests/payments-api/
  docker build -q -t recommend-svc:demo manifests/recommend-svc/
  kind load docker-image payments-api:demo recommend-svc:demo --name "$CLUSTER_NAME"
}

do_deploy_apps() {
  demo_kubectl apply -f manifests/payments-api/deployment.yaml
  demo_kubectl apply -f manifests/recommend-svc/deployment.yaml
  demo_kubectl apply -f manifests/catalog-svc/deployment.yaml
  demo_kubectl apply -f manifests/loadgen/deployment.yaml
  # A re-run right after the OOMKill demo finds payments-api in its restart
  # back-off (up to 5 minutes), so the rollout waits below would time out;
  # with the loop still running, every new pod is OOMKilled again. Stop the
  # loop and replace any crash-looping pod, as soft-reset.sh does.
  if stop_pidfile "$OOM_LOOP_PIDFILE" "$OOM_LOOP_MATCH"; then
    echo "stopped the OOMKill loop"
  fi
  local app
  for app in payments-api recommend-svc catalog-svc loadgen; do
    if demo_kubectl -n "$NS_APPS" get pods -l "app=$app" \
         -o jsonpath='{.items[*].status.containerStatuses[*].state.waiting.reason}' |
         grep -q CrashLoopBackOff; then
      echo "$app is in CrashLoopBackOff: replacing its pod"
      demo_kubectl -n "$NS_APPS" rollout restart "deploy/$app"
    fi
  done
  demo_kubectl -n "$NS_APPS" rollout status deploy/payments-api  --timeout=120s
  demo_kubectl -n "$NS_APPS" rollout status deploy/recommend-svc --timeout=120s
  demo_kubectl -n "$NS_APPS" rollout status deploy/catalog-svc   --timeout=120s
  demo_kubectl -n "$NS_APPS" rollout status deploy/loadgen       --timeout=60s
}

do_prometheus_stack() {
  # With Slack, Alertmanager posts the OOMKill alert through slack-relay.
  local slack_values=()
  if [[ -n "$SLACK_WEBHOOK_URL" ]]; then
    slack_values=(-f helm/alertmanager-slack-values.yaml)
  fi
  demo_helm upgrade --install prometheus-stack \
    oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack \
    --version "$KUBE_PROMETHEUS_STACK_VERSION" \
    --namespace "$NS_MON" \
    -f helm/prometheus-stack-values.yaml \
    ${slack_values[@]+"${slack_values[@]}"} \
    --wait --timeout 10m
}

do_servicemonitors() {
  demo_kubectl apply -f manifests/payments-api/servicemonitor.yaml
  demo_kubectl apply -f manifests/recommend-svc/servicemonitor.yaml
}

do_dashboard() {
  demo_kubectl create configmap demo-cluster-overview-dashboard \
    --from-file=demo-cluster-overview.json=grafana/demo-cluster-overview.json \
    --namespace "$NS_MON" \
    --dry-run=client -o yaml | demo_kubectl apply -f -
  demo_kubectl -n "$NS_MON" label configmap demo-cluster-overview-dashboard \
    grafana_dashboard=1 --overwrite >/dev/null
}

do_k8sgpt_operator() {
  demo_helm upgrade --install k8sgpt-operator k8sgpt-operator \
    --repo https://charts.k8sgpt.ai \
    --version "$K8SGPT_OPERATOR_VERSION" \
    --namespace "$NS_K8SGPT" \
    -f helm/k8sgpt-values.yaml \
    --wait --timeout 5m
  demo_kubectl wait --for=condition=established --timeout=60s \
    crd/k8sgpts.core.k8sgpt.ai crd/results.core.k8sgpt.ai
}

do_slack() {
  # The webhook goes in on stdin, not as an argument other users could see
  # in ps.
  local old_url new_url
  old_url="$(demo_kubectl -n "$NS_K8SGPT" get secret k8sgpt-slack -o jsonpath='{.data.webhook-url}' 2>/dev/null || true)"
  printf %s "$SLACK_WEBHOOK_URL" | demo_kubectl create secret generic k8sgpt-slack \
    --from-file=webhook-url=/dev/stdin \
    --namespace "$NS_K8SGPT" \
    --dry-run=client -o yaml | demo_kubectl apply -f -
  new_url="$(demo_kubectl -n "$NS_K8SGPT" get secret k8sgpt-slack -o jsonpath='{.data.webhook-url}')"

  # The operator's Slack sink posts through the dedupe relay; slack-probe
  # uses k8sgpt-slack directly.
  demo_kubectl create secret generic k8sgpt-slack-relay \
    --from-literal=webhook-url="http://slack-relay.$NS_K8SGPT.svc.cluster.local/" \
    --namespace "$NS_K8SGPT" \
    --dry-run=client -o yaml | demo_kubectl apply -f -
  demo_kubectl apply -f manifests/slack-relay/deployment.yaml
  demo_kubectl apply -f manifests/slack-probe/deployment.yaml
  # Both read the webhook from k8sgpt-slack only when they start.
  if [[ -n "$old_url" && "$old_url" != "$new_url" ]]; then
    demo_kubectl -n "$NS_K8SGPT" rollout restart deploy/slack-relay deploy/slack-probe
  fi
  demo_kubectl -n "$NS_K8SGPT" rollout status deploy/slack-relay --timeout=90s
  demo_kubectl -n "$NS_K8SGPT" rollout status deploy/slack-probe --timeout=90s
}

do_slack_off() {
  # Remove what an earlier run with Slack configured left behind.
  demo_kubectl delete -f manifests/slack-probe/deployment.yaml --ignore-not-found
  demo_kubectl delete -f manifests/slack-relay/deployment.yaml --ignore-not-found
  demo_kubectl -n "$NS_K8SGPT" delete secret k8sgpt-slack k8sgpt-slack-relay --ignore-not-found
}

do_k8sgpt() {
  # The key goes in on stdin, not as an argument other users could see in ps.
  printf %s "${LLM_API_KEY:-unused}" | demo_kubectl create secret generic k8sgpt-llm \
    --from-file=api-key=/dev/stdin \
    --namespace "$NS_K8SGPT" \
    --dry-run=client -o yaml | demo_kubectl apply -f -

  # Render the CR (kept in the log dir for reference). Without Slack, drop
  # the sink block, which the CR keeps last.
  local cr="$LOG_DIR/k8sgpt-cr.yaml" var value subst=()
  # Replace only these ${NAME} placeholders; any other $ in the file stays.
  # Each value is escaped for sed's replacement text (\, | and &).
  for var in K8SGPT_AI_ENABLED K8SGPT_BACKEND K8SGPT_MODEL K8SGPT_BASE_URL K8SGPT_VERSION K8SGPT_AI_HASH; do
    value="$(printf '%s' "${!var}" | sed -e 's/[\\|&]/\\&/g')"
    subst+=(-e "s|\\\${$var}|$value|g")
  done
  sed "${subst[@]}" manifests/k8sgpt/k8sgpt-cr.yaml >"$cr"
  if [[ -z "$SLACK_WEBHOOK_URL" ]]; then
    sed -i.bak '/^  sink:/,$d' "$cr"
    rm -f "$cr.bak"
  fi
  demo_kubectl apply -f "$cr"

  # Restart the operator so its AI circuit breaker starts closed. After more
  # than spec.ai.backOff.maxRetries failed AI calls (a bad key, a retired
  # model) the operator stops sending explain=true, and only a process
  # restart turns it back on, so without this, fixing .env and re-running
  # up.sh leaves every Result without AI text.
  demo_kubectl -n "$NS_K8SGPT" rollout restart deploy/k8sgpt-operator-controller-manager
  demo_kubectl -n "$NS_K8SGPT" rollout status deploy/k8sgpt-operator-controller-manager --timeout=120s

  # k8sgpt-sample registers spec.ai.backend as its "Active" provider and
  # leaves the config file's "Default" at openai. The operator names the
  # backend in every Analyze call, so its scans are fine. Setting the
  # default on the pod (`k8sgpt auth default`) does not help: the running
  # server never re-reads it. A hand-run `k8sgpt analyze --explain` needs
  # `--backend <backend>`. ensure_k8sgpt_restart_safe (k8sgpt-lib.sh) is
  # what keeps a container restart from crash-looping the pod.
  #
  # The operator creates or updates deploy/k8sgpt-sample on its own time.
  # Wait until it carries this run's AI-setup hash, so the rollout below is
  # the one with the current key, backend, model and URL. (`rollout restart` would not do: the operator
  # removes its annotation again, which rolls the pod a second time.)
  local hash=""
  for _ in {1..60}; do
    hash="$(demo_kubectl -n "$NS_K8SGPT" get deploy k8sgpt-sample \
      -o jsonpath='{.spec.template.metadata.annotations.sre-training/llm-config-hash}' 2>/dev/null || true)"
    [[ "$hash" == "$K8SGPT_AI_HASH" ]] && break
    sleep 1
  done
  if [[ "$hash" != "$K8SGPT_AI_HASH" ]]; then
    echo "deploy/k8sgpt-sample did not get the current LLM settings within 60s" >&2
    return 1
  fi
  demo_kubectl -n "$NS_K8SGPT" rollout status deploy/k8sgpt-sample --timeout=120s
  ensure_k8sgpt_restart_safe "$NS_K8SGPT"
}

do_watcher() {
  docker build -q -t "$WATCHER_IMAGE" "$REPO_ROOT/k8s-watcher"
  kind load docker-image "$WATCHER_IMAGE" --name "$CLUSTER_NAME"

  local upgrading=0
  if demo_helm -n "$NS_K8SGPT" status "$WATCHER_RELEASE" >/dev/null 2>&1; then
    upgrading=1
  fi
  demo_helm upgrade --install "$WATCHER_RELEASE" "$REPO_ROOT/charts/k8s-watcher" \
    --namespace "$NS_K8SGPT" \
    -f helm/watcher-values.yaml \
    --wait --timeout 3m
  # The image was rebuilt under the same tag (pullPolicy Never), so on an
  # upgrade Helm sees no pod-spec change and the old pod keeps running the
  # old code. Restart it to pick up the freshly loaded image.
  if [[ $upgrading -eq 1 ]]; then
    demo_kubectl -n "$NS_K8SGPT" rollout restart "deploy/$WATCHER_DEPLOY"
    demo_kubectl -n "$NS_K8SGPT" rollout status "deploy/$WATCHER_DEPLOY" --timeout=120s
  fi
}

do_grafana() {
  GRAFANA_PORT="$GRAFANA_PORT" "$SCRIPT_DIR/grafana.sh"
}

do_smoke_test() {
  # Fail loudly if any pod in the demo's namespaces is stuck on
  # ImagePullBackOff / CrashLoopBackOff / long-Pending.
  "$SCRIPT_DIR/check-pods.sh" "$NS_APPS"    "" 60
  "$SCRIPT_DIR/check-pods.sh" "$NS_K8SGPT"  "" 60
  "$SCRIPT_DIR/check-pods.sh" "$NS_MON"     "" 60
}

# --- Run ---------------------------------------------------------------------
echo
echo "Bringing up kind cluster '$CLUSTER_NAME' (context $KUBE_CONTEXT)"
echo "  AI explanations: $AI_SUMMARY"
echo "  Slack:           $SLACK_SUMMARY"
echo

step "kind cluster"                   do_cluster
step "namespaces"                     do_namespaces
step "metrics-server"                 do_metrics_server
step "build + load sample images"     do_build_images
step "deploy sample apps + loadgen"   do_deploy_apps
step "kube-prometheus-stack"          do_prometheus_stack
step "ServiceMonitors"                do_servicemonitors
step "Grafana dashboard"              do_dashboard
step "K8sGPT operator"                do_k8sgpt_operator
if [[ -n "$SLACK_WEBHOOK_URL" ]]; then
  step "Slack relay + probe"          do_slack
else
  step "Slack relay + probe (off)"    do_slack_off
fi
step "K8sGPT CR + LLM secret"         do_k8sgpt
step "watcher"                        do_watcher
step "pod smoke-test"                 do_smoke_test

optional_step "Grafana port-forward"  do_grafana

KC="kubectl --context $KUBE_CONTEXT"
RUN="$RUN_PREFIX"
GRAFANA_RUN="$RUN"
if [[ "$GRAFANA_PORT" != "3000" ]]; then
  GRAFANA_RUN+="GRAFANA_PORT=$GRAFANA_PORT "
fi
VERIFY="  $KC get pods -A"
if [[ -n "$SLACK_WEBHOOK_URL" ]]; then
  VERIFY+=$'\n  ./scripts/check-slack.sh'
fi
GRAFANA_URL="http://localhost:${GRAFANA_PORT}/d/demo-cluster-overview"
if pidfile_alive "$GRAFANA_PF_PIDFILE" "$GRAFANA_PF_MATCH"; then
  GRAFANA_STATUS="port-forwarded in the background:"
else
  GRAFANA_STATUS="not port-forwarded (see the WARN above). Start it with ${GRAFANA_RUN}./scripts/grafana.sh, then open:"
fi

cat <<EOF

Demo cluster '$CLUSTER_NAME' is up.   (logs: $LOG_DIR)

  AI explanations: $AI_SUMMARY
  Slack:           $SLACK_SUMMARY

Commands below name the demo cluster's context, so your current context
doesn't matter.

Verify:
$VERIFY

Trigger an incident:
  ${RUN}./scripts/trigger-oomkill.sh        (stop: --stop)
  ${RUN}./scripts/trigger-leak.sh           (stop: --off)
  ${RUN}./scripts/trigger-slowleak.sh       (same leak, slower; stop: --off)

See what K8sGPT and the watcher found:
  $KC -n $NS_K8SGPT get results
  $KC -n $NS_K8SGPT get results -o yaml

Grafana is $GRAFANA_STATUS
  $GRAFANA_URL  (admin / admin)
  Restart the port-forward: ${GRAFANA_RUN}./scripts/grafana.sh
  Stop it:                  ${RUN}./scripts/grafana.sh --stop

Tear everything down:  ${RUN}./scripts/down.sh

EOF
