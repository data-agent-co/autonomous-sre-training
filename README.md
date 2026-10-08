# autonomous-sre-training

Code for a hands-on autonomous SRE workshop. One script builds a local
[kind](https://kind.sigs.k8s.io) cluster with three small sample apps,
Prometheus and Grafana, the [K8sGPT](https://k8sgpt.ai) operator, and a small
watcher that writes K8sGPT Results. You then break an app on purpose (an
OOMKill, a slow memory leak, a ConfigMap change) and compare what each layer
catches: the golden-signal dashboard, K8sGPT's analysis (optionally explained
by an LLM and posted to Slack), and the watcher.

The cluster runs on your laptop. An LLM and Slack are optional.

## Prerequisites

| Tool | Minimum | Tested with |
|---|---|---|
| Docker (Desktop or Engine), running | 6 GB of memory for Docker | Docker Engine 29.8.1 (Docker Desktop) |
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | v0.33.0 (for the pinned node image) | v0.33.0 |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | v1.36 (the cluster runs v1.37) | v1.37.1 |
| [Helm](https://helm.sh/docs/intro/install/) | v3.8 (OCI charts); v4 for `helm test --logs` | v4.3.0 |
| curl, bash | bash 3.2 (the macOS default) or later | bash 3.2.57 |

`up.sh` checks for these tools and for a running Docker before it starts.
To run the demo on a [k3d](https://k3d.io) cluster instead of kind, see
[Using k3d instead of kind](demo-cluster/README.md#using-k3d-instead-of-kind).
You don't need Python or Go on your machine: the apps and the watcher are
built and run in Docker. Go is only for working on the watcher (see
[Repo layout](#repo-layout)).

Optional: a Slack incoming webhook ([Slack](#slack-optional)) and an LLM,
either a cloud API key or a model on your laptop ([LLM](#llm-optional)).
Without them K8sGPT still writes Results, with no AI explanation, and
nothing goes to Slack.

- **Laptop:** 16 GB of RAM. The demo uses about 3.5 GB. Running the LLM on
  your laptop instead (Ollama's default model) needs 32 GB.
- **OS:** macOS or Linux. On Windows, use WSL2 and clone the repo inside WSL
  so the scripts keep their LF line endings.
- **Network:** the first run downloads charts, images and Go and Python
  packages (Docker Hub, quay.io, ghcr.io, gcr.io, registry.k8s.io, GitHub,
  proxy.golang.org, PyPI).

## Quickstart

```bash
git clone https://github.com/data-agent-co/autonomous-sre-training.git
cd autonomous-sre-training/demo-cluster

cp .env.example .env    # optional: LLM key, Slack webhook (see below)
./scripts/up.sh
```

From the repository root, `make up` does the same (`make help` lists every
target).

The first run takes about 5–10 minutes (image pulls and builds). `up.sh` is
idempotent: run it again after changing `.env`, or when a step failed.

The scripts only ever act on the kind cluster `demo-cluster`, through the
context `kind-demo-cluster`, and check that this context points at that kind
cluster before they change anything. One side effect comes from kind itself:
creating the cluster switches your current kubectl context to
`kind-demo-cluster`. The commands below name the context explicitly anyway.

**Trigger an OOMKill** in the `payments-api` app:

```bash
./scripts/trigger-oomkill.sh
```

**See what K8sGPT found.** It scans about every 30 seconds; the crash-looping
`payments-api` pod shows up within a minute or two:

```bash
kubectl --context kind-demo-cluster -n k8sgpt-system get results
kubectl --context kind-demo-cluster -n k8sgpt-system get results -o yaml
```

Without an LLM, each Result names the problem (`spec.error`); with one,
`spec.details` also holds the LLM's explanation.

**Open Grafana.** `up.sh` port-forwards it in the background:
<http://localhost:3000/d/demo-cluster-overview> (admin / admin, or browse
anonymously). If the port-forward stopped, run `./scripts/grafana.sh`.

**Stop the fault:**

```bash
./scripts/trigger-oomkill.sh --stop
```

The slow leak, the ConfigMap watcher, the dashboard panels and every script
are described in [demo-cluster/README.md](demo-cluster/README.md).

## LLM (optional)

K8sGPT works without an LLM: it still finds problems and writes Results, only
without the AI explanation. To add explanations, fill in `demo-cluster/.env`
and re-run `./scripts/up.sh`.

| Option | `.env` settings | Guide |
|---|---|---|
| OpenAI | `LLM_API_KEY=<key>` (model defaults to `gpt-4o-mini`) | [`.env.example`](demo-cluster/.env.example) |
| Groq free tier | `LLM_API_KEY`, `K8SGPT_MODEL=openai/gpt-oss-20b`, `K8SGPT_BASE_URL=https://api.groq.com/openai/v1` | [docs/groq-free-tier-setup.md](docs/groq-free-tier-setup.md) |
| Ollama on your laptop | `K8SGPT_LOCAL=true` (model `gpt-oss:20b`) | [demo-cluster/README.md](demo-cluster/README.md#local-llm-with-ollama) |
| LM Studio on your laptop | `K8SGPT_LOCAL=true`, `K8SGPT_MODEL`, `K8SGPT_BASE_URL` | [demo-cluster/docs/lm-studio.md](demo-cluster/docs/lm-studio.md) |

Any other OpenAI-compatible API works the same way, through `K8SGPT_MODEL`
and `K8SGPT_BASE_URL`; `.env.example` has an example for Claude.

> **What the LLM provider receives.** With an LLM configured, K8sGPT sends what
> it finds in the cluster (resource names, namespaces, events, error messages)
> to the provider to get an explanation. Its anonymize option (on by default)
> masks some object names, but error and event text can still name your
> resources. Use a provider you are allowed to send that data to, or a model
> on your laptop, which keeps the data on your machine.

## Slack (optional)

Without Slack, Results stay in the cluster and you read them with `kubectl`.
To have K8sGPT post each new Result to a channel, create a Slack
[incoming webhook](https://api.slack.com/messaging/webhooks), put its URL in
`demo-cluster/.env` as `SLACK_WEBHOOK_URL`, check it with
`./scripts/check-slack.sh`, and re-run `./scripts/up.sh`. Use a workspace and
channel you are allowed to post cluster findings to. Step-by-step setup:
[demo-cluster/README.md](demo-cluster/README.md#slack-optional).

## Reset and teardown

Run these from `demo-cluster/`:

| Command | What it does |
|---|---|
| `./scripts/soft-reset.sh` | Keeps the cluster and returns it to its just-installed state: stops the faults, replaces the sample app pods, wipes Prometheus data, deletes all Results. |
| `./scripts/reset.sh` | Deletes the cluster and builds it again (`down.sh`, then `up.sh`). |
| `./scripts/down.sh` | Stops the Grafana port-forward and the OOMKill loop, then deletes the kind cluster. |

From the repository root, `make down` runs `down.sh`.

`down.sh` deletes only the kind cluster `demo-cluster`, through `kind delete
cluster`. kind also removes its context, so switch back to your usual one
afterwards (`kubectl config use-context <name>`).

## Repo layout

```
.
├── demo-cluster/         the workshop cluster: start here
│   ├── scripts/          up, down, reset, fault triggers, helpers
│   ├── manifests/        sample apps, load generator, K8sGPT resource, Slack relay and probe
│   ├── helm/             values for kube-prometheus-stack, the K8sGPT operator and the watcher
│   ├── grafana/          the dashboard (JSON)
│   ├── docs/             running K8sGPT with LM Studio
│   └── .env.example      optional settings (LLM, Slack)
├── k8s-watcher/              Go source of the watcher
├── charts/k8s-watcher/       Helm chart for the watcher
└── docs/                 Groq setup, how the dashboard was built
```

The **watcher** is a small Go service that turns Kubernetes Warning events (EV
lane) and changes to opted-in ConfigMaps (CM lane) into K8sGPT Result objects.
The demo runs the CM lane only. [k8s-watcher/README.md](k8s-watcher/README.md)
describes what it reports and how to configure it. To work on it (Go 1.27.1),
from the repository root:

```bash
make test       # go test -race in k8s-watcher/
make image      # docker build -t k8s-watcher:dev k8s-watcher/
make helm-lint  # lint and render charts/k8s-watcher
make ci         # every CI check; none needs a cluster
```

`up.sh` rebuilds the image and loads it into the cluster on every run.

## Troubleshooting

- **A setup step failed.** `up.sh` prints `FAIL`, the step's log file and its
  last 20 lines, then stops. Every step logs to
  `demo-cluster/.run/demo-cluster/up-<timestamp>/NN-<step>.log`; the K8sGPT
  resource it applied is saved there too, as `k8sgpt-cr.yaml`. Fix the cause
  and re-run `./scripts/up.sh`.
- **Docker is not running, or runs out of memory.** Start Docker and give it
  at least 6 GB (Docker Desktop: Settings > Resources).
- **kind fails to start the node.** The pinned node image needs kind v0.33.0
  or later. Upgrade kind, or run with `KIND_NODE_IMAGE= ./scripts/up.sh` to use
  your kind version's default image.
- **Port 3000 is busy.** `GRAFANA_PORT=3001 ./scripts/grafana.sh`.
- **No AI text in the Results.** Check the key and model in `.env`, then
  re-run `./scripts/up.sh`; it also restarts the operator, which stops
  calling the LLM after repeated failures. See
  [demo-cluster/README.md](demo-cluster/README.md#llm-optional).

More cases: [demo-cluster/README.md](demo-cluster/README.md#troubleshooting).

## About

Maintained by Data Agent Inc. ([data-agent.co](https://data-agent.co)), which
also builds a commercial autonomous observability and remediation platform.
See [MAINTAINERS.md](MAINTAINERS.md).

## Disclaimer

Educational workshop code, provided as-is, without warranty or support. Not
for production use.
K8sGPT is a CNCF project; this project is not affiliated with or endorsed by
K8sGPT or the CNCF.
The Apache License 2.0 does not grant permission to use the trade names,
trademarks or product names of Data Agent Inc.

## License

Apache License 2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). The watcher
contains code copied from other Apache-2.0 projects; see
[k8s-watcher/NOTICE](k8s-watcher/NOTICE).
