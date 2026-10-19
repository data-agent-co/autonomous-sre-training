# demo-cluster

The workshop cluster: a local kind cluster with three sample apps, a load
generator, Prometheus and Grafana, the K8sGPT operator and the watcher. You
trigger real faults and compare what the dashboard, K8sGPT and the watcher
each see.

Prerequisites and a short quickstart are in the
[top-level README](../README.md#prerequisites). This page is the full guide.
Run every command from this directory (`demo-cluster/`).

## What runs in the cluster

```
Your laptop (Docker)
┌──────────────────────────────────────────────────────────────────────────────┐
│ kind cluster "demo-cluster" (one control-plane node, Kubernetes v1.37)       │
│                                                                              │
│ namespace demo-apps                                                          │
│   payments-api     Go       limit 128Mi   OOMKill victim                     │
│   recommend-svc    Python   limit 128Mi   slow-leak victim, with the         │
│   leak-fixture     sidecar  limit 128Mi   sidecar that leaks on demand       │
│   catalog-svc      nginx    limit 64Mi    healthy control, no traffic        │
│   loadgen          curl     limit 32Mi    ~2 req/s each to the first two     │
│         │                                                                    │
│         │ pod status, events, ConfigMaps                                     │
│         ▼                                                                    │
│ namespace k8sgpt-system                                                      │
│   K8sGPT operator + k8sgpt-sample   scans demo-apps Pods about every 30s     │
│   k8s-watcher (CM lane)             watches labelled ConfigMaps              │
│   Results (results.core.k8sgpt.ai)  written by both                          │
│   slack-relay, slack-probe          only with SLACK_WEBHOOK_URL              │
│                                     (relay posts K8sGPT, watcher and         │
│                                     Alertmanager findings)                   │
│                                                                              │
│ namespace monitoring                                                         │
│   kube-prometheus-stack: Prometheus (5s scrape, 6h retention),               │
│   Grafana, Alertmanager, kube-state-metrics, node-exporter                   │
│                                                                              │
│ namespace kube-system                                                        │
│   metrics-server (kubectl top, leak-progress.sh)                             │
└──────────────────────────────────────────────────────────────────────────────┘
      │ kubectl port-forward     │ HTTPS, optional            │ HTTPS, optional
      ▼                          ▼                            ▼
  localhost:3000             LLM API (OpenAI-compatible,    Slack incoming
  Grafana                    or a model on your laptop)     webhook
```

- **payments-api** (Go) serves `/healthz`, `/metrics` and `/allocate`, which
  holds on to memory. `trigger-oomkill.sh` uses it to push the container past
  its limit.
- **recommend-svc** (Python) is a plain HTTP service with metrics. Its pod also
  runs a `leak-fixture` sidecar that is idle until `trigger-leak.sh` turns
  the leak on, so the app itself has no fault code.
- **catalog-svc** (nginx) gets no traffic and no faults: the healthy baseline.
- **loadgen** calls `/healthz` on payments-api and recommend-svc so their
  golden-signal panels have data.
- **K8sGPT** runs its Pod analyzer on the `demo-apps` namespace only and writes
  one Result per problem it finds. It deletes a Result once the problem is
  gone. With an LLM it adds an explanation; with Slack it posts each new Result.
- **The watcher** (`k8s-watcher`, built from [`../k8s-watcher`](../k8s-watcher)
  and installed from [`../charts/k8s-watcher`](../charts/k8s-watcher)) writes a Result
  when a ConfigMap labelled `k8sgpt-detection-pack.io/critical=true` changes,
  in any namespace. Its EV lane (Kubernetes Warning events) is off in this
  demo. [`../k8s-watcher/README.md`](../k8s-watcher/README.md) has the details.

The data path during an OOMKill:

```
trigger-oomkill.sh (background loop)
    │  kubectl exec: wget /allocate?mb=200, each time the container is running
    ▼
payments-api ── allocates past its 128Mi limit ──► the kernel OOMKills it
    │  the kubelet restarts it in place; after a few restarts: CrashLoopBackOff
    ├──────────────────────────────────────────────┐
    ▼                                              ▼
K8sGPT Pod analyzer (next scan,                Prometheus alert DemoContainerOOMKilled
after CrashLoopBackOff) ──► Result             (seconds after the first OOMKill)
    │  with an LLM: an explanation in              │
    │  spec.details                                ▼
    │                                          Alertmanager
    ▼                                              │
with Slack: slack-relay ◄──────────────────────────┘
    │  K8sGPT repeats are dropped until the object is quiet for 10 minutes
    ▼
your channel
```

## Setup

```bash
cp .env.example .env    # optional
./scripts/up.sh         # or `make up` from the repository root
```

### Settings

`.env` is optional and gitignored. Without it, `up.sh` builds the whole demo:
K8sGPT writes Results without AI explanations and nothing goes to Slack.

| `.env` key | Default | Meaning |
|---|---|---|
| `LLM_API_KEY` | empty | Empty: no AI explanations. Set it to turn them on. |
| `K8SGPT_BACKEND` | `openai` | K8sGPT AI backend. `openai` also covers every OpenAI-compatible API. |
| `K8SGPT_MODEL` | `gpt-4o-mini` (`gpt-oss:20b` with `K8SGPT_LOCAL=true`) | Model name. |
| `K8SGPT_BASE_URL` | empty: the backend's own endpoint (`http://host.docker.internal:11434/v1` with `K8SGPT_LOCAL=true`) | API endpoint. |
| `K8SGPT_LOCAL` | `false` | `true`: use a model served on your machine. No key needed (`LLM_API_KEY` is ignored); the backend is always `openai`, and `K8SGPT_BASE_URL` must be a local or private address. |
| `SLACK_WEBHOOK_URL` | empty | Empty: no Slack. |

These are read from your shell, not from `.env`:

| Variable | Default | Meaning |
|---|---|---|
| `CLUSTER_NAME` | `demo-cluster` | The cluster every script acts on, through the context `<provider>-<name>`. |
| `CLUSTER_PROVIDER` | `kind` | `kind` or `k3d`. See [Using k3d instead of kind](#using-k3d-instead-of-kind). |
| `GRAFANA_PORT` | `3000` | Local port for the Grafana port-forward. |
| `ENV_FILE` | `demo-cluster/.env` | Settings file. A missing default file is fine; a missing file you named is an error. A relative path is relative to your current directory. |
| `KIND_NODE_IMAGE` | the pinned `kindest/node:v1.37.0` | Node image for a new cluster. Set it empty to use your kind version's default. |

To run a second demo cluster next to the first, give it its own name and
Grafana port, and use the same prefix for every script you run against it:

```bash
CLUSTER_NAME=demo-2 GRAFANA_PORT=3001 ./scripts/up.sh
CLUSTER_NAME=demo-2 ./scripts/trigger-oomkill.sh
```

### Using k3d instead of kind

With `CLUSTER_PROVIDER=k3d`, the scripts act on the
[k3d](https://k3d.io) cluster `$CLUSTER_NAME` through the context
`k3d-$CLUSTER_NAME`, with the same safety check as for kind. You need k3d
instead of kind; tested with k3d v5.9.0 (k3s v1.35.5).

- `up.sh` reuses the cluster if it exists, or creates it with one server and
  two agents. `KIND_NODE_IMAGE` is ignored.
- Local images are loaded with `k3d image import`.
- k3s ships its own metrics-server, so `up.sh` waits for it instead of
  installing the upstream one.
- `down.sh` and `reset.sh` delete the cluster with `k3d cluster delete`.

Use the same prefix for every script, for example with an existing k3d
cluster `dev`:

```bash
CLUSTER_PROVIDER=k3d CLUSTER_NAME=dev ./scripts/up.sh
CLUSTER_PROVIDER=k3d CLUSTER_NAME=dev ./scripts/trigger-oomkill.sh
kubectl --context k3d-dev -n k8sgpt-system get results
```

### What up.sh does

It runs these steps in order, each with its own log file:

1. **kind cluster**: creates `demo-cluster` (or reuses it).
2. **namespaces**: `demo-apps`, `monitoring`, `k8sgpt-system`.
3. **metrics-server**, for `kubectl top`.
4. **build + load sample images**: `payments-api:demo` and `recommend-svc:demo`,
   built locally and loaded into the kind node.
5. **deploy sample apps + loadgen**.
6. **kube-prometheus-stack** (Prometheus, Grafana, Alertmanager,
   kube-state-metrics, node-exporter).
7. **ServiceMonitors** for payments-api and recommend-svc.
8. **Grafana dashboard**, as a ConfigMap the Grafana sidecar loads.
9. **K8sGPT operator**.
10. **Slack relay + probe**, or with Slack off, removal of what an earlier run
    with Slack left behind.
11. **K8sGPT CR + LLM secret**: renders
    [`manifests/k8sgpt/k8sgpt-cr.yaml`](manifests/k8sgpt/k8sgpt-cr.yaml) from
    your settings, applies it, and restarts the operator.
12. **watcher**: builds `k8s-watcher:dev` from `../k8s-watcher`, loads it
    into the kind node and installs the `k8s-watcher` release in
    `k8sgpt-system` with [`helm/watcher-values.yaml`](helm/watcher-values.yaml).
13. **pod smoke-test**: fails if a pod in the three namespaces is stuck
    (ImagePullBackOff, CrashLoopBackOff, long Pending).
14. **Grafana port-forward**: optional. If it fails, `up.sh` prints `WARN` and
    finishes anyway.

When a required step fails, `up.sh` prints `FAIL`, the log path and the last
20 lines of the log, and stops. Logs go to
`.run/<cluster>/up-<timestamp>/NN-<step>.log`, next to the rendered
`k8sgpt-cr.yaml`. Fix the cause and run `./scripts/up.sh` again; it is
idempotent.

At the end, `up.sh` prints whether AI explanations and Slack are on, and the
commands to verify, trigger faults, read Results, open Grafana and tear down.

### Your kubectl context

Every script acts only on the kind cluster `$CLUSTER_NAME`: kubectl and helm
calls name the context `kind-$CLUSTER_NAME`, and before changing anything the
scripts check that the kind cluster exists and that this context points at
it. Your current context is never used. One thing comes from kind itself:
`kind create cluster` (the first `up.sh` run) switches your current context to
`kind-demo-cluster`.

The commands on this page name the context too. For a shorter version:

```bash
alias kd='kubectl --context kind-demo-cluster'
```

## Scenarios

### 1. OOMKill (payments-api)

```bash
./scripts/trigger-oomkill.sh
```

A background loop calls `/allocate?mb=200` inside the running payments-api
container (limit 128Mi) through `kubectl exec`, each time the container is
running. The kernel OOMKills it, the kubelet restarts it in place, and after a
few restarts the pod is in CrashLoopBackOff.

- **Dashboard:** Restart rate per pod climbs, Service errors % spikes during
  each restart window, OOMKills (last 5m) counts up, and the Composite Health
  Score drops.
- **Alertmanager:** `DemoContainerOOMKilled` fires seconds after the first
  OOMKill; with Slack it is the first post.
- **K8sGPT:** within a minute or two, a Result for the payments-api pod:

  ```bash
  kubectl --context kind-demo-cluster -n k8sgpt-system get results
  kubectl --context kind-demo-cluster -n k8sgpt-system get results \
    -o custom-columns='NAME:.metadata.name,KIND:.spec.kind,OBJECT:.spec.name,ERROR:.spec.error[0].text'
  ```

  With an LLM, the explanation is in `spec.details`:

  ```bash
  kubectl --context kind-demo-cluster -n k8sgpt-system get results \
    -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.details}{"\n"}{end}'
  ```

Stop it:

```bash
./scripts/trigger-oomkill.sh --stop
```

payments-api recovers at its next restart, which can take a few minutes while
the kubelet's back-off runs out. `./scripts/soft-reset.sh` replaces the pod
right away. Knobs (shell environment): `OOM_ALLOC_MB=200` (MiB per call),
`OOM_INTERVAL=1` (seconds between checks).

### 2. Slow memory leak (recommend-svc)

```bash
./scripts/trigger-leak.sh                # 280 MB/min, OOM in ~30 s
./scripts/trigger-slowleak.sh            # or 20 MB/min, OOM in ~6 min
./scripts/leak-progress.sh               # optional: live memory bar, Ctrl-C to quit
```

The `leak-fixture` sidecar starts allocating memory at 280 MB/min against its
own 128Mi limit, from a baseline of about 15 MiB:

| Time after the trigger | Memory | Share of the limit |
|---|---|---|
| +10 s | ~45 MiB | ~35% |
| +20 s | ~90 MiB | ~70% |
| ~+28 s | OOMKilled | |

- **Dashboard:** Saturation and Memory growth rate show the leak before it
  breaks anything; the Composite Health Score drops at 50% and again at 80%
  of the limit.
- **K8sGPT:** stays silent while the memory climbs, because nothing has failed
  yet. This is the point of the scenario: a leak is visible on the dashboard
  before any analyzer has a failure to report. After the OOMKill,
  K8sGPT reports the pod only once it is in CrashLoopBackOff, a few restarts
  later.
- **Alertmanager:** `DemoContainerOOMKilled` fires seconds after the first
  OOMKill, minutes before K8sGPT's Result; with Slack it is posted right away.

The rate is stored on the Deployment, so after each OOMKill the sidecar
restarts and leaks again. `trigger-slowleak.sh` reaches the limit in about 6
minutes, which leaves time to watch the climb; `--rate N` sets another rate
on either script. Stop it (either script):

```bash
./scripts/trigger-leak.sh --off          # the pod restarts at baseline
```

### 3. ConfigMap change (watcher)

The watcher reports changes to ConfigMaps that opt in with the label
`k8sgpt-detection-pack.io/critical=true`. Create one with the label already
set, then change it:

```bash
kubectl --context kind-demo-cluster -n demo-apps apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: payments-config
  labels:
    k8sgpt-detection-pack.io/critical: "true"
data:
  timeout: "5s"
EOF

kubectl --context kind-demo-cluster -n demo-apps patch configmap payments-config \
  --type merge -p '{"data":{"timeout":"30s"}}'

kubectl --context kind-demo-cluster -n k8sgpt-system get results \
  -l k8sgpt-detection-pack.io/component=cm \
  -o custom-columns='NAME:.metadata.name,OBJECT:.spec.name,ERROR:.spec.error[0].text'
```

The watcher keeps one Result per ConfigMap and updates it on each change. Its
error text counts the keys, e.g. `ConfigMap demo-apps/payments-config
modified: 0 added, 0 removed, 1 changed`, and `spec.details` lists the keys
with digests of their values, never the values. Deleting the ConfigMap
updates the Result to `deleted`. With Slack, slack-relay posts each new
Result and each change to one within about 10 seconds.

What the watcher does not report:

- adding the label to an existing ConfigMap (opting in is not a change);
- removing the label (that is how you opt out);
- ConfigMaps that existed before the watcher started, until they change.

The watcher's chart has a helm test that runs the same check from a pod:

```bash
helm --kube-context kind-demo-cluster -n k8sgpt-system test k8s-watcher --logs
```

`--logs` needs Helm 4; with Helm 3 it fails on the chart's non-Pod test
resources, so leave it out.

It works in the namespace `k8s-watcher-test` and removes everything
it created when it passes. A failed check prints that namespace's events to
its log, and its pod stays until the next run, so
`kubectl --context kind-demo-cluster -n k8sgpt-system logs k8s-watcher-test-cm`
still shows them. Helm may remove the namespace itself.

Clean up the example with
`kubectl --context kind-demo-cluster -n demo-apps delete configmap payments-config`.
Unlike K8sGPT, the watcher never deletes its Results; `soft-reset.sh` does.

## Grafana dashboard

`up.sh` port-forwards Grafana in the background. Open
<http://localhost:3000/d/demo-cluster-overview> (admin / admin; anonymous users
can view). The port-forward dies with its pod or cluster (a reset, a Docker
restart); bring it back with `./scripts/grafana.sh`, stop it with
`./scripts/grafana.sh --stop`.

The dashboard has 19 panels, refreshes every 5 seconds and shows the last 2
minutes. From top to bottom:

1. **Topology & services**: what runs where, and how to trigger each fault.
2. **Demo apps healthy · CrashLoopBackOff · OOMKills (last 5m) · Restarts
   (last 5m)**.
3. **Composite Health Score**: one 0–100 number. It drops by 50 for any
   CrashLoopBackOff, 20 and 30 for memory above 50% and 80% of a limit, 30 for
   each app's 5xx rate above 1%, and 50 for any app pod not Ready.
4. **Throughput · Latency p95 · Service errors % · Saturation**: the golden
   signals per app.
5. **Restart rate per pod · Container restart events / min**.
6. **Container restarts over time · Active fault states** (containers waiting
   in CrashLoopBackOff, ImagePullBackOff and the like) **· K8sGPT detection
   events**.
7. **Memory growth rate**: the slope of each container's memory, the clearest
   signal of a slow leak.
8. **K8sGPT diagnoses by Kind · LLM calls (errors + latency) · K8sGPT agent
   health**: whether the detector itself works.

The source is [`grafana/demo-cluster-overview.json`](grafana/demo-cluster-overview.json).
`up.sh` loads it as the ConfigMap `demo-cluster-overview-dashboard` (label
`grafana_dashboard=1`) in `monitoring`. Changes made in the Grafana UI are
lost when Grafana restarts: edit the JSON and re-run `./scripts/up.sh`.

How the dashboard was built, and the PromQL behind it:
[../docs/sre-dashboard-best-practices.md](../docs/sre-dashboard-best-practices.md).

## Slack (optional)

1. Use a Slack workspace and channel you are allowed to post cluster findings
   to. A separate test workspace keeps the demo out of real channels.
2. Create an app at <https://api.slack.com/apps> (**Create New App** > **From
   scratch**), open **Incoming Webhooks**, turn them on, click **Add New
   Webhook to Workspace** and pick the channel. Copy the webhook URL.
3. Put it in `.env` as `SLACK_WEBHOOK_URL`.
4. `./scripts/check-slack.sh` posts a test message; it should print `OK`.
5. `./scripts/up.sh` turns Slack on in the cluster.

With Slack on, `up.sh` also deploys:

- **slack-relay**: the one path to the channel.
  - The K8sGPT operator posts through it. The operator repeats posts for the
    same problem; the relay forwards the first post per object and drops
    repeats until that object has been quiet for 10 minutes, so a fault that
    stays live is posted once.
  - It polls the watcher's Results and posts each new one and each change.
  - Alertmanager sends it `DemoContainerOOMKilled`
    ([`helm/alertmanager-slack-values.yaml`](helm/alertmanager-slack-values.yaml),
    which `up.sh` adds only with Slack on). Every other alert goes nowhere.
- **slack-probe**: checks every 30 seconds that the webhook still answers
  (without posting) and exports `slack_webhook_up` to Prometheus.

To turn Slack off, empty `SLACK_WEBHOOK_URL` and re-run `./scripts/up.sh`. It
removes the relay, the probe, their secrets, the K8sGPT Slack sink and the
Alertmanager route.

## LLM (optional)

Set `LLM_API_KEY` (and the model and endpoint, if not OpenAI's) in `.env` and
re-run `./scripts/up.sh`. The options and what the LLM provider receives are
described in the [top-level README](../README.md#llm-optional). Guides:

- Groq free tier: [../docs/groq-free-tier-setup.md](../docs/groq-free-tier-setup.md)
- LM Studio on your laptop: [docs/lm-studio.md](docs/lm-studio.md)

`up.sh` restarts the K8sGPT operator on every run. That matters: after several
failed LLM calls in a row (a bad key, a retired model) the operator stops
asking for explanations until it restarts.

K8sGPT itself (the `k8sgpt-sample` pod) reads the key only when it starts.
`up.sh` replaces the pod when the key, the model or the endpoint changes.

### Local LLM with Ollama

A model on your laptop keeps cluster data on your machine and costs nothing
per call, but it is slower than a hosted model and needs memory: plan for a
32 GB laptop with the default model, `gpt-oss:20b`.

```bash
brew install ollama                        # or see https://ollama.com/download
OLLAMA_HOST=0.0.0.0:11434 ollama serve     # keep this running in its own terminal
ollama pull gpt-oss:20b                    # in another terminal; a one-time download
```

`OLLAMA_HOST=0.0.0.0` makes Ollama listen on your network, so the kind node can
reach it through `host.docker.internal`. Stop it when you are done, or use a
firewall, if you are on a shared network.

Then set `K8SGPT_LOCAL=true` in `.env` and re-run `./scripts/up.sh`. No key is
needed. For another model or port, also set `K8SGPT_MODEL` and
`K8SGPT_BASE_URL`.

On Linux without Docker Desktop, `host.docker.internal` may not resolve. Use
the Docker bridge address instead:
`K8SGPT_BASE_URL=http://172.17.0.1:11434/v1`.

## Reset and teardown

| Command | What it does |
|---|---|
| `./scripts/soft-reset.sh` | Keeps the cluster. Stops the slow leak and the OOMKill loop, replaces the payments-api and recommend-svc pods, restarts Prometheus (wipes its data, so graphs start flat), restarts slack-relay (if Slack is on) and the watcher, then deletes all Results and leftover helm-test pods. |
| `./scripts/reset.sh` | `down.sh`, then `up.sh`. Slower, but starts from scratch. |
| `./scripts/down.sh` | Stops the Grafana port-forward and the OOMKill loop, then deletes the kind cluster. |

`down.sh` deletes the cluster through `kind delete cluster --name
$CLUSTER_NAME`, so it cannot touch another cluster. kind also removes the
`kind-demo-cluster` context; switch back to your usual one afterwards.

## Scripts

All scripts are in [`scripts/`](scripts) and work from any directory.

| Script | What it does |
|---|---|
| `up.sh` | Creates or updates the whole demo. Idempotent. A re-run stops the OOMKill loop and replaces any crash-looping app pod. |
| `down.sh` | Stops the background helpers, deletes the kind cluster. |
| `reset.sh` | `down.sh` + `up.sh`. |
| `soft-reset.sh` | Returns the cluster to its just-installed state (see above). |
| `grafana.sh [--stop]` | Starts or stops the Grafana port-forward. Refuses a port that is already in use. |
| `trigger-oomkill.sh [--stop]` | Starts or stops the OOMKill loop on payments-api. |
| `trigger-leak.sh [--rate N \| --off]` | Starts the leak at N MB/min (default 280, at least 1), or stops it. |
| `trigger-slowleak.sh [--rate N \| --off]` | Starts the leak at N MB/min (default 20, at least 1), or stops it. |
| `leak-progress.sh` | Live memory bar for the leak-fixture container. Needs metrics-server. Knobs: `NS`, `LABEL`, `CONTAINER`, `LIMIT_MI`, `INTERVAL_S`. |
| `check-pods.sh <namespace> [selector] [wait-seconds]` | Fails if pods are stuck in ImagePullBackOff, CrashLoopBackOff or Pending. |
| `check-slack.sh` | Posts a test message to `SLACK_WEBHOOK_URL`. |
| `lib.sh`, `k8sgpt-lib.sh` | Helpers the other scripts source. |

The Grafana port-forward and the OOMKill loop run in the background. Their PID
files and logs are in `.run/<cluster>/` (gitignored). A script stops a process
only if it is still the one it started.

## Pinned versions

| Component | Version |
|---|---|
| kind node image | `kindest/node:v1.37.0`, pinned by digest in `up.sh` (needs kind v0.33.0+) |
| metrics-server | v0.9.0 |
| kube-prometheus-stack chart | 91.9.0 (`oci://ghcr.io/prometheus-community/charts`, needs Helm 3.8+) |
| k8sgpt-operator chart | 0.2.29 (`https://charts.k8sgpt.ai`) |
| K8sGPT | `ghcr.io/k8sgpt-ai/k8sgpt:v0.4.39` |
| payments-api image | built from `golang:1.27.1-alpine3.24`, runs on `alpine:3.24.2` |
| recommend-svc, slack-relay, slack-probe | `python:3.14.8-alpine3.24`; recommend-svc and slack-probe add `prometheus_client` 0.26.0 |
| catalog-svc | `nginx:1.30.5-alpine3.24` |
| loadgen | `curlimages/curl:8.22.0` |
| watcher image | built from `golang:1.27.1`, runs on `gcr.io/distroless/static-debian13:nonroot` (both pinned by digest) |
| watcher helm test | `docker.io/alpine/kubectl:1.37.1` (pinned by digest) |

The chart and image versions are set at the top of
[`scripts/up.sh`](scripts/up.sh) and in the manifests.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `up.sh` stops with `FAIL` | A step failed | Read the log it names (`.run/<cluster>/up-<timestamp>/`), fix the cause, re-run `./scripts/up.sh`. |
| `soft-reset.sh` ends with `done with errors` | One or more steps failed | It lists each failed step with its log (`.run/<cluster>/soft-reset-<timestamp>/`) and last lines. Fix the cause and re-run it, or use `./scripts/reset.sh`. |
| `missing tools: ...` or `Docker is not running.` | A prerequisite is missing | Install it or start Docker. |
| `kind create cluster` fails or the node never gets Ready | Too little memory for Docker, or kind is older than v0.33.0 | Give Docker at least 6 GB. Upgrade kind, or run `KIND_NODE_IMAGE= ./scripts/up.sh`. |
| `kind cluster 'demo-cluster' not found` | The cluster was deleted | `./scripts/up.sh` |
| `context 'kind-demo-cluster' points at ...` or `your kubeconfig has no 'kind-demo-cluster' context` | Your kubeconfig lost or changed the kind entry | `kind export kubeconfig --name demo-cluster` |
| ImagePullBackOff on payments-api, recommend-svc or k8s-watcher | The image was not loaded into the kind node | `./scripts/up.sh` |
| `port 3000 is already in use` | Something else listens on 3000 | `GRAFANA_PORT=3001 ./scripts/grafana.sh` |
| Grafana stopped answering | The port-forward died with its pod, the cluster or Docker | `./scripts/grafana.sh` |
| No Result a few minutes after `trigger-oomkill.sh` | K8sGPT has not caught the pod failing yet, or the operator has an error | Check `kubectl --context kind-demo-cluster -n demo-apps get pods` and the operator log: `kubectl --context kind-demo-cluster -n k8sgpt-system logs deploy/k8sgpt-operator-controller-manager` |
| Results have no AI text | No `LLM_API_KEY`, a wrong key or model, or the operator stopped calling the LLM after failures | Check `.env` and re-run `./scripts/up.sh`. The `up.sh` header says whether AI explanations are on. |
| AI text is identical to an earlier answer | K8sGPT cached it | `kubectl --context kind-demo-cluster -n k8sgpt-system patch k8sgpt k8sgpt-sample --type merge -p '{"spec":{"noCache":true}}'`. `up.sh` sets it back. |
| `check-slack.sh` prints `FAIL` | The webhook URL is wrong or was revoked | Create a new webhook and update `.env`. |
| No Slack post, but `check-slack.sh` works | Slack was off when `up.sh` ran, or the relay dropped a repeat | Re-run `./scripts/up.sh` (it also restarts the relay when the webhook changed). Check `kubectl --context kind-demo-cluster -n k8sgpt-system logs deploy/slack-relay`. |
| `leak-progress.sh` keeps waiting for metrics-server | metrics-server has no data yet | Wait a minute; check `kubectl --context kind-demo-cluster -n demo-apps top pods`. |

## Layout

```
demo-cluster/
├── .env.example          optional settings (LLM, Slack)
├── kind-config.yaml      the kind cluster: one control-plane node
├── scripts/              see "Scripts" above
├── manifests/
│   ├── payments-api/     OOMKill victim (Go): source, Dockerfile, Deployment, ServiceMonitor
│   ├── recommend-svc/    slow-leak victim (Python) and its leak-fixture sidecar
│   ├── catalog-svc/      healthy control (nginx)
│   ├── loadgen/          traffic generator (curl)
│   ├── k8sgpt/           the K8sGPT resource (a template up.sh renders)
│   ├── slack-relay/      posts K8sGPT, watcher and Alertmanager findings to Slack
│   └── slack-probe/      checks that the Slack webhook answers
├── helm/                 values for kube-prometheus-stack, the K8sGPT operator, the watcher
├── grafana/              the dashboard JSON
└── docs/                 running K8sGPT with LM Studio
```
