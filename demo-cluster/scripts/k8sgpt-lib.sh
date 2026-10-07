#!/usr/bin/env bash
# Sourced by up.sh, after lib.sh (it uses demo_kubectl).

# ensure_k8sgpt_restart_safe NAMESPACE
#
# Makes the k8sgpt-sample container survive a restart when its backend is
# not "openai" (e.g. google).
#
# `k8sgpt serve` builds its provider from K8SGPT_BACKEND only when its
# config has no providers yet, i.e. on the pod's first start. The config
# lives on an emptyDir, so after a container restart (memory pressure, a
# crash) it is still there, and serve looks up its --backend flag default,
# "openai", instead. With no provider of that name it exits with "AI
# provider openai not specified in configuration" and crash-loops until the
# pod is replaced. A placeholder "openai" entry gives the restart something
# to find. Real calls are unaffected: the operator names the configured
# backend in every request, so the placeholder is never used to call an AI.
#
# Idempotent. Lost with the pod, so call it after anything that can
# replace the pod.
ensure_k8sgpt_restart_safe() {
  local ns="$1" backend providers
  backend="$(demo_kubectl -n "$ns" get k8sgpt k8sgpt-sample -o jsonpath='{.spec.ai.backend}' 2>/dev/null || true)"
  if [[ -z "$backend" || "$backend" == "openai" ]]; then
    return 0
  fi
  providers="$(demo_kubectl -n "$ns" exec deploy/k8sgpt-sample -c k8sgpt -- /k8sgpt auth list 2>/dev/null \
    | sed -n '/^Active:/,/^Unused:/p' || true)"
  if grep -q '> openai$' <<<"$providers"; then
    return 0
  fi
  if demo_kubectl -n "$ns" exec deploy/k8sgpt-sample -c k8sgpt -- \
       /k8sgpt auth add --backend openai --model restart-placeholder --password restart-placeholder >/dev/null 2>&1; then
    echo "  k8sgpt-sample: added restart-safe openai placeholder (backend is $backend)"
  else
    echo "  WARN: could not add the openai placeholder to k8sgpt-sample; a container restart will crash-loop it until the pod is replaced" >&2
  fi
}
