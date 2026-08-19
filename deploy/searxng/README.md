# SearXNG deploy unit (webgate compose)

Bundled with the webgate compose stack. Official `searxng/searxng` plus an
**in-container** Google engine that talks to an external Cloak CDP profile.

- Publishes only SearXNG `:8080`; Google CDP proxy stays on `127.0.0.1:3100`
- Keeps `/etc/searxng` and `/var/cache/searxng` volumes
- Does **not** launch Chrome, Redis, or Valkey
- Google is a SearXNG engine (`!g`), never a webgate `provider`
- First-boot `settings.yml` (from `settings.template.yml`) enables
  `search.formats: [html, json]` so webgate `provider: searxng` works

`CDP_ENDPOINT` / `CDP_API_KEY` must match webgate (same launched Cloak profile).

See the repo root `README.md` and `compose.yaml` for how to run this unit.

### Existing `settings.yml`

The image only generates `/etc/searxng/settings.yml` on **first** boot. If the
config volume already has a `settings.yml` without `search.formats` including
`json`, SearXNG returns **403** for `GET /search?format=json` and webgate
`provider: searxng` fails. Merge [`settings.yml.snippet`](settings.yml.snippet)
(or at least the `search.formats` block) into that file and restart.
