"""Минимальный yamux (серверная сторона) поверх байт-пайпа.

Достаточно для интеропа с hashicorp/yamux-клиентом (его использует WireTurn):
фреймы, установка потока по SYN с ответным ACK, flow-control окна, ответ на
keepalive-Ping, half-close по FIN, сброс по RST.

Кадр (12 байт заголовка), всё big-endian:
  ver(1)=0 | type(1) | flags(2) | streamID(4) | length(4)
type:  0=DATA  1=WINDOW_UPDATE  2=PING  3=GO_AWAY
flags: 0x1=SYN 0x2=ACK 0x4=FIN 0x8=RST

Модель потоков (threads):
  recv-loop      — читает кадры из пайпа, диспатчит.
  Stream.read()  — блокирующее чтение принятых DATA; шлёт WINDOW_UPDATE по мере
                   потребления, возвращая пиру кредиты (backpressure).
  Stream.write() — блокирует, пока send-окно 0; шлёт DATA кадрами <= MAX_FRAME.
"""

from __future__ import annotations

import struct
import threading
import queue

# типы
DATA = 0x0
WINDOW_UPDATE = 0x1
PING = 0x2
GO_AWAY = 0x3

# флаги
SYN = 0x1
ACK = 0x2
FIN = 0x4
RST = 0x8

VERSION = 0
HEADER = struct.Struct(">BBHII")  # ver,type,flags,streamID,length

INITIAL_WINDOW = 256 * 1024   # стартовое окно потока (спека yamux)
MAX_FRAME = 16 * 1024         # максимум данных в одном DATA-кадре
WU_THRESHOLD = INITIAL_WINDOW // 2  # порог отправки WINDOW_UPDATE

# коды GO_AWAY
GOAWAY_NORMAL = 0


class Stream:
    def __init__(self, session: "Session", sid: int):
        self.session = session
        self.id = sid

        self._recv_buf = bytearray()
        self._recv_cv = threading.Condition()
        self._recv_closed = False       # получен FIN от пира
        self._reset = False             # RST
        self._consumed_pending = 0      # байт прочитано, но кредит ещё не возвращён

        self._send_window = INITIAL_WINDOW
        self._send_cv = threading.Condition()
        self._local_closed = False      # мы отправили FIN
        self._sent_first = False        # был ли отправлен первый кадр (для ACK)
        self._send_lock = threading.Lock()

    # ── приём (вызывается recv-loop) ────────────────────────────────────────
    def _recv_data(self, data: bytes) -> None:
        with self._recv_cv:
            self._recv_buf.extend(data)
            self._recv_cv.notify_all()

    def _recv_fin(self) -> None:
        with self._recv_cv:
            self._recv_closed = True
            self._recv_cv.notify_all()

    def _recv_reset(self) -> None:
        with self._recv_cv:
            self._reset = True
            self._recv_cv.notify_all()
        with self._send_cv:
            self._send_cv.notify_all()

    def _grow_send_window(self, delta: int) -> None:
        with self._send_cv:
            self._send_window += delta
            self._send_cv.notify_all()

    # ── сокет-подобный интерфейс для релея ──────────────────────────────────
    def read(self, n: int = 65536) -> bytes:
        with self._recv_cv:
            while (not self._recv_buf and not self._recv_closed
                   and not self._reset and not self.session.closed):
                self._recv_cv.wait(1.0)
            if self._recv_buf:
                take = min(n, len(self._recv_buf))
                out = bytes(self._recv_buf[:take])
                del self._recv_buf[:take]
                self._consumed_pending += take
                delta = self._consumed_pending if self._consumed_pending >= WU_THRESHOLD else 0
                if delta:
                    self._consumed_pending = 0
            else:
                return b""  # EOF (FIN/RST/сессия закрыта)
        if delta:
            # вернуть пиру кредит окна вне lock'а, чтобы не гонять пайп под recv_cv
            self.session._send(WINDOW_UPDATE, 0, self.id, delta)
        return out

    def write(self, data: bytes) -> None:
        mv = memoryview(data)
        off = 0
        while off < len(mv):
            with self._send_cv:
                while (self._send_window == 0 and not self._reset
                       and not self.session.closed):
                    self._send_cv.wait(1.0)
                if self._reset or self.session.closed:
                    raise BrokenPipeError("stream reset/closed")
                n = min(len(mv) - off, self._send_window, MAX_FRAME)
                self._send_window -= n
            flags = self._first_flags()
            self.session._send(DATA, flags, self.id, n, mv[off:off + n])
            off += n

    def close(self) -> None:
        with self._send_lock:
            if self._local_closed:
                return
            self._local_closed = True
        flags = self._first_flags() | FIN
        try:
            self.session._send(DATA, flags, self.id, 0)
        except Exception:
            pass
        self.session._remove_stream(self.id)

    def _first_flags(self) -> int:
        # серверная сторона подтверждает поток ACK'ом на первом исходящем кадре
        with self._send_lock:
            if not self._sent_first:
                self._sent_first = True
                return ACK
        return 0


class Session:
    """Серверная yamux-сессия поверх транспорта с read(n)->bytes / write(bytes)."""

    def __init__(self, transport):
        self.t = transport
        self.closed = False
        self._streams: dict[int, Stream] = {}
        self._lock = threading.Lock()
        self._write_lock = threading.Lock()
        self._accept_q: "queue.Queue[Stream | None]" = queue.Queue()
        self._rbuf = bytearray()   # буфер для read_exact поверх пайпа

        self._recv_thread = threading.Thread(target=self._recv_loop,
                                              name="yamux-recv", daemon=True)
        self._recv_thread.start()

    # ── приём кадров ────────────────────────────────────────────────────────
    def _read_exact(self, n: int) -> bytes | None:
        while len(self._rbuf) < n:
            chunk = self.t.read(n - len(self._rbuf))
            if not chunk:
                return None  # транспорт закрыт/idle
            self._rbuf.extend(chunk)
        out = bytes(self._rbuf[:n])
        del self._rbuf[:n]
        return out

    def _recv_loop(self) -> None:
        try:
            while not self.closed:
                hdr = self._read_exact(HEADER.size)
                if hdr is None:
                    break
                ver, typ, flags, sid, length = HEADER.unpack(hdr)
                if ver != VERSION:
                    break
                if typ == DATA:
                    body = self._read_exact(length) if length else b""
                    if length and body is None:
                        break
                    self._on_data(flags, sid, body)
                elif typ == WINDOW_UPDATE:
                    self._on_window_update(flags, sid, length)
                elif typ == PING:
                    if flags & SYN:  # keepalive-пинг → отвечаем ACK с тем же opaque
                        self._send(PING, ACK, 0, length)
                elif typ == GO_AWAY:
                    break
                # неизвестные типы игнорируем
        finally:
            self._shutdown()

    def _on_data(self, flags: int, sid: int, body: bytes) -> None:
        stream = self._ensure_stream(flags, sid)
        if stream is None:
            return
        if body:
            stream._recv_data(body)
        if flags & RST:
            stream._recv_reset()
        elif flags & FIN:
            stream._recv_fin()

    def _on_window_update(self, flags: int, sid: int, delta: int) -> None:
        stream = self._ensure_stream(flags, sid)
        if stream is None:
            return
        if delta:
            stream._grow_send_window(delta)
        if flags & RST:
            stream._recv_reset()
        elif flags & FIN:
            stream._recv_fin()

    def _ensure_stream(self, flags: int, sid: int) -> Stream | None:
        with self._lock:
            stream = self._streams.get(sid)
            if stream is not None:
                return stream
            if not (flags & SYN):
                return None  # кадр для неизвестного/закрытого потока — дроп
            stream = Stream(self, sid)
            self._streams[sid] = stream
        # подтверждаем установку потока: WINDOW_UPDATE с ACK (delta=0)
        self._send(WINDOW_UPDATE, ACK, sid, 0)
        stream._sent_first = True
        self._accept_q.put(stream)
        return stream

    # ── отправка кадров ─────────────────────────────────────────────────────
    def _send(self, typ: int, flags: int, sid: int, length: int, body=b"") -> None:
        if self.closed:
            raise BrokenPipeError("session closed")
        hdr = HEADER.pack(VERSION, typ, flags, sid, length)
        with self._write_lock:
            self.t.write(hdr)
            if body:
                self.t.write(bytes(body))

    # ── публичный серверный API ─────────────────────────────────────────────
    def accept(self) -> Stream | None:
        """Следующий входящий поток; None когда сессия закрыта."""
        s = self._accept_q.get()
        return s

    def _remove_stream(self, sid: int) -> None:
        with self._lock:
            self._streams.pop(sid, None)

    def close(self) -> None:
        if self.closed:
            return
        try:
            self._send(GO_AWAY, 0, 0, GOAWAY_NORMAL)
        except Exception:
            pass
        self._shutdown()

    def _shutdown(self) -> None:
        if self.closed:
            return
        self.closed = True
        with self._lock:
            streams = list(self._streams.values())
            self._streams.clear()
        for s in streams:
            s._recv_reset()
        self._accept_q.put(None)  # разбудить accept()
