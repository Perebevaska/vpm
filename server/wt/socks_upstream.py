"""Минимальный SOCKS5-клиент для egress'а сервера в цепочку (bypass-in).

Только команда CONNECT. Адрес назначения шлём как domain (ATYP=0x03) — резолв
имени делает upstream (цепочка), а не наша машина. Поддержка no-auth и
username/password (RFC 1929).
"""

from __future__ import annotations

import socket
import struct
from dataclasses import dataclass
from urllib.parse import urlparse


@dataclass
class ProxyConfig:
    host: str
    port: int
    user: str = ""
    password: str = ""

    @classmethod
    def parse(cls, url: str) -> "ProxyConfig":
        u = urlparse(url)
        if u.scheme != "socks5" or not u.hostname or not u.port:
            raise ValueError("proxy must be socks5://[user:pass@]host:port")
        return cls(u.hostname, u.port, u.username or "", u.password or "")


_REPLY = {
    0x00: "succeeded", 0x01: "general failure", 0x02: "not allowed",
    0x03: "network unreachable", 0x04: "host unreachable",
    0x05: "connection refused", 0x06: "TTL expired",
    0x07: "command not supported", 0x08: "address type not supported",
}


def _recv_exact(s: socket.socket, n: int) -> bytes:
    buf = bytearray()
    while len(buf) < n:
        chunk = s.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("socks proxy closed during handshake")
        buf.extend(chunk)
    return bytes(buf)


def connect(proxy: ProxyConfig, dst_host: str, dst_port: int,
            timeout: float = 15.0) -> socket.socket:
    s = socket.create_connection((proxy.host, proxy.port), timeout=timeout)
    try:
        s.settimeout(timeout)
        # greeting: предлагаем no-auth и, если есть креды, user/pass
        methods = [0x00]
        if proxy.user:
            methods.append(0x02)
        s.sendall(bytes([0x05, len(methods)]) + bytes(methods))
        ver, method = _recv_exact(s, 2)
        if ver != 0x05:
            raise ConnectionError("bad socks version in greeting")

        if method == 0x02:
            u = proxy.user.encode()
            p = proxy.password.encode()
            s.sendall(bytes([0x01, len(u)]) + u + bytes([len(p)]) + p)
            _, status = _recv_exact(s, 2)
            if status != 0x00:
                raise ConnectionError("socks auth failed")
        elif method != 0x00:
            raise ConnectionError(f"socks: no acceptable auth method (0x{method:02x})")

        # CONNECT, domain ATYP → резолв на стороне цепочки
        host = dst_host.encode()
        if len(host) > 255:
            raise ValueError("hostname too long for SOCKS5")
        req = bytes([0x05, 0x01, 0x00, 0x03, len(host)]) + host + struct.pack(">H", dst_port)
        s.sendall(req)

        ver, rep, _rsv, atyp = _recv_exact(s, 4)
        if ver != 0x05:
            raise ConnectionError("bad socks version in reply")
        if rep != 0x00:
            raise ConnectionError(f"socks connect failed: {_REPLY.get(rep, rep)}")
        # дочитать BND.ADDR/PORT по типу адреса
        if atyp == 0x01:
            _recv_exact(s, 4 + 2)
        elif atyp == 0x04:
            _recv_exact(s, 16 + 2)
        elif atyp == 0x03:
            ln = _recv_exact(s, 1)[0]
            _recv_exact(s, ln + 2)
        else:
            raise ConnectionError(f"socks: bad ATYP in reply (0x{atyp:02x})")

        s.settimeout(None)
        return s
    except Exception:
        s.close()
        raise


def connect_direct(dst_host: str, dst_port: int, timeout: float = 15.0) -> socket.socket:
    """Прямой egress (без цепочки) — fallback, когда -proxy не задан."""
    s = socket.create_connection((dst_host, dst_port), timeout=timeout)
    s.settimeout(None)
    return s
