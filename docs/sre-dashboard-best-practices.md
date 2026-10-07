# SRE Dashboard Best Practices

*A practical walkthrough of how we built the [demo-cluster](../demo-cluster/) dashboard — the metrics we monitor, the Grafana + Prometheus configuration, and the SRE patterns we leaned on. The dashboard is intentionally minimal but covers a complete fault-detection story; everything here is meant to be reusable.*

## TL;DR

We built a 19-panel Grafana dashboard for a 3-app demo Kubernetes cluster that demonstrates the **golden signals** (Latency / Throughput / Errors / Saturation) of an SRE workflow alongside an AI agent (K8sGPT) that auto-detects faults. The dashboard exposes two parallel health scores — cluster health and agent self-health — so a viewer can immediately see whether the system is broken AND whether the detector is doing its job.

The key takeaways:

1. **Golden signals beat raw metrics.** A 4-panel LTES row tells you more than a wall of raw graphs.
2. **One composite health score is the headline.** Operators don't watch 8 numbers; they watch one and drill in if it drops.
3. **Empty panels should look healthy, not broken.** Replace Grafana's default "No data" with `✓ All containers running` or similar positive states.
4. **`up == 1` is the only reliable availability signal.** Application-emitted 5xx counters miss connection-refused / pod-down scenarios entirely.
5. **Scrape intervals are tunable.** 5s for live demos / dashboards, 15-30s for production.

The rest of this document is the *how*.

---

## 1. What we're monitoring

### 1.1 The three demo apps

| App | Stack | Role | What's emitted |
|---|---|---|---|
| `payments-api` | Go | OOMKill victim | `payments_api_http_requests_total{method, path, status}` (counter) + `payments_api_http_request_duration_seconds` (histogram) |
| `recommend-svc` | Python (stdlib http.server) | Slow-leak victim | `recommend_svc_http_requests_total{...}` + duration histogram |
| `catalog-svc` | nginx | Healthy control | No /metrics endpoint — availability tracked via `kube_pod_status_ready` |

Each instrumented app exposes `/metrics` on the same port as its HTTP service (`:8080`). The middleware records request count + duration with `(method, path, status)` labels.

### 1.2 The metric sources

Three categories of metrics feed the dashboard:

- **App-level (LTE)**: Throughput, latency, errors per service. Emitted by the apps themselves; scraped by Prometheus via per-app `ServiceMonitor` resources.
- **Kubernetes-level (S + state)**: CPU, memory, pod restart count, container waiting reasons, pod readiness. Emitted by `kube-state-metrics` (running pods/state) and `cAdvisor` (container resource usage). Scraped via the kube-prometheus-stack defaults.
- **AI agent (K8sGPT)**: `k8sgpt_number_of_results{object_namespace}`, `k8sgpt_number_of_results_by_type{kind, name}`, `k8sgpt_number_of_failed_backend_ai_calls{backend}`, `k8sgpt_reconcile_error_count`, plus controller-runtime's `controller_runtime_reconcile_*` and `controller_runtime_active_workers` for the `k8sgpt` controller. Emitted by the K8sGPT operator.
- **Slack webhook (only with Slack on)**: `slack_webhook_up`, from the small slack-probe deployment.

### 1.3 The LTES signal map

For each app the dashboard answers four questions:

| Letter | Question | Metric we use |
|---|---|---|
| **L** atency | How slow are requests? | `histogram_quantile(0.95, sum by(le) (rate(*_http_request_duration_seconds_bucket[1m])))` |
| **T** hroughput | How many requests/sec? | `sum(rate(*_http_requests_total[1m]))` |
| **E** rrors | What fraction are failing? | 5xx rate + `(1 - avg_over_time(up[1m]))` (availability gap) |
| **S** aturation | How close to resource limits? | memory working set as % of the memory limit, per container |

The shipped Saturation panel covers memory only, the resource every demo fault exhausts. §3.5 shows how to fold CPU in.

**Best-practice note**: real SRE practice (per the Google SRE book) is **LTE + S = the golden signals**. Some teams use **RED** (Rate, Errors, Duration) which is the same minus saturation, or **USE** (Utilization, Saturation, Errors) for infrastructure. We use LTES because the demo needs to show both application AND infrastructure faults.

### 1.4 The Composite Health score

Single 0-100 number penalizing the worst signals:

```
100
  - 50  if any pod is in CrashLoopBackOff
  - 20  if any pod's memory utilisation > 50%
  - 30  if any pod's memory utilisation > 80%
  - 30  if payments-api error rate > 1%
  - 30  if recommend-svc error rate > 1%
  - 50  if any of the 3 apps' pod is NotReady
```

Caps at 0. Used as the **headline panel** — operators glance at one number to know if anything needs attention.

**Best-practice note**: composite scores work *if and only if* the penalties are calibrated against your own SLOs. A 5% error rate might be normal for one service and catastrophic for another. Build the score, then dial the thresholds against historical data.

---

## 2. Grafana + Prometheus setup

### 2.1 The stack

We use **kube-prometheus-stack** (the Prometheus Operator chart). It bundles:

- **Prometheus** — the metric store and query engine
- **Grafana** — the dashboard layer
- **Alertmanager** — for alerts (not used in our demo, but provisioned)
- **kube-state-metrics** — exports Kubernetes object state as metrics
- **node-exporter** — host-level CPU/memory/disk metrics
- **Prometheus Operator** — manages ServiceMonitor / PodMonitor / Prometheus CRDs

Installed via Helm, pinned to one chart version (this is what `demo-cluster/scripts/up.sh` runs):

```bash
helm --kube-context kind-demo-cluster upgrade --install prometheus-stack \
  oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack \
  --version 91.9.0 \
  -n monitoring --create-namespace \
  -f demo-cluster/helm/prometheus-stack-values.yaml
```

The release name matters: it becomes the `release: prometheus-stack` label Prometheus selects ServiceMonitors by (§2.3).

### 2.2 Scrape interval tuning

The kube-prometheus-stack default scrape interval is **30 seconds**. For the demo we tuned it to **5 seconds** in [`prometheus-stack-values.yaml`](../demo-cluster/helm/prometheus-stack-values.yaml):

```yaml
prometheus:
  prometheusSpec:
    scrapeInterval: 5s
    evaluationInterval: 5s
    retention: 6h
    resources:
      limits:
        memory: 768Mi
        cpu: 750m
```

**Why 5s for demos**: paired with Grafana's 5s auto-refresh, every dashboard frame is fresh. Faults appear on screen within ~5s of happening. Critical for video pacing.

**Why NOT 5s in production**: 6x more data than 30s scraping. Burns more CPU/disk and stresses kube-state-metrics. **Production recommendation: 15-30s scrape, with 5s reserved for short-lived debugging sessions.**

**Trade-off summary**:

| Scrape interval | Use case | Storage impact | CPU impact |
|---|---|---|---|
| 5s | Live demos, debugging | 6× baseline | ~2× baseline |
| 15s | Active dashboards, dev | 2× baseline | ~1.3× baseline |
| 30s | Production default | 1× baseline | 1× baseline |
| 60s+ | Long-term archival, cost-sensitive | 0.5× | 0.7× |

### 2.3 ServiceMonitor pattern (the right way to scrape apps)

For each instrumented app we created a `ServiceMonitor` resource that tells Prometheus which Service to scrape:

```yaml
# demo-cluster/manifests/payments-api/servicemonitor.yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: payments-api
  namespace: demo-apps
  labels:
    release: prometheus-stack   # <-- CRITICAL, see below
spec:
  namespaceSelector:
    matchNames: [demo-apps]
  selector:
    matchLabels:
      app: payments-api
  endpoints:
    - port: http
      path: /metrics
      interval: 5s
      scrapeTimeout: 3s
```

**Best-practice note — the `release` label gotcha**: kube-prometheus-stack configures Prometheus to *only select ServiceMonitors with `release: prometheus-stack` labels*. We hit this twice during the demo build:

1. Our own ServiceMonitors needed the label added.
2. The **K8sGPT operator's chart-provided ServiceMonitor** was missing it, so Prometheus completely ignored the operator's metrics.

**Diagnostic command** when metrics aren't appearing:

```bash
# Is the target even visible to Prometheus?
kubectl --context kind-demo-cluster -n monitoring \
  port-forward svc/prometheus-stack-kube-prom-prometheus 9090 &
sleep 2
curl -sS http://localhost:9090/api/v1/targets | \
  jq -r '.data.activeTargets[] | "\(.labels.job): \(.health)"'
kill %1   # stop the port-forward
```

If your target isn't listed → ServiceMonitor not discovered. Check:
1. ServiceMonitor labels include `release: prometheus-stack`
2. Service labels match ServiceMonitor `.spec.selector.matchLabels`
3. Service has a **named port** (`name: http`, not just `port: 80`) and the ServiceMonitor's `endpoint.port` references that name
4. Service is in a namespace allowed by `serviceMonitorNamespaceSelector`

### 2.4 App-side instrumentation

Go (using `prometheus/client_golang`):

```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
    httpRequests = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "payments_api_http_requests_total",
            Help: "Total HTTP requests, labeled by method, path, status.",
        },
        []string{"method", "path", "status"},
    )
    httpDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "payments_api_http_request_duration_seconds",
            Help:    "HTTP request duration in seconds.",
            Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
        },
        []string{"method", "path"},
    )
)
http.Handle("/metrics", promhttp.Handler())
```

Python (`prometheus_client`):

```python
from prometheus_client import Counter, Histogram, generate_latest, CONTENT_TYPE_LATEST

REQUESTS = Counter('recommend_svc_http_requests_total', 'HTTP requests', ['method', 'path', 'status'])
DURATION = Histogram('recommend_svc_http_request_duration_seconds', 'HTTP request duration in seconds',
                    ['method', 'path'],
                    buckets=(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10))

# Inside the request handler:
def do_GET(self):
    path = urlparse(self.path).path
    start = time.time()
    status = 200
    try:
        ... handle ...
    except Exception:
        status = 500
        raise
    finally:
        REQUESTS.labels(method="GET", path=path, status=str(status)).inc()
        DURATION.labels(method="GET", path=path).observe(time.time() - start)
```

**Best-practice notes on instrumentation**:

- **Use a consistent metric name prefix per service.** We use `payments_api_*` and `recommend_svc_*` — makes label filtering trivial: `sum(rate(payments_api_*[1m]))`.
- **`status` label cardinality is bounded.** 200, 4xx, 5xx — handful of values. Avoid putting *user IDs* or unbounded data in labels (cardinality explosion → Prometheus OOM).
- **Histogram buckets matter.** Default `prometheus.DefBuckets` is 5ms-10s; if your service is sub-millisecond, you'll have all data in one bucket and quantiles will be useless. We use `0.001-10s` for HTTP services.
- **Always include a per-path label** for HTTP services. Aggregating away from `path` is easy; you can't recover per-path info if it was never recorded. Label the *route*, not the raw URL: payments-api passes a fixed route name per handler, while recommend-svc records the raw path even for 404s, which is only safe because nothing outside the cluster can reach it.

### 2.5 Dashboard provisioning via ConfigMap

Grafana dashboards are versioned in git as JSON ([`grafana/demo-cluster-overview.json`](../demo-cluster/grafana/demo-cluster-overview.json)) and loaded into Grafana via a labeled ConfigMap:

```bash
kubectl --context kind-demo-cluster create configmap demo-cluster-overview-dashboard \
  --from-file=demo-cluster-overview.json=grafana/demo-cluster-overview.json \
  --namespace monitoring --dry-run=client -o yaml | kubectl --context kind-demo-cluster apply -f -

kubectl --context kind-demo-cluster -n monitoring label configmap demo-cluster-overview-dashboard \
  grafana_dashboard=1 --overwrite
```

The kube-prometheus-stack Grafana sidecar watches for ConfigMaps in the `monitoring` namespace labeled `grafana_dashboard=1` and auto-loads them.

**Best-practice note — never edit dashboards in the Grafana UI**: changes won't persist across pod restarts (the sidecar rewrites `/tmp/dashboards/*.json` from the labeled ConfigMaps). The flow is:

1. Make changes in JSON locally (or "export JSON" from the UI)
2. Commit to git
3. Re-apply the ConfigMap (in the demo: re-run `./scripts/up.sh`)
4. Grafana sidecar picks it up

This pattern is **GitOps for dashboards**. Make it the convention from day one or it'll bite you the moment a teammate restarts a Grafana pod.

---

## 3. PromQL patterns (with best-practice annotations)

The queries that drive the dashboard. Each is reusable as-is in your own setup; just swap the metric names.

### 3.1 Rate of a counter

```promql
sum(rate(payments_api_http_requests_total[1m]))
```

- **What it gives you**: requests per second, averaged over the last 1 minute.
- **Why the `[1m]` window**: smaller windows (e.g., `[30s]`) react faster but with more noise. Larger (`[5m]`) smoother but laggy.
- **Pair with scrape interval**: 5s scrape × 1m window = 12 samples per rate calculation — clean and responsive.

### 3.2 Latency percentile from a histogram

```promql
histogram_quantile(0.95, sum by(le) (rate(payments_api_http_request_duration_seconds_bucket[1m]))) * 1000
```

- **`* 1000`** to render in ms (the underlying metric is in seconds).
- **`sum by(le)`** aggregates the bucket counters across all `(method, path)` labels, leaving only the bucket boundaries (`le`).
- **Best-practice note**: histogram quantiles are estimates — accuracy depends on your bucket selection. If you need exact p99, use a *summary* metric instead (but summaries have their own trade-offs).

### 3.3 Service errors with availability fallback

This is the killer query — combines server-emitted 5xx with Prometheus scrape availability:

```promql
((sum(rate(payments_api_http_requests_total{status=~"5.."}[1m]))
  / clamp_min(sum(rate(payments_api_http_requests_total[1m])), 0.001)) or vector(0)) * 100
+ (1 - (avg(avg_over_time(up{job="payments-api"}[1m])) or vector(1))) * 100
```

Parts:

- `5xx_rate / total_rate * 100` — % of completed requests that errored.
- `or vector(0)` — fallback to 0 when no traffic (avoids dividing by zero).
- `1 - avg(avg_over_time(up[1m]))` — fraction of the last minute the target was unreachable.
- `+` — adds them. **Why this matters**: pod-down faults (OOMKill, CrashLoopBackOff) never produce 5xx server-side because the server isn't running. The application's own counter shows 0% errors even during outages. The `up` term catches this.
- `avg()` wrapping `avg_over_time` — collapses labels so the result pairs with the LHS for the `+`. Without it the labels don't match and the addition drops series.

**Best-practice note**: every service-level error rate should pair 5xx with availability. Application metrics alone hide the worst kind of failure.

### 3.4 Gap-rendering during outages

```promql
sum(rate(payments_api_http_requests_total[1m])) and on() (avg(up{job="payments-api"}) == 1)
```

- `and on() (up == 1)` returns LHS only when `up == 1` — when the target is unreachable, the entire expression returns nothing.
- Combined with Grafana's `spanNulls: false`, this **renders the line as a visible gap during outages** instead of a flat 0.
- The flat-0 misrepresents "service is down" as "service is serving 0 RPS". The gap correctly says "we don't know".

In the shipped dashboard, the Throughput and Latency panels append `or on() vector(0)` to this expression, which turns the gap back into a 0 during an outage. Remove that suffix if you want the gap.

### 3.5 Max across multiple resource dimensions

PromQL doesn't have a native "max across vectors" operator. Use `label_replace` + `or` + `max by`:

```promql
max by(pod) (
  label_replace(memory_util_pct, "_kind", "memory", "", "")
  or
  label_replace(cpu_util_pct, "_kind", "cpu", "", "")
)
```

- Both sub-expressions produce per-pod values.
- `label_replace` adds a `_kind` label that differs between them, so `or` keeps both vectors.
- `max by(pod)` aggregates over `_kind`, picking the larger.
- Result: the "worst saturated dimension" per pod.

### 3.6 Conditional series (limit appears only when relevant)

```promql
vector(100) and on() (max(saturation_pct) > 30)
```

- Returns a constant 100 only when the condition is true (any pod's saturation > 30%).
- Returns nothing otherwise.
- Renders an **adaptive limit line** — visible only when saturation is approaching dangerous levels, hidden when idle so the chart auto-scales to actual data.

The shipped Saturation panel draws its `limit` series with a plain `vector(100)`, so the line is always there (see §4.3). Swap in the expression above to make it adaptive.

**Best-practice note — `>` vs `> bool`**:

- `> 30` is a **filter**: returns the value if > 30, NOTHING otherwise.
- `> bool 30` is a **boolean**: returns `1` or `0`, always something.

Use `>` when you want to filter series in/out (e.g., for `and on()`). Use `> bool` when you want a 0/1 multiplier (e.g., for penalty calculations).

### 3.7 Subtractive composite scoring

```promql
100
  - ((CrashLoopBackOff_present > bool 0) * 50)
  - ((max_memory_util > bool 0.5) * 20)
  - ((error_rate > bool 0.01) * 30)
```

- Each penalty fires `* points` when the condition is true.
- Clamp final result with `clamp_min(..., 0)`.
- **Best-practice note**: subtractive scoring is simpler than `min()` across health components. It also lets multiple problems compound (CrashLoopBackOff + memory saturation = lower score than either alone), which matches operator intuition.

### 3.8 "No data" → "0"

```promql
sum(...) or vector(0)
```

- `or vector(0)` returns 0 when the LHS produces no series.
- Critical for stat panels — without it, an empty result renders as "No data" which looks like the panel is broken.

**Best-practice note**: every stat panel showing a count/aggregation should have `or vector(0)` (or `or vector(1)` if the safe default is 1 like with `up`). The exception: time-series panels where empty is meaningful (e.g., the gap-rendering case above).

---

## 4. Dashboard layout decisions

### 4.1 The hierarchy: top-down narrative

```
Topology                  — what is this cluster (text panel)
4 cluster-state stats     — at-a-glance count panel row
Composite Health (large)  — the single headline number
LTES golden signals       — the four primary detection panels
Detail rows               — drill-down panels by category
Agent self-health         — does the detection system work?
```

The order matches **how an operator would investigate an alert**:

1. "Is anything wrong?" → Composite Health
2. "What signal is failing?" → LTES row
3. "Which pod / what reason?" → Detail rows (waiting reasons, restart counts)
4. "Is the detector itself OK?" → Agent self-health

### 4.2 Panel real estate budget

Each panel earns its place:

- **Headline panels (full-width, h=4)**: Composite Health. One number, no drill-down needed.
- **Golden signals row (4× w=6, h=7)**: LTES — the primary detection layer.
- **Detail panels (varies)**: per-category drill-down. Removed redundancy ruthlessly (memory utilization % and per-app memory panels were merged into Saturation + Memory growth rate).

**Best-practice note**: a dashboard with 50 panels is unreadable. Aim for **one screenful** when the dashboard is loaded — viewers shouldn't need to scroll to know if the cluster is healthy. We landed at 19 panels in 52 grid rows, which fits a 1080p browser at 80% zoom.

### 4.3 Adaptive y-axis with conditional limit lines

For memory / saturation panels: we removed fixed `max: 100` from the y-axis so the panel auto-scales to actual data, and added a `vector(100)` series named `limit`, styled as a red dashed line (a Grafana field override on that series name).

In the shipped dashboard that series is the plain `vector(100)`, so the line is always drawn and the y-axis always reaches 100%. With the conditional series from §3.6 instead, the panel behaves like this:

| Cluster state | Y-axis range | Limit line |
|---|---|---|
| Idle (5-15% util) | Auto-fits to ~15% | Not drawn — values clearly visible |
| Climbing fault (30%+) | Auto-stretches | Red dashed 100% appears — dramatic ceiling |
| Near OOM (80%+) | 0-100% | Saturation lines climbing toward visible limit |

**Best-practice note**: fixed 0-100 ranges are great for percentages but terrible for absolute values. For absolute (MiB, count, etc.) always auto-scale. The conditional-limit pattern gives you "auto-scale when idle, full range when stressed" — best of both.

### 4.4 Positive empty states

Grafana's default "No data" message looks like an error. We override it for panels where empty is the goal state:

```json
"fieldConfig": {
  "defaults": {
    "noValue": "✓  All containers running"
  }
}
```

The Active fault states table now shows `✓  All containers running` when healthy, and the actual table of failing pods when something's broken. **Empty becomes a meaningful "everything's fine" signal, not an error.**

### 4.5 Two health scores (not one)

We expose:

- **Cluster Composite Health** — golden signals + waiting states + readiness. "Is anything wrong?"
- **K8sGPT agent self-health** — `failed_backend_ai_calls` rate + `reconcile_error_count` rate. "Is the detector working?"

Both must be ≥ 80 for the system's value to hold. A green cluster with a red agent = blind to faults. A green agent with a red cluster = expected during an incident.

---

## 5. Gotchas we hit

A list of stumbling blocks in the build, with the fixes. If you replicate this setup you'll probably hit at least three.

### 5.1 ServiceMonitor missing `release` label

**Symptom**: Service exposes `/metrics`, target not visible in Prometheus.
**Cause**: Default kube-prometheus-stack Prometheus selects ServiceMonitors with `release: prometheus-stack` label. Missing label = invisible.
**Fix**: Add label, restart Prometheus pod to force re-discovery.

### 5.2 `up` only exists for Prometheus-scraped targets

**Symptom**: `up{job="catalog-svc"}` returns nothing.
**Cause**: catalog-svc has no /metrics endpoint, so no ServiceMonitor, so no scrape target, so no `up` series.
**Fix**: For non-instrumented services use `kube_pod_status_ready{condition="true"}` from kube-state-metrics — always emitted for every pod.

### 5.3 PromQL label-set mismatch on division

**Symptom**: Query like `(metric_a / metric_b) * 100` errors with **"multiple matches for labels: many-to-one matching must be explicit"**.
**Cause**: LHS and RHS have different label sets, and PromQL can't auto-resolve the mapping.
**Fix**: Aggregate both sides first:

```promql
max(
  sum by(namespace, pod, container) (metric_a)
  / on(namespace, pod, container)
  sum by(namespace, pod, container) (metric_b)
)
```

The `sum by(...)` canonicalizes labels so the `on(...)` join becomes unambiguous.

### 5.4 `> bool` vs `>` confusion

**Symptom**: Conditional series renders all the time, even when the condition is "false".
**Cause**: `> bool 30` returns `0` (always exists). For `and on()` filtering you need the value to be *absent* when false, which is what plain `>` does.
**Fix**: Use `> 30` (filter), not `> bool 30` (multiplier).

### 5.5 Stat panel shows "No data" but query returns value

**Symptom**: Direct query against Prometheus returns `100`, panel renders "No data".
**Cause**: Stat panels with complex expressions sometimes fail to reduce a range query result. Default Grafana behavior is `instant: false`.
**Fix**: Set `instant: true` and `range: false` on the target:

```json
"targets": [
  {
    "expr": "...",
    "instant": true,
    "range": false
  }
]
```

### 5.6 Counter metric flapping during pod restarts

**Symptom**: `rate(restarts_total[1m])` returns 0 even though kubectl shows restart count climbing.
**Cause**: Counter resets to 0 each time the pod is recreated. `rate()` handles single resets but a sequence of resets within the window confuses it.
**Fix**: Use `increase()` over a longer window, OR use the absolute counter value over time rather than rate.

### 5.7 kube-prometheus-stack default scrape too slow for dashboards

**Symptom**: Grafana refresh is 5s but charts update every 30s.
**Cause**: Prometheus scrape is 30s by default — the data isn't updated more often than that.
**Fix**: Lower scrape interval in helm values (see §2.2).

### 5.8 Rolling update protects fault scenarios

**Symptom**: Trigger OOMKill via deployment env var change, but the existing pod stays serving traffic and only a new (failing) pod appears.
**Cause**: With `replicas=1` and default RollingUpdate strategy, Kubernetes creates the new pod first and waits for Ready before terminating the old.
**Fix**: For demo purposes, trigger OOMKill on the *running* pod (we hit `/allocate` directly). Don't change the deployment spec.

---

## 6. What we'd change for production

The demo cluster takes shortcuts a real production setup wouldn't. Specifically:

| Demo | Production |
|---|---|
| 5s scrape interval | 15-30s (cost) |
| 6h retention | 15d (Prometheus) → 1y (Thanos / Mimir / remote-write to Grafana Cloud) |
| Local kube-prometheus-stack | Grafana Cloud Metrics or self-hosted Mimir |
| Chart's default alert rules, no Alertmanager receivers | Rules tuned to your SLOs, with PagerDuty / OpsGenie routing |
| Single replica per app | HPA + multiple replicas, PodDisruptionBudgets |
| Slack webhook for K8sGPT | Alertmanager → routing tree → ticketing system |
| Composite Health for human eyes | + alert rule that pages when score < 70 for > 5m |
| No multi-tenant labels | `cluster`, `team`, `service` labels on everything |
| Saturation thresholds at 50/80% | Per-service thresholds tied to SLO budget |

The dashboard JSON, queries, and patterns transfer 1:1. The infrastructure layer is what changes.

---

## 7. References & further reading

- Google SRE book, [Chapter 6 — Monitoring Distributed Systems](https://sre.google/sre-book/monitoring-distributed-systems/) — the golden signals framework.
- [Prometheus Best Practices](https://prometheus.io/docs/practices/) — official metric naming, labeling, alerting.
- [kube-prometheus-stack docs](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack) — the helm chart we use.
- [Grafana panel docs](https://grafana.com/docs/grafana/latest/panels-visualizations/) — every panel type's capabilities.
- [PromQL operators reference](https://prometheus.io/docs/prometheus/latest/querying/operators/) — including the `and`, `or`, `bool` semantics that catch out most people.

---

## 8. Source files in this repo

| File | What's in it |
|---|---|
| [`demo-cluster/grafana/demo-cluster-overview.json`](../demo-cluster/grafana/demo-cluster-overview.json) | The full dashboard JSON — 19 panels, 3,084 lines. Use as a template. |
| [`demo-cluster/helm/prometheus-stack-values.yaml`](../demo-cluster/helm/prometheus-stack-values.yaml) | Prometheus + Alertmanager + Grafana tuning. |
| [`demo-cluster/helm/k8sgpt-values.yaml`](../demo-cluster/helm/k8sgpt-values.yaml) | K8sGPT operator config — `serviceMonitor.additionalLabels` includes the `release` fix. |
| [`demo-cluster/manifests/payments-api/`](../demo-cluster/manifests/payments-api/) | Go app + Dockerfile + Deployment + ServiceMonitor — the LTE-instrumented service template. |
| [`demo-cluster/manifests/recommend-svc/`](../demo-cluster/manifests/recommend-svc/) | Python equivalent. |
| [`demo-cluster/scripts/trigger-oomkill.sh`](../demo-cluster/scripts/trigger-oomkill.sh), [`trigger-slowleak.sh`](../demo-cluster/scripts/trigger-slowleak.sh) | The two faults the dashboard is built to show. |

---

*This document is a living artifact. As we learn more from running the demo, expect updates with new gotchas, query patterns, and best-practice refinements. If you spot a mistake or have a better pattern, open an issue.*
