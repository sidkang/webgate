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
export CDP_ENDPOINT=ws://cloak-manager:9222/devtools/browser/...   # no localhost default
# export CDP_API_KEY=...          # optional, env only
# export WEBGATE_CLOAK_DISABLED=1 # fetch returns cloak_disabled; search still works
go run ./cmd/webgate
```

### Search — `POST /v1/search`

`Authorization: Bearer` and JSON body. Optional `limit` 1–20.

With only `SEARXNG_BASE_URL` set (current `main.go`), send `provider: "searxng"` or `"auto"`:

```json
{"query":"...","provider":"searxng"}
```

Omitted/`"llm"` resolves to `openai` then `xai`, but those HTTP clients are not wired yet — so omitted/`llm` returns `missing_config` until a later ticket. `"auto"` works today because it can use SearXNG and skip unset OpenAI/xAI.

`provider` resolution:

- omitted or `"llm"` → sequential `openai` then `xai` (unconfigured sources skipped; needs OpenAI/xAI later)
- `"auto"` → `searxng` then `openai` then `xai` (unconfigured skipped)
- `"searxng"` | `"openai"` | `"xai"` → that source only (no fallback; missing → `missing_config`)
- `"google"`, `"all"`, arrays, and unknown names → `invalid_input`

### Fetch — `POST /v1/fetch`

Acquires one URL via CDP Attach to the **server-configured** Cloak profile (background tab). Clients never send `profile`.

```json
{"url":"https://example.com","mode":"readable","kernel":"defuddle"}
```

- `url` required (http/https only; local/private/metadata blocked)
- `mode` omitted → `readable`; `raw` returns captured HTML (same acquire path)
- `kernel` omitted → `defuddle`; chain falls back per host web-access rules (`html-extractor`, then `llm` when configured)
- `WEBGATE_CLOAK_DISABLED` → `cloak_disabled` (503); missing `CDP_ENDPOINT` (and no injected capturer) → `missing_config`
- Inline content default 30_000 chars (cap 200_000) with truncation notice; no tail retrieval

## Status

#4: `POST /v1/fetch` via CDP Attach + kernel fallback. Hosted LLM search HTTP and compose are later tickets.
