#!/usr/bin/env python3
"""Свой WireTurn-совместимый сервер поверх WebDAV (Яндекс.Диск).

Egress по умолчанию — upstream SOCKS5 (в цепочку bypass-in), при отсутствии
--proxy выпускает напрямую.

Креды берутся из окружения (совместимо со схемой webdav-tunnel):
  WEBDAV_URL       база WebDAV, напр. https://webdav.yandex.ru
  WEBDAV_LOGIN     логин
  WEBDAV_PASSWORD  пароль приложения

Пример:
  WEBDAV_URL=https://webdav.yandex.ru WEBDAV_LOGIN=... WEBDAV_PASSWORD=... \\
  python3 main.py --enc --proxy socks5://192.168.1.1:10800
"""

from __future__ import annotations

import argparse
import logging
import os
import sys
from urllib.parse import quote, urlparse

from wt import crypto, socks_upstream
from wt.server import Server
from wt.webdav import WebDAV


def client_uri(webdav_url: str, login: str, password: str, enc: bool,
               name: str = "vpm") -> str:
    """WebDAV-URL для профиля WireTurn (тип подключения = WebDAV, НЕ turnable/olcRTC).

    Формат по docs/generate_profiles.md WireTurn:
      webdavs://user:pass@host?timeout=60s&poll-min=200ms&poll-max=500ms#name
    enc=1 добавляется при --enc (нижележащий tunnel-lib парсит его из URL).
    """
    u = urlparse(webdav_url)
    scheme = "webdavs" if u.scheme == "https" else "webdav"
    host = u.netloc
    params = "timeout=60s&poll-min=200ms&poll-max=500ms"
    if enc:
        params += "&enc=1"
    return f"{scheme}://{quote(login)}:{quote(password)}@{host}?{params}#{name}"


def _env(*names: str) -> str:
    for n in names:
        v = os.environ.get(n)
        if v:
            return v
    sys.exit(f"error: задайте одну из переменных окружения: {' / '.join(names)}")


def main() -> None:
    p = argparse.ArgumentParser(description="WireTurn-совместимый WebDAV-туннель (сервер)")
    p.add_argument("--webdav", default=os.environ.get("WEBDAV_URL", ""),
                   help="база WebDAV (или env WEBDAV_URL)")
    p.add_argument("--enc", action="store_true",
                   help="AES-256-GCM (ключ из пароля) — должно совпадать с клиентом")
    p.add_argument("--proxy", default="",
                   help="upstream SOCKS5 egress: socks5://[user:pass@]host:port")
    p.add_argument("--rest-upload", action="store_true",
                   help="лить s2c-чанки через Yandex REST API (нужен YANDEX_OAUTH_TOKEN); "
                        "download остаётся WebDAV. Меньше троттлинга на запись.")
    p.add_argument("-v", "--verbose", action="store_true")
    args = p.parse_args()

    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        datefmt="%H:%M:%S")

    webdav_url = args.webdav or _env("WEBDAV_URL")
    login = _env("WEBDAV_LOGIN")
    password = _env("WEBDAV_PASSWORD")

    dav = WebDAV(webdav_url, login, password)
    try:
        dav.ping()
    except Exception as e:
        sys.exit(f"error: WebDAV недоступен: {e}")

    key = crypto.derive_key(password) if args.enc else None
    proxy = socks_upstream.ProxyConfig.parse(args.proxy) if args.proxy else None

    writer = None
    if args.rest_upload:
        from wt.rest_upload import RestUploader
        token = _env("YANDEX_OAUTH_TOKEN")
        writer = RestUploader(token)

    uri = client_uri(webdav_url, login, password, args.enc)
    print("=" * 68)
    print("WireTurn client URI (импортируй в приложение — содержит пароль!):")
    print("  " + uri)
    print("=" * 68, flush=True)

    Server(dav, key, proxy, writer).run()


if __name__ == "__main__":
    main()
