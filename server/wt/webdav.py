"""Синхронный WebDAV-клиент на requests.

Ровно те методы, что нужны протоколу туннеля: PUT/GET/DELETE/MKCOL/PROPFIND/
OPTIONS плюс хелперы для сессий. Мимикрия под браузер и no-cache заголовки —
чтобы Яндекс/Cloudflare не отдавали устаревшие 404 и не резали трафик.
"""

from __future__ import annotations

import time
import xml.etree.ElementTree as ET
from dataclasses import dataclass

import requests

_UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
       "(KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
_NOCACHE = {"Cache-Control": "no-cache, no-store, must-revalidate", "Pragma": "no-cache"}


class RateLimited(Exception):
    """429 от хранилища. wait — сколько ждать до повтора (сек)."""

    def __init__(self, wait: float):
        super().__init__(f"rate limited, retry after {wait}s")
        self.wait = wait


def _retry_after(headers) -> float:
    ra = headers.get("Retry-After", "").strip()
    if not ra:
        return 5.0
    try:
        return float(int(ra))
    except ValueError:
        return 5.0


@dataclass
class GetResult:
    data: bytes
    status: int


class WebDAV:
    def __init__(self, base_url: str, login: str, password: str, timeout: float = 60.0):
        self.base = base_url.rstrip("/")
        self.timeout = timeout
        self.s = requests.Session()
        self.s.auth = (login, password)
        self.s.headers["User-Agent"] = _UA

    def _url(self, path: str) -> str:
        if not path:
            return self.base
        return self.base + "/" + path.lstrip("/")

    def put(self, path: str, data: bytes) -> None:
        r = self.s.request("PUT", self._url(path), data=data, timeout=self.timeout)
        if r.status_code == 429:
            raise RateLimited(_retry_after(r.headers))
        if r.status_code >= 400:
            raise RuntimeError(f"PUT {path}: {r.status_code}")

    def get(self, path: str) -> GetResult:
        r = self.s.request("GET", self._url(path), headers=_NOCACHE, timeout=self.timeout)
        if r.status_code == 429:
            raise RateLimited(_retry_after(r.headers))
        if r.status_code == 404:
            return GetResult(b"", 404)
        if r.status_code >= 400:
            raise RuntimeError(f"GET {path}: {r.status_code}")
        return GetResult(r.content, r.status_code)

    def delete(self, path: str) -> None:
        # 423 Locked: Яндекс кратко лочит папку при параллельных операциях —
        # короткий ретрай вместо фейла.
        for attempt in range(4):
            r = self.s.request("DELETE", self._url(path), timeout=self.timeout)
            if r.status_code == 429:
                raise RateLimited(_retry_after(r.headers))
            if r.status_code == 423 and attempt < 3:
                time.sleep(0.5 * (attempt + 1))
                continue
            if r.status_code >= 400 and r.status_code not in (404, 423):
                raise RuntimeError(f"DELETE {path}: {r.status_code}")
            return

    def mkcol(self, path: str) -> None:
        r = self.s.request("MKCOL", self._url(path), timeout=self.timeout)
        if r.status_code == 429:
            raise RateLimited(_retry_after(r.headers))
        # 405 = уже существует, 409 = нет родителя (best-effort, не фейлим)
        if r.status_code >= 400 and r.status_code not in (405, 409):
            raise RuntimeError(f"MKCOL {path}: {r.status_code}")

    def propfind(self, path: str, depth: str = "1") -> list[str]:
        if not path.endswith("/"):
            path += "/"  # без слэша Apache отдаёт 301 → GET → HTML-индекс
        body = ('<?xml version="1.0"?><D:propfind xmlns:D="DAV:">'
                '<D:prop><D:resourcetype/></D:prop></D:propfind>')
        headers = {"Depth": depth, "Content-Type": "application/xml", **_NOCACHE}
        r = self.s.request("PROPFIND", self._url(path), data=body,
                           headers=headers, timeout=self.timeout)
        if r.status_code == 429:
            raise RateLimited(_retry_after(r.headers))
        if r.status_code == 404:
            return []
        if r.status_code >= 400:
            raise RuntimeError(f"PROPFIND {path}: {r.status_code}")
        hrefs = []
        root = ET.fromstring(r.content)
        for href in root.iter("{DAV:}href"):
            if href.text:
                hrefs.append(href.text)
        return hrefs

    def ping(self) -> None:
        r = self.s.request("OPTIONS", self.base + "/", timeout=self.timeout)
        if r.status_code == 401:
            raise RuntimeError("authentication failed (401)")
        if r.status_code >= 400:
            raise RuntimeError(f"OPTIONS: {r.status_code}")

    # ── хелперы сессий ──────────────────────────────────────────────────────
    def list_sessions(self) -> list[str]:
        """sid'ы под tunnel/, у которых есть маркер init (сессия готова)."""
        try:
            hrefs = self.propfind("tunnel", "1")
        except RuntimeError:
            return []
        out = []
        for href in hrefs:
            sid = _last_segment(href)
            if not sid or sid == "tunnel":
                continue
            if self.get(f"tunnel/{sid}/init").status == 200:
                out.append(sid)
        return out

    def session_age(self, sid: str) -> float:
        """Секунды с последнего heartbeat клиента; -1 если hb нет/битый."""
        res = self.get(f"tunnel/{sid}/hb")
        if res.status != 200 or not res.data:
            return -1.0
        try:
            ts = int(res.data.decode().strip())
        except ValueError:
            return -1.0
        return time.time() - ts


def _last_segment(href: str) -> str:
    s = href.rstrip("/")
    i = s.rfind("/")
    return s[i + 1:] if i >= 0 else s
