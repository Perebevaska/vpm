"""Минимальный локальный SOCKS5 CONNECT-прокси для теста --proxy пути.
Логирует каждую цель, релеит TCP. no-auth. Не для продакшена."""

import socket
import struct
import sys
import threading


def _recv_exact(s, n):
    b = bytearray()
    while len(b) < n:
        d = s.recv(n - len(b))
        if not d:
            raise ConnectionError("closed")
        b.extend(d)
    return bytes(b)


def serve_client(c: socket.socket):
    try:
        c.settimeout(15)
        ver, nmethods = _recv_exact(c, 2)
        _recv_exact(c, nmethods)                 # methods
        c.sendall(bytes([0x05, 0x00]))           # no-auth
        ver, cmd, rsv, atyp = _recv_exact(c, 4)
        if atyp == 0x01:
            host = socket.inet_ntoa(_recv_exact(c, 4))
        elif atyp == 0x03:
            ln = _recv_exact(c, 1)[0]
            host = _recv_exact(c, ln).decode()
        elif atyp == 0x04:
            host = socket.inet_ntop(socket.AF_INET6, _recv_exact(c, 16))
        else:
            c.close(); return
        port = struct.unpack(">H", _recv_exact(c, 2))[0]
        print(f"[socks5-proxy] CONNECT {host}:{port}", flush=True)

        try:
            up = socket.create_connection((host, port), timeout=15)
        except Exception:
            c.sendall(bytes([0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0]))
            c.close(); return
        c.sendall(bytes([0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0]))
        c.settimeout(None)

        def pipe(a, b):
            try:
                while True:
                    d = a.recv(65536)
                    if not d:
                        break
                    b.sendall(d)
            except Exception:
                pass
            finally:
                try: b.shutdown(socket.SHUT_WR)
                except Exception: pass
        t1 = threading.Thread(target=pipe, args=(c, up), daemon=True)
        t2 = threading.Thread(target=pipe, args=(up, c), daemon=True)
        t1.start(); t2.start(); t1.join(); t2.join()
        up.close()
    except Exception:
        pass
    finally:
        c.close()


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 1090
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", port))
    srv.listen(64)
    print(f"[socks5-proxy] listening on 127.0.0.1:{port}", flush=True)
    while True:
        c, _ = srv.accept()
        threading.Thread(target=serve_client, args=(c,), daemon=True).start()


if __name__ == "__main__":
    main()
