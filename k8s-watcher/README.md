# watcher

A small Kubernetes controller that writes K8sGPT `Result` objects
(`results.core.k8sgpt.ai`). It runs two pipelines, called lanes, in one
process:

- **EV lane (events-watcher):** turns Kubernetes Warning events into Results.
- **CM lane (configmap-watcher):** turns changes to opted-in ConfigMaps into
  Results.

Its Results sit next to the ones K8sGPT writes, so the same tools see both,
for example `kubectl get results`.

The two pipelines come from the Detection Pack, an Apache-2.0 add-on for
K8sGPT (see [Attribution](#attribution)).

## What each lane does

### EV lane

1. Watches core/v1 events of type `Warning` across the cluster.
2. Keeps only the reasons on an allow-list (`internal/pack/ev/filter`):
   - pod lifecycle: `BackOff`, `Killing`, `Unhealthy`, `OOMKilled`, `Error`,
     `ContainerCannotRun`, and `Failed` on an image pull;
   - scheduling: `FailedScheduling` and `Unschedulable`, once the event is
     60s old;
   - storage: `FailedMount`, `FailedAttachVolume`, `VolumeFailedDelete`,
     `ProvisioningFailed`;
   - eviction: `Evicted`;
   - cert-manager: `Failed` on a `Certificate` or `CertificateRequest`.
3. Reports each object and reason once per dedup window (`EV_DEDUP_WINDOW`,
   5m by default).
4. Writes a Result named `ev-<reason>-<object>-<hash>`. It holds the event
   text and a ranked list of likely causes. Its labels
   (`k8sgpt-detection-pack.io/...`) give the category, signal type and
   severity, plus a fault class and an episode ID for most signals.

A failing probe (`Unhealthy`) can be enriched when `PROBE_ENRICHMENT_ENABLED`
is on. The lane then also caches Pods, and reads the pod's CPU limit and
recent restarts from that cache. If `PROMETHEUS_URL` is set, it also asks
Prometheus for the pod's CPU throttle rate, with a 2s timeout. With this
context, a probe timeout on a throttled pod with a small CPU limit gets
`fault_class=resource_exhaustion`, and a refused connection on a pod that
restarted 2 or more times in 15 minutes gets `crash_loop`. Without it, both
stay `indeterminate`. These lookups run on 4 workers of their own, so a slow
Prometheus never holds up other events.

### CM lane

1. Watches ConfigMaps across the cluster. The cache keeps a digest of each
   value, never the value itself.
2. Reports only ConfigMaps labelled `k8sgpt-detection-pack.io/critical=true`.
   A labelled ConfigMap is reported when it is:
   - created after the watcher started (ConfigMaps that already exist at
     startup are not reported);
   - changed in `data` or `binaryData`, if it carries the label before and
     after the change;
   - deleted.

   Adding the label to an existing ConfigMap is not reported, and neither
   is a change to its labels or annotations only.
3. Writes one Result per ConfigMap, named `cm-configmap-<name>-<hash>`, with
   signal type `cm_mutation` (or `cm_mutation_deleted`). The text counts the
   keys, for example `ConfigMap demo-apps/feature-flags modified: 0 added,
   0 removed, 1 changed`. The details list the changed keys with each
   value's length and a short hash, never the value.

### Writing Results

Both lanes write Results into `RESULT_NAMESPACE` with get, create and update
calls. When a Result with the same name exists and the new one says
something different, it is updated: `k8sgpt-detection-pack.io/observation-count`
goes up, `last-observed-at` is set and `first-observed-at` is kept. When
nothing changed, nothing is written. Writes go through a bounded queue with
a rate limit; when the queue is full, the oldest waiting Result is dropped
and counted in `k8sgpt_pack_emitter_dropped_total`.

Each lane serves HTTP on its own port:

| Path       | Meaning |
|------------|---------|
| `/healthz` | 200 while the process runs (liveness). |
| `/readyz`  | 503 until the lane's informers have finished their first list, then 200. |
| `/metrics` | The lane's metrics: `k8sgpt_pack_results_emitted_total`, `k8sgpt_pack_emitter_*` and `k8sgpt_pack_component_info`, each with a `component` label. |

If a lane's first list does not finish within 2 minutes (for example, a
missing RBAC rule), the process exits. Its error names the cause, such as
`events is forbidden`.

## How the demo runs it

`demo-cluster/scripts/up.sh` builds, loads and installs the watcher on every
run. The steps below run against the kind cluster's context only:

```sh
docker build -t k8s-watcher:dev k8s-watcher/
kind load docker-image k8s-watcher:dev --name demo-cluster
helm upgrade --install k8s-watcher charts/k8s-watcher \
  --kube-context kind-demo-cluster -n k8sgpt-system \
  -f demo-cluster/helm/watcher-values.yaml --wait
```

The image is never pushed to a registry. The values file sets
`pullPolicy: Never` and runs the CM lane only, with a ServiceMonitor so
Prometheus scrapes it. The K8sGPT operator, which up.sh installs earlier,
provides the Result CRD.

Try the CM lane. Create a labelled ConfigMap, change it, then look at the
Result:

```sh
kubectl --context kind-demo-cluster apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: feature-flags
  namespace: demo-apps
  labels:
    k8sgpt-detection-pack.io/critical: "true"
data:
  checkout: "on"
EOF

kubectl --context kind-demo-cluster -n demo-apps patch configmap feature-flags \
  --type merge -p '{"data":{"checkout":"off"}}'

kubectl --context kind-demo-cluster -n k8sgpt-system get results \
  -l k8sgpt-detection-pack.io/component=cm -o yaml
```

The chart also has a check for each enabled lane. It creates a test object
and waits for the matching Result:

```sh
helm test k8s-watcher --kube-context kind-demo-cluster -n k8sgpt-system --logs
```

`--logs` needs Helm 4; with Helm 3 it fails on the chart's non-Pod test
resources, so leave it out.

## Configuration

The watcher has no command-line flags. It reads environment variables; the
chart sets them from its values.

| Variable | Default | Chart value | Meaning |
|----------|---------|-------------|---------|
| `EV_ENABLED` | `true` | `ev.enabled` | Run the EV lane. |
| `CM_ENABLED` | `true` | `cm.enabled` | Run the CM lane. At least one lane must be on. |
| `RESULT_NAMESPACE` | `k8sgpt-system` | `resultNamespace` | Namespace the Results are written to. |
| `EV_HEALTH_ADDR` | `:8080` | `ev.port` | EV lane HTTP address. |
| `CM_HEALTH_ADDR` | `:8081` | `cm.port` | CM lane HTTP address. |
| `DRY_RUN` | `false` | `dryRun` | Print Results to stdout as JSON instead of writing them. |
| `EV_DEDUP_WINDOW` | `5m` | `ev.dedupWindow` | How long one object and reason is reported only once. Keep it at `2m15s` or more: with probe enrichment, a shorter window can let an older probe failure overwrite a newer one's Result. |
| `EV_SWEEP_INTERVAL` | `1m` | `ev.sweepInterval` | How often expired dedup entries are cleared. |
| `PROBE_ENRICHMENT_ENABLED` | `true` | `ev.probeEnrichment.enabled` | Enrich probe failures; also makes the EV lane cache Pods. |
| `PROMETHEUS_URL` | empty | `ev.probeEnrichment.prometheusUrl` | Prometheus base URL for the throttle lookup. Empty skips it. |
| `LOG_LEVEL` | `info` | `logLevel` | `debug`, `info`, `warn` or `error`. Logs are JSON on stdout. |

Booleans accept `true`/`false`, `1`/`0`, `yes`/`no` and `on`/`off`.
Durations use Go syntax (`90s`, `5m`).

The API server comes from `KUBECONFIG` if set, else the in-cluster
config, else `~/.kube/config`.

Permissions the watcher needs (`charts/k8s-watcher/templates/rbac.yaml`):

- cluster-wide `list` and `watch` on `events` for the EV lane, plus `pods`
  when probe enrichment is on;
- cluster-wide `list` and `watch` on `configmaps` for the CM lane;
- `get`, `create` and `update` on `results.core.k8sgpt.ai` in
  `RESULT_NAMESPACE`.

The Result CRD must be installed; the K8sGPT operator installs it.

## Package layout

```
cmd/watcher/                  main: reads the environment, builds the clients, runs the lanes
internal/informer/            informers for Warning events, ConfigMaps (digests in place of
                              values) and Pods, with a time limit on the first list
internal/adapt/               turns informer output into pipeline input; the Source each lane reads
internal/k8sgpt/v1alpha1/     the K8sGPT Result type
internal/pack/emitter/        writes Results: no-change check, bounded queue, rate limit, dry run
internal/pack/metrics/        per-lane metrics registry
internal/pack/ev/app/         EV lane: pipeline, probe workers, HTTP server
internal/pack/ev/event/       the EV pipeline's event type
internal/pack/ev/filter/      reason allow-list: category, signal type, severity
internal/pack/ev/dedup/       one Result per object and reason per window
internal/pack/ev/mapper/      Result name, labels, text, likely causes, fault class, episode
internal/pack/ev/probectx/    Pod-cache and Prometheus lookups for probe enrichment
internal/pack/cm/app/         CM lane: pipeline, HTTP server
internal/pack/cm/mutation/    the CM pipeline's change type
internal/pack/cm/filter/      the critical-label opt-in
internal/pack/cm/diff/        added, removed and changed keys
internal/pack/cm/mapper/      Result for a ConfigMap change
```

## Development

From the repository root (`make help` lists every target):

```sh
make build      # go build; binary in k8s-watcher/bin/watcher
make test       # go test -race ./...
make lint       # golangci-lint (config in .golangci.yml)
make vuln       # govulncheck
make image      # docker build -t k8s-watcher:dev k8s-watcher/
make ci         # every check CI runs; none needs a cluster
```

The module needs Go 1.27.1 or newer (see `go.mod`). The Dockerfile builds
with `golang:1.27.1`.

To run the watcher on your machine, give it a kubeconfig file that holds
only the kind cluster, so your current context is never used.
`DRY_RUN=true` prints Results instead of writing them:

```sh
cfg=$(mktemp)
kind get kubeconfig --name demo-cluster > "$cfg"
(cd k8s-watcher && KUBECONFIG="$cfg" DRY_RUN=true go run ./cmd/watcher)
```

## Attribution

The code under `internal/pack` is copied from the Detection Pack, an add-on
for K8sGPT, under the Apache License 2.0. It has been changed: the two
pipelines' emitters and metrics are merged, the package layout is flattened,
and bugs are fixed. `internal/k8sgpt/v1alpha1` copies the Result type from
the K8sGPT operator (Apache-2.0). [NOTICE](NOTICE) lists the sources and
every change, and [LICENSE-APACHE](LICENSE-APACHE) has the license text.

That is why the label keys, the `Result.spec.backend` values
(`k8sgpt-detection-pack:ev` and `:cm`) and the `k8sgpt_pack_*` metric names
still use the Pack's names.

This project is not affiliated with or endorsed by K8sGPT or the CNCF.
