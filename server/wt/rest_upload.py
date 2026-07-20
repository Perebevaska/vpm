"""Заливка чанков через Yandex Disk REST API (вместо WebDAV PUT).

Зачем: WebDAV Яндекса троттлит запись сильнее, чем REST-API. Серверный write-путь
(s2c) — наш код, поэтому его можно лить по REST. Файл ложится по тому же пути на
Диске, и WireTurn читает его обычным WebDAV-GET — wire-совместимость сохраняется.

Двухшаговый REST-upload: GET /resources/upload (OAuth) → presigned href → PUT тела.
Интерфейс .put(path, data) совместим с WebDAV.put, чтобы Pipe использовал их
взаимозаменяемо.
"""

from __future__ import annotations

import requests

from .webdav import RateLimited, _retry_after

REST_BASE = "https://cloud-api.yandex.net/v1/disk"


class RestUploader:
    def __init__(self, oauth_token: str, timeout: float = 60.0):
        self.timeout = timeout
        self.s = requests.Session()
        self.s.headers["Authorization"] = f"OAuth {oauth_token}"

    def put(self, path: str, data: bytes) -> None:
        """Залить data по пути path (относительно корня Диска), overwrite."""
        disk_path = "/" + path.lstrip("/")

        # шаг 1 — presigned href
        r = self.s.get(f"{REST_BASE}/resources/upload",
                       params={"path": disk_path, "overwrite": "true"},
                       timeout=self.timeout)
        if r.status_code == 429:
            raise RateLimited(_retry_after(r.headers))
        if r.status_code != 200:
            raise RuntimeError(f"REST upload-href {path}: {r.status_code}")
        href = r.json()["href"]

        # шаг 2 — PUT тела (href уже авторизован, доп. заголовки не нужны)
        r2 = requests.put(href, data=data, timeout=self.timeout)
        if r2.status_code == 429:
            raise RateLimited(_retry_after(r2.headers))
        if r2.status_code not in (201, 202):
            raise RuntimeError(f"REST PUT {path}: {r2.status_code}")
