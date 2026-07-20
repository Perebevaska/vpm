# wtserver — мульти-аккаунтный туннель-сервер (каркас разворота)

Go-переписка сервера под масштаб и мульти-транспорт. Ядро транспорт-агностично;
способ доставки трафика — сменный плагин.

## Зачем Go
Потолок Python-версии — тред-модель под GIL (сотни тредов на десятки юзеров).
Go: горутины (тысячи дёшево, без GIL) + `hashicorp/yamux` из коробки (минус наш
ручной yamux) + один статик-бинарь.

## Архитектура

```
Supervisor
 └─ Worker[account]          изоляция per-account (креды/квота/enc-ключ/троттл)
     └─ Transport.Accept()   → Session (дуплексный байт-канал)
         └─ SessionHandler.Serve(session)
```

Две оси расширения:

| | Transport (как клиент доходит) | SessionHandler (что делаем) |
|---|---|---|
| **MVP1** | `webdav` — поллинг чанков Диска | `YamuxPassthrough` — yamux, `[host][port]`+dial+relay |
| **MVP2** | `olcrtc` — WebRTC/видео | `VLESSBridge` — мост в VLESS-цепочку (xray) |

Supervisor / account-изоляция / egress — **общие** для обоих MVP.

## Пакеты
```
cmd/wtserver              CLI, загрузка конфига, supervisor
internal/config           accounts.json (stdlib, без внешних deps)
internal/transport        интерфейсы Transport + Session
internal/transport/webdav MVP1 транспорт (КАРКАС — портировать из Python)
internal/transport/olcrtc MVP2 транспорт (ЗАГЛУШКА — WebRTC, крупная веха)
internal/handler          SessionHandler: YamuxPassthrough (MVP1) / VLESSBridge (MVP2)
internal/egress           Dialer: Direct / upstream SOCKS5 (реализовано)
internal/account          Worker на аккаунт
internal/supervisor       сборка транспорт×handler по типу аккаунта
```

## Статус
- ✅ **MVP1 (webdav) рабочий end-to-end.** crypto, WebDAV-клиент, REST-аплоадер, Pipe,
  `webdav.Transport`, egress (direct/SOCKS5), yamux-passthrough. Проверено:
  `wtserver` ↔ `webdav-tunnel -mode client` (стенд-ин WireTurn) через живой Яндекс,
  curl сквозь SOCKS5 доходит до цели. Wire-совместимо с WireTurn.
- ⬜ `olcrtc.Transport` + `VLESSBridge` — MVP2 (pion/webrtc, сигналинг, видео-кодирование). Заглушка.

Прогресс и журнал — в [PLAN.md](PLAN.md). Дизайн-решения (с учётом замера #8) — [DESIGN.md](DESIGN.md).

### Заметки реализации
- Яндекс отдаёт **403 на PROPFIND Depth:2** → discovery = depth=1 + GET `init` на кандидата.
- Тюнинг-дефолты (замер #8): chunk 256KB, read-ahead 16, put-workers 16, poll 50–300ms.
- Тесты live скипаются без кредов; запуск: `set -a && . /path/.env && set +a && go test ./...`.

## Запуск
```sh
cp accounts.example.json accounts.json   # вписать реальные креды (в .gitignore)
go build -o wtserver ./cmd/wtserver
./wtserver -config accounts.json
```

## Изоляция multi-user
- **MVP1 (webdav):** аккаунт-на-юзера (свой Диск = своя квота/троттл/enc-ключ).
- **MVP2 (olcrtc):** через xray UUID на одном инбаунде — масштабируется лучше.
