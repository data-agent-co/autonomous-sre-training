# Groq free tier for the demo cluster

K8sGPT needs an LLM to explain what it finds. Groq has a free API tier that
works with the demo cluster. No credit card needed.

> **Groq is not Grok.** Groq (console.groq.com) is the inference provider
> used here. xAI's Grok has no free API tier.

With a key configured, K8sGPT sends what it finds in the demo cluster
(resource names, namespaces, events, error messages) to Groq. See the
[top-level README](../README.md#llm-optional) for what that includes.

## 1. Create the account

1. Go to <https://console.groq.com>.
2. Sign up with Google, GitHub, SSO or email.
3. Open **API Keys** and click **Create API Key**.
4. Copy the key (`gsk_...`). It is shown only once.

## 2. Configure `demo-cluster/.env`

Copy [`demo-cluster/.env.example`](../demo-cluster/.env.example) to
`demo-cluster/.env` if you have not yet, and set:

```
LLM_API_KEY=gsk_your_key
K8SGPT_BACKEND=openai
K8SGPT_MODEL=openai/gpt-oss-20b
K8SGPT_BASE_URL=https://api.groq.com/openai/v1
K8SGPT_LOCAL=false
```

- `K8SGPT_BACKEND=openai` because Groq's API is OpenAI-compatible.
- Keep `K8SGPT_LOCAL=false`. `true` is for a model on your laptop.
- Do not use `llama-3.1-8b-instant`. Groq retired it (404 `model_not_found`).
- `.env` is gitignored. Never commit the key.

## 3. Check the key

From the repository root:

```bash
set -a; source demo-cluster/.env; set +a
curl -si https://api.groq.com/openai/v1/chat/completions \
  -H "Authorization: Bearer $LLM_API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-oss-20b","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}' \
  | grep -iE "^HTTP|x-ratelimit"
```

- `HTTP/2 200` plus `x-ratelimit-*` headers: the key works.
- `401`: wrong or missing key.
- `404`: the model name is wrong. List valid names with
  `curl -s https://api.groq.com/openai/v1/models -H "Authorization: Bearer $LLM_API_KEY"`.

## 4. Apply it

From `demo-cluster/`:

```bash
./scripts/up.sh
```

This works for a new cluster and for a running one. On a running cluster,
`up.sh` updates the key and the K8sGPT resource, replaces the K8sGPT pod
when the key, model or URL changed (the pod reads them only when it
starts), and restarts the K8sGPT operator. The restart matters after a bad key or model: once LLM calls have
failed several times in a row, the operator stops asking for explanations
until it restarts.

The header `up.sh` prints should read
`AI explanations: on: backend openai, model openai/gpt-oss-20b`.

## 5. Free limits

Limits for `openai/gpt-oss-20b` (the same for `openai/gpt-oss-120b`), from
Groq's [rate limits page](https://console.groq.com/docs/rate-limits), which
has the current values:

| Limit | Value |
|---|---|
| Requests per minute | 30 |
| Requests per day | 1,000 |
| Tokens per minute | 8,000 |
| Tokens per day | 200,000 |

Tokens are the tight limit. One K8sGPT call uses about 1,000 to 1,400
tokens (prompt plus model reasoning), so expect roughly 150 calls per day.
The K8sGPT resource's `backOff` setting slows K8sGPT down when Groq answers
429.

The demo scenarios fit easily. An OOMKill run used about 5 calls. The slow
leak uses none until the container is OOMKilled, because a growing leak is not
a Pod failure.

## 6. Check real usage

Response headers can be inaccurate. Use the console, signed in:

- Logs, one row per request with tokens and errors:
  <https://console.groq.com/dashboard/logs>
- Usage and spend (can lag up to 15 minutes):
  <https://console.groq.com/dashboard/usage>
- Plan limits: <https://console.groq.com/settings/limits>
