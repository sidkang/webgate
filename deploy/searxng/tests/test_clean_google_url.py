"""Unit checks for clean_google_url scheme validation."""

from __future__ import annotations

import importlib.util
import pathlib
import sys
import unittest


def load_proxy():
    path = pathlib.Path(__file__).resolve().parents[1] / "container" / "proxy.py"
    spec = importlib.util.spec_from_file_location("google_cdp_proxy", path)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class CleanGoogleURLTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.proxy = load_proxy()

    def test_https_ok(self):
        self.assertEqual(
            self.proxy.clean_google_url("https://example.com/a"),
            "https://example.com/a",
        )

    def test_httpx_rejected(self):
        self.assertIsNone(self.proxy.clean_google_url("httpx://example.com/a"))

    def test_google_host_rejected(self):
        self.assertIsNone(self.proxy.clean_google_url("https://www.google.com/search"))

    def test_redirect_unwrap_rejects_bad_scheme(self):
        href = "/url?q=httpx://evil.example/path"
        self.assertIsNone(self.proxy.clean_google_url(href))


if __name__ == "__main__":
    unittest.main()
