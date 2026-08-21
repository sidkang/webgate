#!/usr/bin/env python3
"""Low-frequency Google search proxy via an external Chrome CDP endpoint.

Part of the webgate compose SearXNG deploy unit. Only the Playwright client is
required (no browser in this image). HTTP serving, configuration and JSON
encoding use the standard library; the proxy is private to one SearXNG
container and intentionally serializes requests.
"""

from __future__ import annotations

import json
import logging
import os
import signal
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any
from urllib.parse import parse_qs, urlencode, urlparse

LOG = logging.getLogger("google-cdp-proxy")

EXTRACT_JS = r"""
() => {
  const out = [];
  const seen = new Set();
  const containerSelectors = [
    '#rso .g', '#search div[data-hveid]', '#rso div[data-hveid]',
    '#search a:has(h3)', '#rso a:has(h3)',
  ];
  let nodes = [];
  for (const selector of containerSelectors) {
    const found = document.querySelectorAll(selector);
    if (found.length) { nodes = Array.from(found); break; }
  }
  if (!nodes.length) {
    nodes = Array.from(document.querySelectorAll('#search a[href], #rso a[href]'))
      .filter(anchor => anchor.querySelector('h3'));
  }
  const snippetSelectors = [
    '.VwiC3b', "[data-sncf='1']", "div[style*='webkit-line-clamp']",
    "div[role='text']", '.lEBKkf', '.MUxGbd',
  ];
  for (const node of nodes) {
    const h3 = node.querySelector('h3');
    const title = (h3?.innerText || '').trim();
    if (!title) continue;
    const anchor = node.tagName === 'A'
      ? node
      : (node.querySelector('a[href]') || h3.closest('a'));
    const href = anchor?.href || anchor?.getAttribute('href');
    if (!href || seen.has(href)) continue;
    seen.add(href);
    let snippet = '';
    for (const selector of snippetSelectors) {
      const element = node.querySelector(selector);
      if (element?.innerText?.trim()) {
        snippet = element.innerText.trim().replace(/\s+/g, ' ');
        break;
      }
    }
    out.push({title, href, snippet});
  }
  return out;
}
"""


def env_int(name: str, default: int, minimum: int, maximum: int) -> int:
    try:
        value = int(os.getenv(name, str(default)))
    except ValueError:
        return default
    return max(minimum, min(value, maximum))


def clean_google_url(href: str | None) -> str | None:
    if not href:
        return None
    if href.startswith("/url?") or href.startswith("https://www.google.com/url?"):
        target = (parse_qs(urlparse(href).query).get("q") or [None])[0]
        if not target:
            return None
        parsed = urlparse(target)
        if parsed.scheme not in ("http", "https"):
            return None
        host = parsed.hostname or ""
        return None if "google." in host or host.endswith("googleusercontent.com") else target
    parsed = urlparse(href)
    if parsed.scheme not in ("http", "https"):
        return None
    host = parsed.hostname or ""
    return None if "google." in host or host.endswith("googleusercontent.com") else href


class GoogleProxy:
    """One browser connection and one in-flight search at a time."""

    def __init__(self) -> None:
        self.endpoint = os.getenv("CDP_ENDPOINT", "").rstrip("/")
        self.api_key = os.getenv("CDP_API_KEY", "")
        self.min_interval_s = env_int("MIN_INTERVAL_MS", 3000, 0, 60_000) / 1000
        self.goto_timeout_ms = env_int("GOTO_TIMEOUT_MS", 20_000, 1_000, 120_000)
        self.selector_timeout_ms = env_int("WAIT_SELECTOR_TIMEOUT_MS", 10_000, 1_000, 60_000)
        self.captcha_retries = env_int("CAPTCHA_RETRIES", 1, 0, 2)
        self.failure_cache_s = env_int("FAILURE_CACHE_S", 90, 0, 3_600)
        self.max_page = env_int("MAX_PAGE", 2, 1, 2)
        self.google_hl = os.getenv("GOOGLE_HL", "zh-CN")
        self.google_gl = os.getenv("GOOGLE_GL", "us")
        self.google_udm = os.getenv("GOOGLE_UDM", "14")
        self.google_safe = os.getenv("GOOGLE_SAFE", "off")
        self.google_pws = os.getenv("GOOGLE_PWS", "0")
        self.google_as_eq = os.getenv("GOOGLE_AS_EQ", "").strip()
        self._playwright: Any = None
        self._browser: Any = None
        self._last_request_at = 0.0
        self._failed_until = 0.0

    @property
    def connected(self) -> bool:
        return bool(self._browser and self._browser.is_connected())

    def close(self) -> None:
        if self._playwright is not None:
            try:
                self._playwright.stop()
            except Exception:  # pragma: no cover - best-effort process cleanup
                pass
        self._playwright = None
        self._browser = None

    def build_url(self, query: str, pageno: int) -> str:
        params = {
            "q": query,
            "newwindow": "1",
            "pws": self.google_pws,
            "gl": self.google_gl,
            "hl": self.google_hl,
            "udm": self.google_udm,
            "gws_rd": "cr",
            "safe": self.google_safe,
            "start": str((pageno - 1) * 10),
        }
        if self.google_as_eq:
            params["as_eq"] = self.google_as_eq.replace("+", " ")
        return "https://www.google.com/search?" + urlencode(params)

    def _connect(self) -> Any:
        if self.connected:
            return self._browser
        if not self.endpoint:
            raise RuntimeError("CDP_ENDPOINT is not set")

        from playwright.sync_api import sync_playwright

        self.close()
        self._playwright = sync_playwright().start()
        headers = {"Authorization": f"Bearer {self.api_key}"} if self.api_key else None
        self._browser = self._playwright.chromium.connect_over_cdp(
            self.endpoint,
            headers=headers,
            timeout=30_000,
        )
        LOG.info("connected to CDP")
        return self._browser

    @staticmethod
    def _is_captcha(url: str, content: str) -> bool:
        content = content.lower()
        return (
            "/sorry" in url
            or "sorry.google.com" in url
            or "unusual traffic" in content
            or "g-recaptcha" in content
        )

    @staticmethod
    def _normalize(raw: list[dict[str, Any]] | None, limit: int) -> list[dict[str, str]]:
        results: list[dict[str, str]] = []
        seen: set[str] = set()
        for item in raw or []:
            if len(results) >= limit:
                break
            url = clean_google_url(item.get("href"))
            title = (item.get("title") or "").strip()
            if not url or not title or url in seen:
                continue
            seen.add(url)
            results.append(
                {"title": title, "url": url, "content": (item.get("snippet") or "").strip()}
            )
        return results

    def search(self, query: str, pageno: int, limit: int) -> dict[str, Any]:
        pageno = max(1, pageno)
        start = (pageno - 1) * 10
        base = {"query": query, "engine": "google", "pageno": pageno, "start": start}
        if pageno > self.max_page:
            return {**base, "results": [], "note": "max_page"}
        if self._failed_until > time.time():
            return {**base, "results": [], "note": "quarantined"}

        wait = self._last_request_at + self.min_interval_s - time.monotonic()
        if wait > 0:
            time.sleep(wait)
        self._last_request_at = time.monotonic()

        url = self.build_url(query, pageno)
        for attempt in range(self.captcha_retries + 1):
            page = None
            try:
                browser = self._connect()
                context = browser.contexts[0] if browser.contexts else browser.new_context(locale=self.google_hl)
                page = context.new_page()
                page.goto(url, wait_until="domcontentloaded", timeout=self.goto_timeout_ms)
                content = page.content()
                if self._is_captcha(page.url, content):
                    LOG.warning("Google CAPTCHA (%d/%d)", attempt + 1, self.captcha_retries + 1)
                    if attempt < self.captcha_retries:
                        time.sleep(2 * (attempt + 1))
                        continue
                    return {**base, "results": [], "note": "captcha"}
                try:
                    page.wait_for_selector("#search, #rso, #main", timeout=self.selector_timeout_ms)
                except Exception:
                    LOG.warning("Google results container was not found before timeout")
                return {**base, "results": self._normalize(page.evaluate(EXTRACT_JS), limit), "note": None}
            except Exception as exc:
                LOG.warning("Google request failed: %s", exc)
                self.close()
                if attempt < self.captcha_retries:
                    time.sleep(2 * (attempt + 1))
                    continue
                self._failed_until = time.time() + self.failure_cache_s
                return {**base, "results": [], "note": "error", "error": str(exc)}
            finally:
                if page is not None:
                    try:
                        page.close()
                    except Exception:
                        pass
        return {**base, "results": [], "note": "error"}


PROXY = GoogleProxy()


class Handler(BaseHTTPRequestHandler):
    server_version = "GoogleCDPProxy/1.0"

    def log_message(self, format: str, *args: object) -> None:
        LOG.info("%s - %s", self.address_string(), format % args)

    def _send_json(self, status: int, data: dict[str, Any]) -> None:
        body = json.dumps(data, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        parsed = urlparse(self.path)
        if parsed.path == "/health":
            self._send_json(200, {"ok": True, "browser": "connected" if PROXY.connected else "disconnected"})
            return
        if parsed.path != "/google":
            self._send_json(404, {"error": "not found"})
            return

        params = parse_qs(parsed.query)
        query = (params.get("q") or [""])[0].strip()
        if not query:
            self._send_json(400, {"error": "q is required"})
            return
        try:
            pageno = int((params.get("pageno") or ["1"])[0])
            limit = min(10, max(1, int((params.get("limit") or ["10"])[0])))
        except ValueError:
            self._send_json(400, {"error": "pageno and limit must be integers"})
            return
        self._send_json(200, PROXY.search(query, pageno, limit))


def main() -> None:
    logging.basicConfig(level=os.getenv("LOG_LEVEL", "INFO"), format="%(asctime)s %(levelname)s %(message)s")
    server = HTTPServer(("127.0.0.1", 3100), Handler)

    def stop(_signum: int, _frame: Any) -> None:
        # shutdown() must run outside serve_forever's main thread.
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        server.serve_forever()
    finally:
        server.server_close()
        PROXY.close()


if __name__ == "__main__":
    main()
