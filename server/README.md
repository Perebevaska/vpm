# wt — свой WireTurn-совместимый сервер (WebDAV-туннель)

Серверная сторона канала vpm MVP1, **написанная с нуля** (не форк
`webdav-tunnel`). Wire-совместима с клиентом WireTurn: обе стороны общаются
только через общее WebDAV-хранилище (Яндекс.Диск), координируясь нумерованными
чанк-файлами. Спецификация протокола — в [PROTOCOL.md](PROTOCOL.md).

Egress по умолчанию — **upstream SOCKS5** (в цепочку `bypass-in` → vless-reality),
так что exit = цепочка, а не домашний IP. Без `--proxy` выпускает напрямую.

## Слои

| Модуль | Назначение |
|--------|-----------|
| `wt/crypto.py` | AES-256-GCM чанков, ключ из пароля (`SHA256("webdav-tunnel-v1:"+pw)`) |
| `wt/webdav.py` | Синхронный WebDAV-клиент (requests) |
| `wt/pipe.py` | Дуплексный байт-канал: coalesce-запись, read-ahead чтение, порядок, EOF |
| `wt/yamux.py` | yamux server с нуля: кадры, SYN/ACK/FIN/RST, flow-control, keepalive |
| `wt/socks_upstream.py` | SOCKS5-клиент для egress'а в цепочку |
| `wt/server.py` | Поллинг сессий, приём потоков, релей |
| `main.py` | CLI + печать client URI для WireTurn |

## Зависимости

```
requests, cryptography   # обе обычно уже есть; иначе: pip install requests cryptography
```

## Запуск

Креды — из окружения (схема совместима с `webdav-tunnel/.env`):

```sh
export WEBDAV_URL=https://webdav.yandex.ru
export WEBDAV_LOGIN=твой_яндекс_логин
export WEBDAV_PASSWORD=пароль_приложения      # Яндекс ID → Пароли приложений (WebDAV)

python3 main.py --enc --proxy socks5://192.168.1.1:10800
```

Флаги:
- `--enc` — AES-256-GCM (обязано совпадать с клиентом).
- `--proxy socks5://[user:pass@]host:port` — upstream egress; без него — напрямую.
- `-v` — debug-лог.

При старте печатается **client URI** — импортируй его в WireTurn (в нём логин,
пароль, тюнинг и `enc=1`). URI содержит пароль — храни как секрет.

## Подключение WireTurn

1. Запусти сервер (см. выше) — скопируй напечатанный `webdavs://…`.
2. WireTurn → новый профиль → тип WebDAV → импорт URI (буфер/QR).
3. Включи шифрование (должно совпасть с `--enc`), стартуй.

Проверка: с телефона открыть сайт через WireTurn → работает (медленно, КБ/с);
`api.ipify.org` покажет IP цепочки (при `--proxy`), не домашний.

## Тесты

```sh
# yamux-интероп (hashicorp Go client ↔ наш server), эхо 1 МБ
cd /tmp/yamuxtest && go build -o yamuxtest . && \
  python3 /root/vpm/server/tests/yamux_echo_server.py &  ./yamuxtest

# локальный SOCKS5-стенд для проверки --proxy пути
python3 tests/socks5_proxy.py 1090
```

Полный стек проверялся так: наш сервер + `webdav-tunnel -mode client` (как
стенд-ин WireTurn — тот же протокол) через живой Яндекс.Диск, `curl` сквозь
SOCKS5 клиента → трафик доходит до цели.

## Ограничения

- Резервный канал: КБ/с, задержка ~секунды (поллинг файлов). Не для видео.
- Анти-абуз Диска: частый churn мелких чанков → троттлинг. Тюнить `poll`/`chunk`.
- yamux-реализация минимальна (CONNECT-потоки, flow-control, keepalive); экзотику
  вроде session-ping-инициатора со своей стороны не шлём — клиентского keepalive
  достаточно.
