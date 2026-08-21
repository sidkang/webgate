# syntax=docker/dockerfile:1

# webgate compose SearXNG deploy unit: official SearXNG runtime plus an
# in-container Google-via-CDP engine. Volumes, settings, and startup match
# the upstream image; only the CDP client is added. No Chrome in this image.
ARG SEARXNG_IMAGE="docker.io/searxng/searxng:2026.8.20-487d7a96e"
ARG UV_IMAGE="ghcr.io/astral-sh/uv:0.10.10"
ARG PYPI_INDEX_URL="https://pypi.tuna.tsinghua.edu.cn/simple"
ARG PLAYWRIGHT_VERSION="1.62.0"

FROM ${UV_IMAGE} AS uv

FROM ${SEARXNG_IMAGE} AS proxy-builder

ARG PYPI_INDEX_URL
ARG PLAYWRIGHT_VERSION

ENV UV_INDEX_URL=${PYPI_INDEX_URL} \
    UV_LINK_MODE=copy \
    UV_NO_MANAGED_PYTHON=true

COPY --from=uv /uv /uvx /usr/local/bin/

# Playwright is the only direct dependency added by this image. It connects to
# CDP_ENDPOINT and therefore does not install a browser in this image.
RUN uv venv --python python3 /opt/google-cdp/.venv \
    && uv pip install --python /opt/google-cdp/.venv/bin/python \
      "playwright==${PLAYWRIGHT_VERSION}"

FROM ${SEARXNG_IMAGE}

ARG CREATED="0001-01-01T00:00:00Z"
ARG VERSION="unknown"
ARG VCS_URL="unknown"
ARG VCS_REVISION="unknown"

LABEL org.opencontainers.image.created="${CREATED}" \
    org.opencontainers.image.description="SearXNG with an internal Google CDP engine (webgate compose unit)" \
    org.opencontainers.image.documentation="https://docs.searxng.org/admin/installation-docker" \
    org.opencontainers.image.licenses="AGPL-3.0-or-later" \
    org.opencontainers.image.revision="${VCS_REVISION}" \
    org.opencontainers.image.source="${VCS_URL}" \
    org.opencontainers.image.title="SearXNG" \
    org.opencontainers.image.version="${VERSION}"

COPY --chown=977:977 --from=proxy-builder \
  /opt/google-cdp/.venv /opt/google-cdp/.venv
COPY --chown=977:977 container/proxy.py /opt/google-cdp/proxy.py
COPY --chown=977:977 container/settings.template.yml \
  /usr/local/searxng/settings.template.yml
COPY --chmod=755 container/entrypoint.sh /usr/local/bin/searxng-cdp-entrypoint

# The official image already exposes :8080 and its configuration/data volumes.
# The CDP proxy is intentionally kept on container-local 127.0.0.1:3100.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD /usr/local/searxng/.venv/bin/python -c \
    "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/healthz', timeout=3)"

ENTRYPOINT ["/usr/local/bin/searxng-cdp-entrypoint"]
