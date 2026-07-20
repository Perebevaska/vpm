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
- ✅ Каркас компилируется (`go build ./...`), egress (direct/SOCKS5) и yamux-passthrough реализованы.
- ⬜ `webdav.Transport.Accept` — портировать логику из `server/wt/{webdav,pipe,crypto,rest_upload}.py`
  (+ оптимизации: PROPFIND depth=2, startup-cleanup только своих stale, адаптивный poll).
- ⬜ `olcrtc.Transport` + `VLESSBridge` — MVP2 (pion/webrtc, сигналинг, видео-кодирование).

## Запуск
```sh
cp accounts.example.json accounts.json   # вписать реальные креды (в .gitignore)
go build -o wtserver ./cmd/wtserver
./wtserver -config accounts.json
```

## Изоляция multi-user
- **MVP1 (webdav):** аккаунт-на-юзера (свой Диск = своя квота/троттл/enc-ключ).
- **MVP2 (olcrtc):** через xray UUID на одном инбаунде — масштабируется лучше.
