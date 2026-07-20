"""Серверная сторона: поллинг WebDAV на сессии клиента, приём yamux-потоков,
релей каждого в назначение через upstream SOCKS5 (или напрямую).

Роль зеркальна клиенту: пишем в s2c, читаем из c2s.
"""

from __future__ import annotations

import logging
import socket
import struct
import threading
import time

from . import socks_upstream, yamux
from .pipe import Pipe
from .webdav import WebDAV

log = logging.getLogger("wt.server")

STALE_SESSION_AGE = 90.0     # сессия без свежего heartbeat считается мёртвой
POLL_SESSIONS_EVERY = 3.0
DIAL_TIMEOUT = 15.0


class _PipeTransport:
    """Адаптер Pipe → транспорт yamux (read(n)->bytes / write(bytes))."""

    def __init__(self, pipe: Pipe):
        self.pipe = pipe

    def read(self, n: int) -> bytes:
        return self.pipe.read(n)

    def write(self, b: bytes) -> None:
        self.pipe.write(b)


class Server:
    def __init__(self, dav: WebDAV, key: bytes | None,
                 proxy: socks_upstream.ProxyConfig | None):
        self.dav = dav
        self.key = key
        self.proxy = proxy
        self._known: dict[str, float] = {}   # sid -> время закрытия (0 = активна)
        self._lock = threading.Lock()

    # ── главный цикл ─────────────────────────────────────────────────────────
    def run(self) -> None:
        try:
            self.dav.mkcol("tunnel")
        except Exception as e:
            log.warning("mkcol tunnel: %s", e)
        self._startup_cleanup()
        egress = f"SOCKS5 {self.proxy.host}:{self.proxy.port}" if self.proxy else "direct"
        log.info("server up, egress=%s, enc=%s", egress, bool(self.key))

        while True:
            try:
                sessions = self.dav.list_sessions()
            except Exception as e:
                log.warning("list sessions: %s", e)
                time.sleep(0.5)
                continue
            for sid in sessions:
                with self._lock:
                    if sid in self._known:
                        continue
                    self._known[sid] = 0.0
                threading.Thread(target=self._handle_session, args=(sid,),
                                 name=f"sess-{sid}", daemon=True).start()
            self._reap_known()
            time.sleep(POLL_SESSIONS_EVERY)

    def _reap_known(self) -> None:
        # закрытые сессии держим 5 мин (пока идёт удаление файлов на WebDAV)
        now = time.time()
        with self._lock:
            for sid, closed_at in list(self._known.items()):
                if closed_at and now - closed_at > 300:
                    del self._known[sid]

    def _startup_cleanup(self) -> None:
        """Снести протухшие сессии, оставшиеся от прошлого запуска."""
        try:
            hrefs = self.dav.propfind("tunnel", "1")
        except Exception:
            return
        for href in hrefs:
            sid = href.rstrip("/").rsplit("/", 1)[-1]
            if not sid or sid == "tunnel":
                continue
            try:
                self.dav.put(f"tunnel/{sid}/done", b"1")
                self.dav.delete(f"tunnel/{sid}/init")
                self.dav.delete(f"tunnel/{sid}")
            except Exception:
                pass

    # ── обработка одной сессии ───────────────────────────────────────────────
    def _handle_session(self, sid: str) -> None:
        try:
            age = self.dav.session_age(sid)
            if age > STALE_SESSION_AGE:
                log.info("[%s] stale (%.0fs), removing", sid, age)
                self.dav.delete(f"tunnel/{sid}/init")
                self.dav.delete(f"tunnel/{sid}")
                return

            # сигнал клиенту «сессию подхватил»
            self.dav.put(f"tunnel/{sid}/srv-hb", str(int(time.time())).encode())

            pipe = Pipe(self.dav, sid, write_dir="s2c", read_dir="c2s", key=self.key)
            pipe.start()
            sess = yamux.Session(_PipeTransport(pipe))
            log.info("[%s] session up", sid)

            while True:
                stream = sess.accept()
                if stream is None:
                    break
                threading.Thread(target=self._serve_stream, args=(sid, stream),
                                 name=f"strm-{sid}", daemon=True).start()

            sess.close()
            pipe.close()
        except Exception as e:
            log.warning("[%s] session error: %s", sid, e)
        finally:
            try:
                self.dav.delete(f"tunnel/{sid}")
            except Exception:
                pass
            with self._lock:
                if sid in self._known:
                    self._known[sid] = time.time()
            log.info("[%s] session closed", sid)

    def _serve_stream(self, sid: str, stream: yamux.Stream) -> None:
        try:
            host, port = _read_target(stream)
        except Exception:
            stream.close()
            return
        log.info("[%s] connect %s:%d", sid, host, port)
        try:
            if self.proxy:
                sock = socks_upstream.connect(self.proxy, host, port, DIAL_TIMEOUT)
            else:
                sock = socks_upstream.connect_direct(host, port, DIAL_TIMEOUT)
        except Exception as e:
            log.info("[%s] dial %s:%d failed: %s", sid, host, port, e)
            stream.close()
            return
        _relay(stream, sock)


def _read_target(stream: yamux.Stream) -> tuple[str, int]:
    """[2 BE host_len][host][2 BE port]."""
    hb = _read_exact(stream, 2)
    host_len = struct.unpack(">H", hb)[0]
    host = _read_exact(stream, host_len).decode()
    pb = _read_exact(stream, 2)
    port = struct.unpack(">H", pb)[0]
    return host, port


def _read_exact(stream: yamux.Stream, n: int) -> bytes:
    buf = bytearray()
    while len(buf) < n:
        d = stream.read(n - len(buf))
        if not d:
            raise ConnectionError("stream closed before target read")
        buf.extend(d)
    return bytes(buf)


def _relay(stream: yamux.Stream, sock: socket.socket) -> None:
    """Двунаправленный релей yamux-поток ↔ TCP-сокет."""
    def s2t():
        try:
            while True:
                d = stream.read(65536)
                if not d:
                    break
                sock.sendall(d)
        except Exception:
            pass
        finally:
            try:
                sock.shutdown(socket.SHUT_WR)
            except Exception:
                pass

    def t2s():
        try:
            while True:
                d = sock.recv(65536)
                if not d:
                    break
                stream.write(d)
        except Exception:
            pass
        finally:
            stream.close()

    a = threading.Thread(target=s2t, daemon=True)
    b = threading.Thread(target=t2s, daemon=True)
    a.start(); b.start()
    a.join(); b.join()
    try:
        sock.close()
    except Exception:
        pass
