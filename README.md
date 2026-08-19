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

## Run

```sh
export WEBGATE_TOKEN=dev-token
export SEARXNG_BASE_URL=http://127.0.0.1:8080   # needed for provider searxng / auto
go run ./cmd/webgate
```

`POST /v1/search` with `Authorization: Bearer` and JSON `{"query":"..."}`. Optional `limit` 1–20.

`provider` resolution:

- omitted or `"llm"` → sequential `openai` then `xai` (unconfigured sources skipped)
- `"auto"` → `searxng` then `openai` then `xai` (unconfigured skipped)
- `"searxng"` | `"openai"` | `"xai"` → that source only (no fallback; missing → `missing_config`)
- `"google"`, `"all"`, arrays, and unknown names → `invalid_input`

OpenAI / xAI HTTP clients are not wired yet; inject them in tests or leave unset so `llm`/`auto` skip those slots. `SEARXNG_BASE_URL` still registers SearXNG for `searxng` / `auto`.

## Status

#3: provider resolver (`llm` / `auto` / single source) with stop-vs-continue fallback. Fetch, hosted LLM search HTTP, and compose are later tickets.
