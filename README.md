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

## Compose (webgate + SearXNG + optional Cloak Manager)

**webgate** (`:8787`) is the only caller-facing API. Bundled **SearXNG** is
internal to the compose network (`expose: 8080`, no host publish). Optional
**Cloak Manager** binds to `127.0.0.1:8081` via `compose.cloak.yaml`.

Secrets stay in `.env` (see `.env.example`). Never commit real tokens.

```sh
cp .env.example .env
# set WEBGATE_TOKEN=…
mkdir -p deploy/searxng/config deploy/searxng/data

# Default: webgate (LAN :8787) + internal SearXNG
docker compose up -d --build

# Optional local Manager (localhost only; AUTH_TOKEN required)
CLOAK_AUTH_TOKEN=… docker compose -f compose.yaml -f compose.cloak.yaml --profile cloak up -d --build
```

### First-run with Cloak fetch / SearXNG `!g`

1. Start the stack (add the cloak overlay if you need a local Manager).
2. Open Manager from the host (`http://127.0.0.1:8081`), create/launch a profile.
3. Set `CDP_ENDPOINT` in `.env` (and `CDP_API_KEY` if required) to that profile’s CDP URL. From the **compose network**, use `http://cloak-manager:8080/api/profiles/<id>/cdp` — not `127.0.0.1:8081`. Compose cannot invent this URL.
4. `docker compose up -d` again so **webgate** and **searxng** both see the same `CDP_ENDPOINT` / `CDP_API_KEY`.
5. webgate **does not** call Manager to launch profiles. Google stays **inside SearXNG** (`!g`). There is no webgate `provider: google`.

### Cloak off / external substitutes

| Goal | How |
| --- | --- |
| No Cloak | Omit the cloak overlay. Set `WEBGATE_CLOAK_DISABLED=1`. `POST /v1/fetch` returns `cloak_disabled`. `provider: searxng` still hits SearXNG. |
| Existing Manager | Do not start `cloak-manager`. Set `CDP_ENDPOINT` to that instance’s launched profile. |
| External SearXNG | Set `SEARXNG_BASE_URL` and `docker compose up -d --build webgate --no-deps`. |
| Bundled SearXNG without Google CDP | Leave `CDP_ENDPOINT` empty; other engines still work; `!g` needs CDP. |

First boot writes `search.formats: [html, json]` and a **random** `secret_key`. Existing `deploy/searxng/config/settings.yml` is never rewritten — merge `search.formats` yourself if missing (else 403 on `format=json`).

Validate YAML: `docker compose config` (cloak: add `-f compose.cloak.yaml` and set `CLOAK_AUTH_TOKEN`).

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
