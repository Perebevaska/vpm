#!/usr/bin/env python3
"""
Отдельный клиент Яндекс.Диска с асимметричным транспортом:

  upload   → REST API (cloud-api.yandex.net), OAuth-токен, PUT на presigned href
  download → WebDAV   (webdav.yandex.ru),     Basic-auth паролем приложения

Почему так: REST-загрузка делается одним presigned-PUT (меньше файловых
операций/churn, дружелюбнее к анти-абузу Диска на запись). Скачивание оставлено
на WebDAV — дешёвый потоковый GET, удобно поллить/стримить.

Зависимости: requests  (pip install requests)

Конфиг через переменные окружения:
  YD_OAUTH_TOKEN   OAuth-токен для REST API    (https://yandex.ru/dev/disk/poligon/)
  YD_WEBDAV_LOGIN  Яндекс-логин                (WebDAV Basic-auth)
  YD_WEBDAV_PASS   пароль приложения (WebDAV)  (Яндекс ID → Пароли приложений)

Примеры:
  python3 ydclient.py upload   ./local.bin  disk:/vpm/chunk-001.bin
  python3 ydclient.py download vpm/chunk-001.bin  ./out.bin
"""

from __future__ import annotations

import argparse
import os
import sys
import time
from urllib.parse import quote

import requests

# ── endpoints ────────────────────────────────────────────────────────────────
REST_BASE = "https://cloud-api.yandex.net/v1/disk"
WEBDAV_BASE = "https://webdav.yandex.ru"

CHUNK = 1 << 16  # 64 KiB потоковых чанков для чтения/записи
CONNECT_TIMEOUT = 10
READ_TIMEOUT = 60
RETRIES = 3
RETRY_BACKOFF = 1.5  # секунды, экспоненциально


# ── config ───────────────────────────────────────────────────────────────────
def _env(*names: str) -> str:
    """Первое непустое из перечисленных имён (поддержка чужих .env-схем)."""
    for name in names:
        val = os.environ.get(name)
        if val:
            return val
    joined = " / ".join(names)
    sys.exit(f"error: не задана ни одна из переменных окружения: {joined}")


# ── retry-обёртка ────────────────────────────────────────────────────────────
def _with_retries(fn, what: str):
    last = None
    for attempt in range(1, RETRIES + 1):
        try:
            return fn()
        except (requests.ConnectionError, requests.Timeout) as e:
            last = e
            if attempt < RETRIES:
                delay = RETRY_BACKOFF ** attempt
                print(f"warn: {what} попытка {attempt} упала ({e}); "
                      f"повтор через {delay:.1f}s", file=sys.stderr)
                time.sleep(delay)
    raise RuntimeError(f"{what}: исчерпаны {RETRIES} попытки") from last


# ── UPLOAD: REST API ─────────────────────────────────────────────────────────
def upload(local_path: str, remote_path: str) -> None:
    """
    Двухшаговая REST-загрузка:
      1. GET /resources/upload?path=... с OAuth → presigned href (метод PUT)
      2. PUT тела файла на href (href сам авторизован, доп. заголовки не нужны)
    remote_path: 'disk:/vpm/x.bin' или просто '/vpm/x.bin'.
    """
    token = _env("YD_OAUTH_TOKEN", "YANDEX_OAUTH_TOKEN")
    if not os.path.isfile(local_path):
        sys.exit(f"error: нет локального файла {local_path}")

    size = os.path.getsize(local_path)
    headers = {"Authorization": f"OAuth {token}"}

    # шаг 1 — получить ссылку для заливки
    def _get_href():
        r = requests.get(
            f"{REST_BASE}/resources/upload",
            headers=headers,
            params={"path": remote_path, "overwrite": "true"},
            timeout=(CONNECT_TIMEOUT, READ_TIMEOUT),
        )
        if r.status_code != 200:
            sys.exit(f"error: получение upload-href → HTTP {r.status_code}: {r.text}")
        return r.json()["href"]

    href = _with_retries(_get_href, "upload/href")

    # шаг 2 — PUT тела потоком (не грузим файл целиком в память)
    def _put():
        with open(local_path, "rb") as f:
            r = requests.put(
                href,
                data=f,
                headers={"Content-Length": str(size)},
                timeout=(CONNECT_TIMEOUT, None),  # запись большого тела без read-таймаута
            )
        # 201 Created / 202 Accepted — оба успех у Яндекса
        if r.status_code not in (201, 202):
            sys.exit(f"error: PUT тела → HTTP {r.status_code}: {r.text}")

    _with_retries(_put, "upload/put")
    print(f"ok: uploaded {local_path} ({size} B) → {remote_path}")


# ── DOWNLOAD: WebDAV ─────────────────────────────────────────────────────────
def download(remote_path: str, local_path: str) -> None:
    """
    Потоковый WebDAV GET с Basic-auth. remote_path — путь относительно корня
    Диска, например 'vpm/x.bin' (ведущий '/' и префикс 'disk:' срезаются).
    """
    login = _env("YD_WEBDAV_LOGIN", "WEBDAV_LOGIN")
    password = _env("YD_WEBDAV_PASS", "WEBDAV_PASSWORD")

    rel = remote_path.removeprefix("disk:").lstrip("/")
    url = f"{WEBDAV_BASE}/{quote(rel)}"

    def _get():
        r = requests.get(
            url,
            auth=(login, password),
            stream=True,
            timeout=(CONNECT_TIMEOUT, READ_TIMEOUT),
        )
        if r.status_code != 200:
            body = r.text[:200]
            sys.exit(f"error: WebDAV GET {rel} → HTTP {r.status_code}: {body}")
        written = 0
        tmp = local_path + ".part"
        with open(tmp, "wb") as f:
            for chunk in r.iter_content(CHUNK):
                if chunk:
                    f.write(chunk)
                    written += len(chunk)
        os.replace(tmp, local_path)  # атомарная финализация
        return written

    written = _with_retries(_get, "download/get")
    print(f"ok: downloaded {remote_path} ({written} B) → {local_path}")


# ── CLI ──────────────────────────────────────────────────────────────────────
def main() -> None:
    p = argparse.ArgumentParser(description="Яндекс.Диск: upload=REST, download=WebDAV")
    sub = p.add_subparsers(dest="cmd", required=True)

    up = sub.add_parser("upload", help="залить файл через REST API")
    up.add_argument("local", help="локальный файл")
    up.add_argument("remote", help="путь на Диске, напр. disk:/vpm/x.bin")

    dn = sub.add_parser("download", help="скачать файл через WebDAV")
    dn.add_argument("remote", help="путь на Диске, напр. vpm/x.bin")
    dn.add_argument("local", help="локальный файл назначения")

    args = p.parse_args()
    if args.cmd == "upload":
        upload(args.local, args.remote)
    elif args.cmd == "download":
        download(args.remote, args.local)


if __name__ == "__main__":
    main()
