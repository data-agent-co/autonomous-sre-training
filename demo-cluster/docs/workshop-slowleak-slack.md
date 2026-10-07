# Workshop: From a slow leak to a K8sGPT alert in Slack

In this lab you make recommend-svc leak memory with `trigger-leak.sh` and wait for the alert in Slack. K8sGPT stays silent while memory climbs and posts its diagnosis only after the leak ends in an OOMKill. Grafana is only the view on the side, and the one place the leak shows early.

## Before you start

You need the demo cluster running with Slack and an LLM turned on. Run every command from `demo-cluster/`. If you just did the [OOMKill lab](workshop-oomkill-slack.md), the same setup works.

- [ ] `.env` has `SLACK_WEBHOOK_URL` set to the webhook for `#incidents-demo`
- [ ] `./scripts/check-slack.sh` prints `OK: Slack webhook accepted the test message`
- [ ] The LLM answers. For LM Studio: `curl -s localhost:1234/v1/models` lists `gemma-3-1b-it`. If not, run `lms server start --port 1234 --bind 0.0.0.0` (see [lm-studio.md](lm-studio.md))
- [ ] `./scripts/up.sh` has run since you set Slack and the LLM. Its summary says `AI explanations: on` and `Slack: on`
- [ ] Start clean, so an earlier fault doesn't mix in: `./scripts/soft-reset.sh`. It also stops a running OOMKill loop
- [ ] All demo pods are healthy: `./scripts/check-pods.sh demo-apps` prints `all pods healthy in demo-apps`

## Step 1: Open Slack (and Grafana, if you like)

Open `#incidents-demo` and scroll to the bottom. Everything after your `check-slack.sh` test post comes from this lab.

Optional: open Grafana at <http://localhost:3000/d/demo-cluster-overview> (login `admin` / `admin`) next to it. In this lab Grafana is worth a look: it is the only place the leak shows before it breaks anything.

## Step 2: Start the leak

Run:

```bash
./scripts/trigger-leak.sh
```

It prints `==> leak-fixture enabled at 280MB/min` and returns.

What happens:

1. The recommend-svc pod has two containers: the service itself, and a `leak-fixture` sidecar that is idle until now.
2. The script sets `LEAK_RATE_MB_PER_MIN=280` on the sidecar in the Deployment. Kubernetes rolls out a new recommend-svc pod with that setting.
3. The sidecar grabs 280 MiB more memory per minute, about 4.7 MiB a second, from a baseline of about 15 MiB, against its own 128 MiB limit.
4. The rate lives in the Deployment, so after each OOMKill the sidecar restarts and leaks again.

Want more time to watch the climb? Run `./scripts/trigger-slowleak.sh` instead: at 20 MB/min it reaches the limit in about 6 minutes instead of 30 seconds.

To watch the memory from the terminal, run `./scripts/leak-progress.sh` in a spare terminal. It draws a live bar of the sidecar's memory against its limit; Ctrl-C quits.

## Step 3: The quiet phase — memory climbs, Slack stays silent

For the first half minute nothing reaches Slack. That is the point of this lab: the leak is real, but nothing has failed yet, so no analyzer has anything to report.

At `trigger-leak.sh`'s 280 MB/min:

| Time after the trigger | Sidecar memory | Share of the 128 MiB limit | Slack |
| --- | --- | --- | --- |
| +10 s | ~45 MiB | ~35% | nothing |
| +20 s | ~90 MiB | ~70% | nothing |
| ~+28 s | OOMKilled | | the alerts start (Step 4) |

Ask the group: which tool would you need to catch this before it crashes? For a longer quiet phase to discuss it, run `./scripts/trigger-slowleak.sh` instead (about 6 minutes).

## Step 4: The OOMKill and the K8sGPT alert in Slack

When the sidecar hits 128 MiB, the kernel kills it and two posts reach `#incidents-demo`: a short Prometheus alert first, then K8sGPT's diagnosis. K8sGPT's post is the main result of the lab.

How the K8sGPT alert gets there:

1. The kernel OOMKills the `leak-fixture` container (exit code 137). The kubelet restarts it, and it leaks again.
2. After a few restarts the pod is in CrashLoopBackOff. K8sGPT scans the `demo-apps` Pods about every 30 seconds, sees the OOMKilled container and writes a Result.
3. K8sGPT sends the error to the LLM, which adds an explanation and suggested fixes.
4. The K8sGPT operator posts it to Slack through `slack-relay`, which drops repeats while the fault stays live.

The two posts look like this. The Solution wording changes from run to run, because the LLM writes it:

```
[alertmanager] DemoContainerOOMKilled: demo-apps/recommend-svc-574d8879ff-snrc9
Container leak-fixture in demo-apps/recommend-svc-574d8879ff-snrc9 was OOMKilled

[k8sgpt-sample] K8sGPT analysis of the Pod demo-apps/recommend-svc-574d8879ff-snrc9
Report
Error: OOMKilled exitCode=137 container=leak-fixture pod=recommend-svc-574d8879ff-snrc9.
Solution: 1. Check Resource Limits: verify your pod's resource requests and limits
are appropriate for its workload. 2. ... 4. Increase Resources ...
```

Timing with `trigger-leak.sh`: the first OOMKill came 28 seconds after the trigger in two runs, and the Alertmanager post about 20 seconds after it. In earlier runs, K8sGPT's post came 2–7 minutes after the Alertmanager post. K8sGPT is slower here than in the OOMKill lab because it waits for CrashLoopBackOff.

The Result behind the K8sGPT post:

```bash
kubectl --context kind-demo-cluster -n k8sgpt-system get results \
  -o custom-columns='OBJECT:.spec.name,ERROR:.spec.error[0].text'
```

**Check yourself:** the K8sGPT post names `container=leak-fixture`, not `recommend-svc`. The service container never fails; only the sidecar does.

## Step 5 (optional): See the leak early in Grafana

Grafana is the answer to the question in Step 3: it shows the leak before any alert. These panels move during the quiet phase:

| Panel | What you see | When, with `trigger-leak.sh` | When, with `trigger-slowleak.sh` |
| --- | --- | --- | --- |
| Memory growth rate (per pod, 2m window) | The leak-fixture line rises above the yellow (100 KB/s) and red (500 KB/s) lines | Within ~20 s | Within a minute, ~330 KB/s (yellow) |
| Saturation — memory % of limit (S) | The leak-fixture line climbs (yellow from 50%, red from 80%) and shows 100% once the sidecar is OOMKilled, for as long as it keeps being killed | One sample around ~50%, then 100% at ~+30 s | ~+3 min, ~+4 min, 100% at ~+6 min |
| Composite Health Score | Drops 20 points at 50% of the limit and 30 more at 80% | Same times | Same times |

Container memory is sampled every 10–15 s, so at the default rate the climb shows as one or two points before the jump to 100%; use `trigger-slowleak.sh` to watch it build.

After the OOMKill, OOMKills (last 5m), Restarts (last 5m) and CrashLoopBackOff turn yellow or red, as in the OOMKill lab.

## Step 6: Stop the leak and reset

Stop the leak when you are done. The rate lives in the Deployment, so left alone it leaks and crashes forever, and with Slack on the channel keeps getting posts.

```bash
./scripts/trigger-leak.sh --off
```

It prints `==> leak-fixture disabled` and rolls out a new recommend-svc pod at baseline memory.

To also clear the graphs and K8sGPT's Results, run `./scripts/soft-reset.sh` instead.

You are done when no new posts arrive in `#incidents-demo` and Grafana's top row is green again.

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Nothing in Slack after 1 minute | With `trigger-leak.sh` the first OOMKill comes at ~+28 s; with `trigger-slowleak.sh` at about +6 min | Check `kubectl --context kind-demo-cluster -n demo-apps get pods` for `leak-fixture` restarts; with `trigger-slowleak.sh`, keep waiting |
| Only the Alertmanager post arrives | K8sGPT is waiting for the pod to reach CrashLoopBackOff | Wait a few more minutes; check `get results` from Step 4 |
| A K8sGPT post names a recommend-svc pod that no longer exists | The trigger rolled out a new pod, and K8sGPT caught the old one while it shut down | Ignore it; the real post names the new pod and `container=leak-fixture` |
| The post has an empty Report | K8sGPT can't reach the LLM (for LM Studio: `connection refused` on port 1234) | Start the LLM server, then `kubectl --context kind-demo-cluster -n k8sgpt-system rollout restart deploy/k8sgpt-operator-controller-manager deploy/k8sgpt-sample` |
| The same pod is posted again later | The leak ran long enough for K8sGPT to repeat after the relay's 10-minute quiet window | Stop the leak when you finish (Step 6) |
| No post at all, but `check-slack.sh` works | Slack was off when `up.sh` ran | Run `./scripts/up.sh`, then check `kubectl --context kind-demo-cluster -n k8sgpt-system logs deploy/slack-relay` |
| `leak-progress.sh` keeps waiting for metrics-server | metrics-server has no data yet | Wait a minute |
