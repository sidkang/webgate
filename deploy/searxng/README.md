# SearXNG deploy unit (webgate compose)

Bundled with the webgate compose stack. Official `searxng/searxng` (pinned tag)
plus an **in-container** Google engine that talks to an external Cloak CDP profile.

- On the compose network only (`expose: 8080`); not published to the host
- Google CDP proxy stays on `127.0.0.1:3100` inside the container
- Keeps `/etc/searxng` and `/var/cache/searxng` volumes
- Does **not** launch Chrome, Redis, or Valkey
- Google is a SearXNG engine (`!g`), never a webgate `provider`
- First boot materializes `settings.yml` from the template with a **random**
  `secret_key` and `search.formats: [html, json]`

`CDP_ENDPOINT` / `CDP_API_KEY` must match webgate (same launched Cloak profile).

### Existing `settings.yml`

Entrypoint **never rewrites** an existing `/etc/searxng/settings.yml`. If the
config volume already has a file without `search.formats` including `json`,
SearXNG returns **403** for `GET /search?format=json`. Merge
[`settings.yml.snippet`](settings.yml.snippet) and restart.
