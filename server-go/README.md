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
internal/transport/webdav MVP1 транспорт (реализован + захарднен)
internal/transport/olcrtc MVP2 транспорт (ЗАГЛУШКА — WebRTC, крупная веха)
internal/handler          SessionHandler: YamuxPassthrough (MVP1) / VLESSBridge (MVP2)
internal/egress           Dialer: Direct / upstream SOCKS5 (реализовано)
internal/account          Worker на аккаунт
internal/supervisor       сборка транспорт×handler по типу аккаунта
```

> Проектный документ верхнего уровня (все каналы, БС-модель, деплой) — корневой
> [../README.md](../README.md). Здесь — README Go-сервера. Деплой рядом с 3x-ui —
> [../SETUP-3xui.md](../SETUP-3xui.md).

## Статус
- ✅ **MVP1 (webdav) готов и захарднен под прод-нагрузку.** crypto, WebDAV-клиент,
  REST-аплоадер, Pipe, `webdav.Transport`, egress (direct/SOCKS5), yamux-passthrough.
  Проверено E2E (`wtserver` ↔ стенд-ин WireTurn через живой Яндекс, curl сквозь
  SOCKS5). Wire-совместимо с WireTurn. Плюс хардненинг: дедубль сессий, rate-лимитер,
  бэкпрешер (RSS ограничен), discovery-модель. Вживую: текст + мелкие фото проходят;
  ≥~10 МБ рвёт клиента по потолку общего аккаунта (резервный **текстовый** канал).
- ⬜ `olcrtc.Transport` + `VLESSBridge` — MVP2 (pion/webrtc, сигналинг, видео-кодек).
  Заглушка; детальный план S9 — в [PLAN.md](PLAN.md).
- ⛔ MVP0 (Yandex Functions) — исключён (туннель не поднять; см. корневой README).

Прогресс и журнал — в [PLAN.md](PLAN.md). Дизайн-решения — [DESIGN.md](DESIGN.md).
Wire-протокол — [PROTOCOL.md](PROTOCOL.md).

### Заметки реализации
- Яндекс отдаёт **403 на PROPFIND Depth:2** → discovery = depth=1 + GET `init` на кандидата.
- Тюнинг-дефолты (актуальные, привязаны к WireTurn): chunk 131071 (128KB−1),
  read-ahead 4, put-workers 4, доставка 50–300ms, discovery 120–400ms, rate-лимитер
  8 rps/burst. Подробности — [DESIGN.md](DESIGN.md).
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
