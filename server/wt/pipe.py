"""Дуплексный байт-канал поверх нумерованных чанк-файлов WebDAV.

Пайп выглядит для верхнего слоя (yamux) как блокирующий сокет:
  read(n)  -> bytes   (b"" на EOF)
  write(b) -> None
  close()

Внутри:
  writer  — коалесцирует исходящие байты, режет на чанки <= CHUNK_DATA_SIZE,
            добавляет заголовок [header|ts], шифрует, PUT в write_dir/<seq>.bin.
            seq'ы строго возрастающие и непрерывные (иначе читатель встанет).
  reader  — тянет read_dir/<seq>.bin по порядку с read-ahead окном, расшифровывает,
            снимает заголовок, отдаёт данные в inbound-буфер строго по порядку.
"""

from __future__ import annotations

import itertools
import struct
import threading
import time
from concurrent.futures import ThreadPoolExecutor

from . import crypto
from .webdav import RateLimited, WebDAV

CHUNK_DATA_SIZE = 128 * 1024 - 1  # 131071

HEADER_DATA = 0x00
HEADER_EOF = 0x01

COALESCE_DELAY = 0.010      # окно склейки записи
POLL_MIN = 0.05            # старт адаптивного бэкоффа чтения
POLL_MAX = 0.5            # потолок бэкоффа при простое
PUT_WORKERS = 8            # параллельные PUT
READ_AHEAD = 8            # параллельные GET (окно чтения)
IDLE_TIMEOUT = 90.0
PUT_MAX_ATTEMPTS = 15


def _chunk_path(sid: str, direction: str, seq: int) -> str:
    return f"tunnel/{sid}/{direction}/{seq:010d}.bin"


class Pipe:
    def __init__(self, dav: WebDAV, sid: str, write_dir: str, read_dir: str,
                 key: bytes | None, writer=None):
        self.dav = dav
        self.sid = sid
        self.write_dir = write_dir
        self.read_dir = read_dir
        self.key = key
        # writer.put(path, data) для аплоада чанков; по умолчанию — WebDAV PUT.
        # Сервер может подставить REST-аплоадер (меньше троттлинга на запись).
        self._writer = writer or dav

        self._closed = threading.Event()
        self._finish = threading.Event()   # запрос финального флаша+EOF
        self._write_seq = itertools.count(1)

        # inbound: упорядоченные данные для read()
        self._in_buf = bytearray()
        self._in_cv = threading.Condition()
        self._in_eof = False

        # outbound coalescing
        self._out_buf = bytearray()
        self._out_lock = threading.Lock()
        self._out_flushed_eof = False

        self._put_pool = ThreadPoolExecutor(max_workers=PUT_WORKERS,
                                            thread_name_prefix=f"put-{sid}")
        self._get_pool = ThreadPoolExecutor(max_workers=READ_AHEAD,
                                            thread_name_prefix=f"get-{sid}")
        self._pending_puts: list = []
        self._puts_lock = threading.Lock()

        self._reader_thread = threading.Thread(
            target=self._run_reader, name=f"reader-{sid}", daemon=True)
        self._flusher_thread = threading.Thread(
            target=self._run_flusher, name=f"flusher-{sid}", daemon=True)
        self._started = False

    # ── публичный сокет-подобный интерфейс ──────────────────────────────────
    def start(self) -> None:
        if self._started:
            return
        self._started = True
        self._reader_thread.start()
        self._flusher_thread.start()

    def read(self, n: int) -> bytes:
        """До n байт входного потока; b"" на EOF. Блокирует до данных/EOF/idle."""
        deadline = time.monotonic() + IDLE_TIMEOUT
        with self._in_cv:
            while not self._in_buf and not self._in_eof and not self._closed.is_set():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return b""  # idle timeout
                self._in_cv.wait(remaining)
            if self._in_buf:
                take = min(n, len(self._in_buf))
                out = bytes(self._in_buf[:take])
                del self._in_buf[:take]
                return out
            return b""  # EOF или закрыт

    def write(self, data: bytes) -> None:
        if self._finish.is_set():
            raise BrokenPipeError("pipe closed")
        with self._out_lock:
            self._out_buf.extend(data)

    def close(self) -> None:
        if self._finish.is_set():
            return
        # Сигналим flusher'у финализироваться: он один эмитит финальные чанки + EOF,
        # поэтому seq'ы остаются непрерывными и EOF гарантированно последний.
        self._finish.set()
        if self._started:
            self._flusher_thread.join(timeout=20)
        # дождаться завершения PUT-ов (включая EOF — доставлен приёмнику)
        with self._puts_lock:
            pending = list(self._pending_puts)
        for f in pending:
            try:
                f.result(timeout=15)
            except Exception:
                pass
        self._closed.set()
        with self._in_cv:
            self._in_cv.notify_all()
        self._put_pool.shutdown(wait=False)
        self._get_pool.shutdown(wait=False)

    def is_closed(self) -> bool:
        return self._closed.is_set()

    # ── writer ──────────────────────────────────────────────────────────────
    def _run_flusher(self) -> None:
        # Единственный поток-эмиттер: пока не финализируемся — шлём полные чанки;
        # по _finish — финальный форс-флаш хвоста и EOF, затем выходим.
        while not self._finish.is_set():
            self._finish.wait(COALESCE_DELAY)
            if self._finish.is_set():
                break
            # по тику склейки шлём ВСЁ накопленное (включая неполный хвост),
            # иначе трейлинг < CHUNK_DATA_SIZE завис бы до close()
            self._flush(force=True, send_eof=False)
        self._flush(force=True, send_eof=True)

    def _flush(self, force: bool, send_eof: bool) -> None:
        """Нарезать out_buf на чанки и запустить PUT. force=True шлёт неполный хвост."""
        while True:
            with self._out_lock:
                have = len(self._out_buf)
                if have == 0:
                    break
                if not force and have < CHUNK_DATA_SIZE:
                    break
                take = min(have, CHUNK_DATA_SIZE)
                data = bytes(self._out_buf[:take])
                del self._out_buf[:take]
            self._emit_chunk(HEADER_DATA, data)
        if send_eof and not self._out_flushed_eof:
            self._out_flushed_eof = True
            self._emit_chunk(HEADER_EOF, b"")

    def _emit_chunk(self, header: int, data: bytes) -> None:
        seq = next(self._write_seq)
        payload = struct.pack(">BQ", header, time.time_ns()) + data
        fut = self._put_pool.submit(self._put_chunk, seq, payload)
        with self._puts_lock:
            self._pending_puts.append(fut)

    def _put_chunk(self, seq: int, payload: bytes) -> None:
        body = crypto.encrypt_chunk(self.key, payload) if self.key else payload
        path = _chunk_path(self.sid, self.write_dir, seq)
        backoff = 0.5
        attempts = 0
        while not self._closed.is_set():
            try:
                self._writer.put(path, body)
                return
            except RateLimited as e:
                time.sleep(e.wait)
                continue
            except Exception:
                attempts += 1
                if attempts >= PUT_MAX_ATTEMPTS:
                    # безнадёжно — рвём пайп, иначе читатель встанет на этом seq
                    self.close()
                    return
                time.sleep(backoff)
                backoff = min(backoff * 1.5, 10.0)

    # ── reader ──────────────────────────────────────────────────────────────
    def _run_reader(self) -> None:
        counter = itertools.count(1)
        next_deliver = 1
        inflight: dict[int, "object"] = {}   # seq -> future
        results: dict[int, bytes] = {}

        def launch():
            seq = next(counter)
            inflight[seq] = self._get_pool.submit(self._fetch_chunk, seq)

        for _ in range(READ_AHEAD):
            launch()

        while not self._closed.is_set():
            # собрать готовые future
            done_any = False
            for seq in list(inflight.keys()):
                fut = inflight[seq]
                if fut.done():
                    del inflight[seq]
                    try:
                        payload = fut.result()
                    except Exception:
                        self._set_eof()
                        return
                    if payload is None:   # пайп закрыт во время fetch
                        self._set_eof()
                        return
                    results[seq] = payload
                    launch()  # держим окно заполненным
                    done_any = True

            # доставить всё, что идёт подряд от next_deliver
            while next_deliver in results:
                payload = results.pop(next_deliver)
                header = payload[0]
                if header == HEADER_EOF:
                    self._set_eof()
                    return
                self._deliver(payload[9:])
                next_deliver += 1

            if not done_any:
                time.sleep(0.005)

    def _fetch_chunk(self, seq: int) -> bytes | None:
        """Тянуть один чанк по порядку; None если пайп закрыт. Поллит до появления."""
        path = _chunk_path(self.sid, self.read_dir, seq)
        backoff = POLL_MIN
        while not self._closed.is_set():
            try:
                res = self.dav.get(path)
            except RateLimited as e:
                time.sleep(e.wait)
                continue
            except Exception:
                time.sleep(backoff)
                backoff = min(backoff * 2, POLL_MAX)
                continue

            need_retry = res.status == 404
            payload = res.data
            if not need_retry:
                if self.key:
                    try:
                        payload = crypto.decrypt_chunk(self.key, res.data)
                    except Exception:
                        need_retry = True  # ещё дозаписывается / битый
                if not need_retry and len(payload) < 9:
                    need_retry = True

            if need_retry:
                time.sleep(backoff)
                backoff = min(backoff * 2, POLL_MAX)
                continue

            # удалить чанк после чтения (лучшее усилие)
            try:
                self.dav.delete(path)
            except Exception:
                pass
            return payload
        return None

    def _deliver(self, data: bytes) -> None:
        if not data:
            return
        with self._in_cv:
            self._in_buf.extend(data)
            self._in_cv.notify_all()

    def _set_eof(self) -> None:
        with self._in_cv:
            self._in_eof = True
            self._in_cv.notify_all()

    # ── контрольные файлы ───────────────────────────────────────────────────
    def signal_done(self) -> None:
        try:
            self.dav.put(f"tunnel/{self.sid}/done", b"1")
        except Exception:
            pass

    def cleanup(self) -> None:
        try:
            self.dav.delete(f"tunnel/{self.sid}")
        except Exception:
            pass
