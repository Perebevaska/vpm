"""Тестовый эхо-сервер: yamux.Session (наш, серверная сторона) поверх TCP.
Принимает один поток, эхоит все данные обратно до FIN."""

import socket
import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from wt import yamux


class SockTransport:
    def __init__(self, s: socket.socket):
        self.s = s

    def read(self, n: int) -> bytes:
        return self.s.recv(n)

    def write(self, b: bytes) -> None:
        self.s.sendall(b)


def main():
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", 7777))
    srv.listen(1)
    print("echo server on 127.0.0.1:7777", flush=True)
    conn, _ = srv.accept()
    conn.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
    sess = yamux.Session(SockTransport(conn))
    st = sess.accept()
    if st is None:
        print("no stream")
        return
    total = 0
    while True:
        d = st.read(65536)
        if not d:
            break
        st.write(d)
        total += len(d)
    print(f"echoed {total} bytes", flush=True)
    st.close()
    sess.close()
    conn.close()
    srv.close()


if __name__ == "__main__":
    main()
