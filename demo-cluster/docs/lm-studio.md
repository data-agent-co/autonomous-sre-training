# Running K8sGPT with LM Studio

Use a model on your laptop instead of a cloud API. LM Studio serves an
OpenAI-compatible API, so K8sGPT uses its `openai` backend with a local URL.
No code changes are needed, only `.env` values. Cluster data stays on your
machine.

Tested model: **`gemma-3-1b-it`** (about 1.07 GB). It gave correct answers for
the OOMKill and slow-leak faults.

Run the `./scripts/...` commands below from `demo-cluster/`.

## 1. Install LM Studio

Download the app from <https://lmstudio.ai> and open it once. This installs
the `lms` command-line tool.

Add `lms` to your PATH:

```bash
~/.lmstudio/bin/lms bootstrap
export PATH=$PATH:~/.lmstudio/bin
```

## 2. Download the Gemma model

```bash
lms get https://huggingface.co/lmstudio-community/gemma-3-1b-it-GGUF -y
```

Check it is there:

```bash
lms ls
```

In the app you can instead search for `lmstudio-community/gemma-3-1b-it-GGUF`
in the Discover tab and download it.

> `lms get lmstudio-community/gemma-3-1b-it-GGUF` (without the full URL) fails
> with "artifact does not exist". Use the Hugging Face URL.

## 3. Load the model and start the server

The server must listen on the network. The kind node reaches your laptop through
`host.docker.internal`, so the default `127.0.0.1` binding is refused.

```bash
lms load gemma-3-1b-it -y
lms server start --port 1234 --bind 0.0.0.0
```

In the app, the same settings are in the Developer tab: turn the server on, set
the port to 1234 and enable "Serve on Local Network".

This also makes the server reachable from your local network. On a shared
network, stop it when you are done (`lms server stop`).

## 4. Verify the server

From your laptop:

```bash
curl -s http://localhost:1234/v1/models
```

From inside Docker (this is the path K8sGPT uses):

```bash
docker run --rm curlimages/curl:8.22.0 -s http://host.docker.internal:1234/v1/models
```

Both should list `gemma-3-1b-it`.

## 5. Configure the demo cluster

In `demo-cluster/.env` (copy it from `.env.example` first; it is gitignored):

```bash
K8SGPT_LOCAL=true
K8SGPT_MODEL=gemma-3-1b-it
K8SGPT_BASE_URL=http://host.docker.internal:1234/v1
```

No key is needed: leave `LLM_API_KEY` empty. With `K8SGPT_LOCAL=true` the
backend is always `openai`.

Then bring the cluster up, or apply the change to a running one:

```bash
./scripts/up.sh
```

The header it prints should read
`AI explanations: on: gemma-3-1b-it at http://host.docker.internal:1234/v1`.
To switch to another model later, edit `.env` and run `./scripts/up.sh` again.

K8sGPT runs only its Pod analyzer, on the `demo-apps` namespace
([`manifests/k8sgpt/k8sgpt-cr.yaml`](../manifests/k8sgpt/k8sgpt-cr.yaml)),
which keeps the number of LLM calls low on a laptop.

## 6. Test it

```bash
./scripts/trigger-oomkill.sh
kubectl --context kind-demo-cluster -n k8sgpt-system get results
```

A result with backend `openai` should appear within a minute or two. Read it:

```bash
kubectl --context kind-demo-cluster -n k8sgpt-system get results \
  -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.details}{"\n"}{end}'
```

Stop the faults when done:

```bash
./scripts/trigger-oomkill.sh --stop
./scripts/trigger-slowleak.sh --off
```

To watch requests reach LM Studio:

```bash
lms log stream
```

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `connection refused` to `host.docker.internal:1234` in the operator log | The server is off or bound to localhost. Run `lms server start --port 1234 --bind 0.0.0.0`. |
| Server stops after loading or unloading a model | Check `curl localhost:1234/v1/models` and start the server again. |
| Operator keeps failing after you fix the server | It stopped asking for explanations after repeated failures. Run `./scripts/up.sh` again, which restarts it, or `kubectl --context kind-demo-cluster -n k8sgpt-system rollout restart deploy/k8sgpt-operator-controller-manager`. |
| New result text is identical to an earlier answer | K8sGPT cached it. While comparing models, turn the cache off with `kubectl --context kind-demo-cluster -n k8sgpt-system patch k8sgpt k8sgpt-sample --type merge -p '{"spec":{"noCache":true}}'`. `up.sh` turns it back on. |
| Result is empty | The model is a reasoning model. K8sGPT reads only `content`, and these models fill `reasoning_content`. Use a non-reasoning model such as Gemma. |

The operator log:
`kubectl --context kind-demo-cluster -n k8sgpt-system logs deploy/k8sgpt-operator-controller-manager`.
