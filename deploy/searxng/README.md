# SearXNG deploy unit (webgate compose)

Bundled with the webgate compose stack. Official `searxng/searxng` plus an
**in-container** Google engine that talks to an external Cloak CDP profile.

- Publishes only SearXNG `:8080`; Google CDP proxy stays on `127.0.0.1:3100`
- Keeps `/etc/searxng` and `/var/cache/searxng` volumes
- Does **not** launch Chrome, Redis, or Valkey
- Google is a SearXNG engine (`!g`), never a webgate `provider`

`CDP_ENDPOINT` / `CDP_API_KEY` must match webgate (same launched Cloak profile).

See the repo root `README.md` and `compose.yaml` for how to run this unit.
Merge [`settings.yml.snippet`](settings.yml.snippet) into an existing
`settings.yml` if you bring your own config volume.
