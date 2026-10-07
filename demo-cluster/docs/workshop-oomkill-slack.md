# Workshop: From OOMKill to a K8sGPT alert in Slack

In this lab you OOMKill payments-api with `trigger-oomkill.sh` and follow the alert to Slack. K8sGPT analyzes the crashed pod and posts its diagnosis, with an LLM explanation, to `#incidents-demo` within about a minute. Grafana is only the view on the side: it shows the same fault as graphs.

## Before you start

You need the demo cluster running with Slack and an LLM turned on. Run every command from `demo-cluster/`.

- [ ] `.env` has `SLACK_WEBHOOK_URL` set to the webhook for `#incidents-demo`
- [ ] `./scripts/check-slack.sh` prints `OK: Slack webhook accepted the test message`, and the test message shows in the channel
- [ ] The LLM answers. For LM Studio: `curl -s localhost:1234/v1/models` lists `gemma-3-1b-it`. If not, run `lms server start --port 1234 --bind 0.0.0.0` (see [lm-studio.md](lm-studio.md))
- [ ] `./scripts/up.sh` has run since you set Slack and the LLM. Its summary says `AI explanations: on` and `Slack: on`
- [ ] Start clean, so earlier posts and restarts don't mix in: `./scripts/soft-reset.sh`
- [ ] All demo pods are healthy: `./scripts/check-pods.sh demo-apps` prints `all pods healthy in demo-apps`

Without an LLM, K8sGPT still posts, but the Report in Slack is empty.

## Step 1: Open Slack (and Grafana, if you like)

Open `#incidents-demo` and scroll to the bottom. The last message should be the test post from `check-slack.sh`. Everything after it comes from this lab.

Optional: open Grafana at <http://localhost:3000/d/demo-cluster-overview> (login `admin` / `admin`) next to it. Healthy means Demo apps healthy = 3 and every other tile in the top row is 0, green.

## Step 2: Trigger the OOMKill

Run:

```bash
./scripts/trigger-oomkill.sh
```

The script starts a background loop and returns right away. It prints `Loop running (PID …)` and the log path, `.run/demo-cluster/trigger-oomkill.log`.

What the loop does, every second:

1. Finds the payments-api pod and checks that its container is running.
2. Calls `/allocate?mb=200` inside the container through `kubectl exec`. The container's memory limit is 128 MiB.
3. The kernel kills the container (OOMKilled, exit code 137). The kubelet restarts it in the same pod.
4. The loop hits the new container as soon as it runs, so it never looks healthy for long.

After a few restarts the kubelet waits longer between them, up to 5 minutes. The pod then shows `CrashLoopBackOff`.

Two settings change the load, both read from your shell: `OOM_ALLOC_MB` (default 200) and `OOM_INTERVAL` (default 1 second). Example: `OOM_ALLOC_MB=300 ./scripts/trigger-oomkill.sh`.

## Step 3: Watch the K8sGPT alert arrive in Slack

Within about a minute of the trigger, `#incidents-demo` gets a post from K8sGPT that names the pod, the OOMKill and what to do about it. This is the main result of the lab.

How the alert gets there:

1. The kernel OOMKills the payments-api container (exit code 137) and the kubelet restarts it.
2. K8sGPT scans the `demo-apps` Pods about every 30 seconds. Its Pod analyzer sees the OOMKilled container and writes a Result in `k8sgpt-system`.
3. K8sGPT sends the error to the LLM, which adds an explanation and suggested fixes.
4. The K8sGPT operator posts the Result to Slack through `slack-relay`. The relay forwards the first post per pod and drops repeats while the fault stays live.

The post looks like this. The wording of the Solution changes from run to run, because the LLM writes it:

```
[k8sgpt-sample] K8sGPT analysis of the Pod demo-apps/payments-api-66959989c5-d222p
Report
Error: OOMKilled exitCode=137 container=payments-api pod=payments-api-66959989c5-d222p
Solution: 1. Check resource limits for the pod (CPU/Memory). 2. Increase pod resource
requests and limits. 3. Optimize application to reduce memory usage.
```

The Result behind the post is in the cluster too:

```bash
kubectl --context kind-demo-cluster -n k8sgpt-system get results \
  -o custom-columns='OBJECT:.spec.name,ERROR:.spec.error[0].text'
```

You will usually see a shorter post before K8sGPT's: `[alertmanager] DemoContainerOOMKilled: demo-apps/payments-api-…`, within about 15 seconds. That is a Prometheus alert. It says that an OOMKill happened; K8sGPT's post says why and what to do.

K8sGPT anonymizes names before it calls the LLM, so the explanation may show a pod name as random letters, such as `pod=ZnIsN21L…`. The headline always has the real name.

**Check yourself:** after 2 minutes the channel has one K8sGPT post for payments-api with a filled-in Report, not two.

## Step 4 (optional): See the same fault in Grafana

Grafana shows the fault as graphs but doesn't explain it. With the dashboard open, you should see these within 1–2 minutes:

| Panel | What you see |
| --- | --- |
| OOMKills (last 5m) | Counts up with each kill; red from 3 |
| Restarts (last 5m) | Climbs; yellow from 1.5, red from 3 |
| CrashLoopBackOff | 0 → 1, red |
| Service errors — % (E) | payments-api spikes toward 100% while its container is down |
| Composite Health Score | Drops below 50, red |
| K8sGPT detection events (per minute) | A blip when K8sGPT writes the Result behind the Slack post |

## Step 5: Stop the fault and reset

Stop the loop when you are done. Left running, it keeps killing the container, and with Slack on, the channel keeps getting posts.

```bash
./scripts/trigger-oomkill.sh --stop
```

It prints `OOMKill loop stopped`. payments-api recovers at its next restart, which can take up to 5 minutes while the kubelet's back-off runs out.

To get a clean dashboard right away, run `./scripts/soft-reset.sh` instead. It stops the loop, replaces the payments-api pod and restarts Prometheus, so the graphs start flat. It also clears K8sGPT's Results.

You are done when no new posts arrive in `#incidents-demo` and Grafana's top row is green again.

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| No K8sGPT post after 3 minutes | Slack was off when `up.sh` ran, or the pod hasn't failed yet | Check `.env`, run `./scripts/up.sh`, then `kubectl --context kind-demo-cluster -n k8sgpt-system logs deploy/slack-relay` |
| `check-slack.sh` prints `FAIL` | The webhook URL is wrong or was revoked | Create a new webhook and update `SLACK_WEBHOOK_URL` in `.env` |
| The post has an empty Report | K8sGPT can't reach the LLM (for LM Studio: `connection refused` on port 1234) | Start the LLM server, then restart K8sGPT: `kubectl --context kind-demo-cluster -n k8sgpt-system rollout restart deploy/k8sgpt-operator-controller-manager deploy/k8sgpt-sample` |
| Only the Alertmanager post arrives | K8sGPT hasn't scanned yet or hasn't caught the pod failing | Wait another minute; check `get results` from Step 3 |
| The same pod is posted again later | The fault ran long enough for K8sGPT to repeat its post after the relay's 10-minute quiet window | Stop the fault when you finish the lab (Step 5) |
| `zsh: no such file or directory: ./scripts/…` | You are not in `demo-cluster/` | `cd demo-cluster` first |
| `Already running (PID …)` | A loop from an earlier run is still going | `./scripts/trigger-oomkill.sh --stop`, then start again |
