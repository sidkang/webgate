# webgate

Homelab **web access** service. Clients call this; they do not talk CDP.

Repo: https://github.com/sidkang/webgate

Not an app. `ai-news` and later news / finance packs are callers. This repo is search / fetch and the adapters behind them.

## Core API

- `search` — LLM-assisted search and/or fetch a source list page
- `fetch` — get detail for a URL/ref (DOM preferred, raw exists)

Special adapters (planned): YouTube captions, self-hosted SearXNG, Cloak Manager CDP profiles.

## Phase 1 shape

One server next to **Cloak Manager** on the **128G homelab**. Many Chromium **profiles**, not many scrape servers. No geo split yet.

```
clients (Pi, process, …)
        │  optional CF Worker — 分流 only
        v
 webgate (this repo, homelab)
        │  connectOverCDP
        v
 Cloak Manager profiles  (pin: 财新 / 雪球 / X; else any)
```

Return follows the same path. webgate does **not** store pipeline items (hidden cache only). Durable data stays on the homelab store, owned by callers.

Pins are **static config** (profile id). No runtime failover.

## Stack

Go service. Local-first. Cloudflare free (Worker hop, Tunnel) is optional.

## Run (local Go)

```sh
export WEBGATE_TOKEN=dev-token
export SEARXNG_BASE_URL=http://127.0.0.1:8080   # provider searxng / auto
export CDP_ENDPOINT=ws://…/devtools/browser/...   # launched Cloak profile
# export CDP_API_KEY=...
# export WEBGATE_CLOAK_DISABLED=1

# Hosted web_search (CLIProxy / compatible Responses). Secrets stay in env.
export OPENAI_BASE_URL=https://proxy.example/v1
export OPENAI_API_KEY=...
export OPENAI_MODEL=gpt-4.1
export XAI_BASE_URL=https://proxy.example/v1
export XAI_API_KEY=...
export XAI_MODEL=grok-4
# Official https://api.x.ai (and *.api.x.ai) is rejected — use a proxy route.

go run ./cmd/webgate
```

Listens on `WEBGATE_ADDR` (default `:8787`, all interfaces). Clients send
`Authorization: Bearer $WEBGATE_TOKEN`.

## Compose (webgate + SearXNG + optional Cloak Manager)

`compose.yaml` runs **webgate** and a bundled **SearXNG** unit that includes a
Google-via-CDP engine. CloakBrowser-Manager is optional (`--profile cloak`).

Secrets stay in `.env` (see `.env.example`). Never commit real tokens.

```sh
cp .env.example .env
# set WEBGATE_TOKEN=…
mkdir -p deploy/searxng/config deploy/searxng/data

# Default: webgate (:8787) + SearXNG (:8080)
docker compose up -d --build

# Also start Cloak Manager on :8081 (profile data in a named volume)
docker compose --profile cloak up -d --build
```

### First-run with Cloak fetch / SearXNG `!g`

1. Start the stack (with `--profile cloak` if you need a local Manager).
2. Open Manager from the host (`http://127.0.0.1:8081`), create/launch a profile.
3. Set `CDP_ENDPOINT` in `.env` (and `CDP_API_KEY` if required) to that profile’s CDP URL. From the **compose network**, use the Manager service hostname and **container** port, e.g. `http://cloak-manager:8080/api/profiles/<id>/cdp` — not `127.0.0.1:8081` (that address is only for the host browser). Compose cannot invent this URL; the operator sets it after launch.
4. `docker compose up -d` again so **webgate** and **searxng** both see the same `CDP_ENDPOINT` / `CDP_API_KEY`.
5. webgate **does not** call Manager to launch profiles. Google stays **inside SearXNG** (`!g` / `google-cdp` engine). There is no webgate `provider: google`.

### Cloak off / external substitutes

| Goal | How |
| --- | --- |
| No Cloak | Omit `--profile cloak`. Set `WEBGATE_CLOAK_DISABLED=1`. `POST /v1/fetch` returns `cloak_disabled`. `provider: searxng` still hits SearXNG. |
| Existing Manager | Do not start `cloak-manager`. Set `CDP_ENDPOINT` to that instance’s launched profile. |
| External SearXNG | Set `SEARXNG_BASE_URL` to that instance and start webgate without its dependency: `docker compose up -d --build webgate --no-deps`. |
| Bundled SearXNG without Google CDP | Leave `CDP_ENDPOINT` empty; other SearXNG engines still work; `!g` needs CDP. |

Validate YAML without bringing the stack up: `docker compose config`.

### Search — `POST /v1/search`

`Authorization: Bearer` and JSON body. Optional `limit` 1–20.

```json
{
  "query": "...",
  "provider": "llm",
  "search_context_size": "high",
  "allowed_domains": ["example.com"],
  "user_location": {"country": "CN", "city": "Shanghai"}
}
```

`provider` resolution:

- omitted or `"llm"` → sequential `openai` then `xai` (unconfigured skipped)
- `"auto"` → `searxng` then `openai` then `xai`
- `"searxng"` | `"openai"` | `"xai"` → that source only (no fallback)
- JSON array of **two or more** of those names → run configured sources **in parallel**, dedupe evidence, and **always** LLM-merge with the OpenAI Responses model (`OPENAI_*`). Response `provider` is the list that ran; `merge.ok` is true on success. Merge failure still returns HTTP 200 with labelled per-source answers (`## openai\n…`) plus `merge: {ok:false, code, error}` — not a total search failure. A length-1 array is invalid (list-merge only). Unconfigured names in the list are skipped; if only one configured source remains, that single result is returned without merge.
- `"google"`, `"all"` → `invalid_input`
- There is **no** `merge` request field (any value → `invalid_input`)

Extra fields: `search_context_size` (`low`|`medium`|`high`, OpenAI default high); `allowed_domains`; `user_location` (country only if two-letter ISO code — names like `"China"` are dropped). xAI ignores context size / location and rejects >5 allowed domains.

### X search — `POST /v1/x_search`

Uses `XAI_*` config. Never falls back to `web_search`. Official `api.x.ai` → `missing_config`.

```json
{"query":"...","from_date":"2024-01-01","to_date":"2024-01-31"}
```

Dates optional `YYYY-MM-DD`; reversed or invalid calendar → `invalid_input`. Success requires assistant text plus `x_search_call` or an X/Twitter citation URL.

### Fetch — `POST /v1/fetch`

Acquires one URL via CDP Attach to the **server-configured** Cloak profile. Clients never send `profile`.

```json
{"url":"https://example.com","mode":"readable","kernel":"defuddle"}
```

## Status

#7: Compose stack — webgate + SearXNG (Google CDP) + optional Cloak Manager.
