# webgate

Homelab **web access** service. Clients call this; they do not talk CDP.

Repo: https://github.com/sidkang/webgate

Not an app. `ai-news` and later news / finance packs are callers. This repo is search / fetch and the adapters behind them.

## Core API

- `search` — LLM-assisted search and/or fetch a source list page
- `fetch` — get detail for a URL/ref (DOM preferred, raw exists)

Special adapters (planned): YouTube captions, self-hosted SearXNG.

## Phase 1 shape

One server on the **128G homelab**. One CDP browser (bundled CloakBrowser, or any other existing CDP). No geo split yet.

```
clients (Pi, process, …)
        │  optional CF Worker — 分流 only
        v
 webgate (this repo, homelab)
        │  connectOverCDP
        v
 one Chromium  (cloakserve, or CDP_ENDPOINT override)
```

Return follows the same path. webgate does **not** store pipeline items (hidden cache only). Durable data stays on the homelab store, owned by callers.

No runtime failover. Swap the CDP by changing `CDP_ENDPOINT`.

## Stack

Go service. Local-first. Cloudflare free (Worker hop, Tunnel) is optional.

## Run (local Go)

```sh
export WEBGATE_TOKEN=dev-token
export SEARXNG_BASE_URL=http://127.0.0.1:8080   # provider searxng / auto
export CDP_ENDPOINT=http://127.0.0.1:9222   # any CDP HTTP root, /json/version, or ws://…
# export CDP_API_KEY=...
# export WEBGATE_CLOAK_DISABLED=1   # fetch returns cloak_disabled; search still works

# Hosted web_search via official openai-go Responses client (custom base URL for CLIProxy).
# Secrets stay in env.
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

## Compose (webgate + SearXNG + CloakBrowser)

**webgate** (`:8787`) is the only caller-facing API. Bundled **SearXNG** is
internal to the compose network (`expose: 8080`, no host publish). Bundled
**CloakBrowser** (`cloakserve`) is one stealth Chromium on the compose
network (`expose: 9222`, not published). Fetch and SearXNG `!g` both attach
to it via `CDP_ENDPOINT` (default `http://cloak:9222`).

No Manager and no profiles. Override `CDP_ENDPOINT` to use any other
already-running CDP instead of the bundled browser.

Secrets stay in `.env` (see `.env.example`). Never commit real tokens.

```sh
cp .env.example .env
# set WEBGATE_TOKEN=…
mkdir -p deploy/searxng/config deploy/searxng/data

# webgate (LAN :8787) + internal SearXNG + cloakserve CDP
docker compose up -d --build
```

Google stays **inside SearXNG** (`!g`). There is no webgate `provider: google`.

### CDP / fetch substitutes

| Goal | How |
| --- | --- |
| Other existing CDP | Set `CDP_ENDPOINT` (HTTP root, `/json/version`, or `ws://…`). Same value for webgate and SearXNG. |
| Fetch off | `WEBGATE_CLOAK_DISABLED=1`. `POST /v1/fetch` returns `cloak_disabled`. Search still works. |
| External SearXNG | Set `SEARXNG_BASE_URL` and `docker compose up -d --build webgate --no-deps`. |

First boot writes `search.formats: [html, json]` and a **random** `secret_key`. Existing `deploy/searxng/config/settings.yml` is never rewritten — merge `search.formats` yourself if missing (else 403 on `format=json`).

Validate YAML: `docker compose config`.

### Threat model (short)

`urlguard` / Fetch.pause apply to **main-frame Document** navigations only. Page-initiated iframe / XHR / WebSocket requests to LAN or metadata are **not** blocked in-process — put Chromium on an isolated network. DNS resolution remains fail-open; `198.18.0.0/15` is not specially denied.

After navigate, fetch waits for **network-quiet** + **DOM signature stability** (cheap size signal; hard 64 MiB still enforced after capture).

### Search — `POST /v1/search`

`Authorization: Bearer` and JSON body (`Content-Type: application/json`). Optional `limit` 1–20.

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
- JSON array of **two or more** of those names → parallel search + always LLM-merge (`OPENAI_*`). Merge failure → HTTP 200 labelled raw + `merge.ok=false`.
- `"google"`, `"all"` → `invalid_input`
- There is **no** `merge` request field

`allowed_domains` is enforced in the **OpenAI / xAI request shape only**. `auto` / `provider: searxng` do **not** guarantee that filter (documented; not rejected with 400).

Hosted OpenAI/xAI success requires a nonempty answer **and** structured evidence (`web_search_call` or citation URLs). Answer-only / scraped-link-only → `invalid_response`.

### X search — `POST /v1/x_search`

Uses `XAI_*` config. Never falls back to `web_search`. Official `api.x.ai` → `missing_config`.

```json
{"query":"...","from_date":"2024-01-01","to_date":"2024-01-31"}
```

Dates optional `YYYY-MM-DD`; reversed or invalid calendar → `invalid_input`. Success requires assistant text plus `x_search_call` or an X/Twitter citation URL.

### Fetch — `POST /v1/fetch`

Acquires one URL via CDP Attach to the **server-configured** Cloak profile. Clients never send `profile`. After navigate, fetch waits for **network-quiet** (Document/XHR/Fetch/Script) plus **DOM signature stability** using host timings, then a bounded lazy-load scroll settle — same pipeline as the reference web-access CDP capturer.

```json
{"url":"https://example.com","mode":"readable","kernel":"defuddle"}
```

## Status

Hardening: compose exposure, PendingGate, CDP URL/dialer, HTTP timeouts/caps, hosted evidence.
