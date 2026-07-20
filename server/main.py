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


def client_uri(webdav_url: str, login: str, password: str, enc: bool) -> str:
    """URI для импорта в WireTurn: webdav(s)://login:pass@host?tuning[&enc=1].

    Тюнинг = дефолты мобильного клиента (см. docs/android.md webdav-tunnel),
    чтобы клиент и наш сервер поллили согласованно.
    """
    u = urlparse(webdav_url)
    scheme = "webdavs" if u.scheme == "https" else "webdav"
    host = u.netloc
    params = ("chunk-size=131071&coalesce=10ms&poll-max=500ms&poll-min=200ms"
              "&puts=8&read-max=8&read-min=3")
    if enc:
        params += "&enc=1"
    return f"{scheme}://{quote(login)}:{quote(password)}@{host}?{params}"


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

    uri = client_uri(webdav_url, login, password, args.enc)
    print("=" * 68)
    print("WireTurn client URI (импортируй в приложение — содержит пароль!):")
    print("  " + uri)
    print("=" * 68, flush=True)

    Server(dav, key, proxy).run()


if __name__ == "__main__":
    main()
