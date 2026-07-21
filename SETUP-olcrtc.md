# Деплой olcRTC-сервера (MVP2, скоростной канал)

> ⚠️ **MVP2 ЗАПАРКОВАН (2026-07-21).** Авто-канал через стриминги нежизнеспособен:
> платформы закрыли guest-создание комнат (WB Stream → `Guests are not allowed to
> create room`; Telemost — только Yandex 360 org; публичный jitsi → `token
> required`), логин = 2FA/3FA. Итоговое решение проекта — **MVP1 (Диск)**. Этот
> файл — **референс** (механика деплоя подтверждена вживую на jitsi/datachannel вне
> БС). Живой БС-канал возможен лишь на вручную созданной комнате (1 аккаунт, не
> масштаб) или через self-host jitsi на Yandex Cloud. Обоснование — `README.md` §6.

MVP2 = **готовый `openlibrecommunity/olcrtc`** (не свой код). Это encrypted
TCP-over-WebRTC туннель: трафик маскируется под видеозвонок на разрешённом сервисе
(WB Stream / Telemost / Jitsi). Сервер уже включает все 4 транспорта
(datachannel / vp8 / sei / video), XChaCha20-Poly1305 и smux — реверсить и писать
свой кодек не нужно.

```
Телефон(БС): app → SOCKS5(WireTurn) → olcrtc cnc → WebRTC/SFU(WB Stream) →
             olcrtc srv → socks.proxy → SOCKS-inbound 3x-ui → цепочка → meat → выход
```

Клиент — **WireTurn** (Android, встроенный olcRTC `cnc`), сервер — **`olcrtc srv`**
рядом с 3x-ui. Egress сервера заводим в тот же `bypass-in`, что у MVP1 (Диск), —
seam общий, каналы взаимозаменяемы.

Ссылки: `github.com/openlibrecommunity/olcrtc` · клиент
`github.com/spkprsnts/WireTurn`.

---

## Шаг 0. Предусловие — SOCKS-inbound в 3x-ui

Тот же локальный SOCKS-inbound `127.0.0.1:10800`, что и для Диск-канала — см.
`SETUP-3xui.md` шаги 1–2 (inbound + routing в онвард-цепочку). Если он уже поднят
для `wtserver`, olcRTC переиспользует его как есть.

## Шаг 1. Сборка/деплой сервера (интерактивный скрипт)

На сервере цепочки (рядом с 3x-ui):

```bash
git clone https://github.com/openlibrecommunity/olcrtc --recurse-submodules
cd olcrtc
./script/srv.sh
```

Скрипт ставит Podman (если нет), собирает бинарь в контейнере и спрашивает конфиг.
Ответы под наш кейс:

- **Carrier / provider:** `wbstream` (самый стабильный; пул на потом — telemost/jitsi).
- **Transport:** `datachannel` (скорость; vp8/sei/video — запас под глубокую инспекцию).
- **Room:** авто-генерация.
- **DNS:** по умолчанию `8.8.8.8:53`.
- **SOCKS5 proxy for egress:** **`127.0.0.1:10800`** — это и заводит выход сервера
  в цепочку (bypass-in → xray → meat). Без него olcrtc пойдёт в интернет напрямую.

Результат: ключ в `~/.olcrtc_key` и **`olcrtc://`-URI** (несёт ключ + комнату) —
его импортируем в WireTurn (шаг 3).

## Шаг 2. Ручной конфиг (альтернатива скрипту)

Если собираешь бинарь напрямую (`./build/olcrtc-linux-amd64 server.yaml`), минимальный
серверный YAML + upstream-egress в цепочку:

```yaml
mode: srv
auth:
  provider: wbstream
room:
  id: "auto-or-your-room-id"
crypto:
  key_file: "~/.olcrtc_key"     # общий с клиентом (URI несёт этот ключ)
net:
  transport: datachannel
  dns: "8.8.8.8:53"
socks:                           # egress сервера в цепочку (upstream SOCKS5)
  proxy_addr: "127.0.0.1"
  proxy_port: 10800
data: data
```

Поля egress — `socks.proxy_addr` / `socks.proxy_port` (+ опц. `proxy_user`/`proxy_pass`).
Если имена в твоей версии отличаются — сверься с `docs/configuration.md`; надёжнее
задать egress через prompt `srv.sh`.

## Шаг 3. Клиент WireTurn

- Импортировать `olcrtc://`-URI (из шага 1) в WireTurn → профиль типа **olcRTC**
  (тот же carrier/transport, что у сервера; ключ и комната — из URI).
- WireTurn поднимет WebRTC-туннель и отдаст локальный SOCKS5 на телефоне
  (подписочный клиент цепляется через `detour`, либо WireTurn TUN).

## Шаг 4. systemd-юнит (после отладки скриптом)

Когда конфиг подобран, обернуть бинарь в сервис (на том же хосте, где 3x-ui):

```ini
[Unit]
Description=olcrtc srv — WebRTC tunnel (WB Stream -> 3x-ui chain)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/olcrtc /etc/olcrtc/server.yaml
Restart=on-failure
RestartSec=5
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now olcrtc
journalctl -u olcrtc -f
```

(Если используешь podman-деплой из скрипта — вместо этого юнит на контейнер или
`podman generate systemd`.)

## Шаг 5. Проверка

olcRTC-сервер сам присоединяется к комнате и **ждёт клиента** — egress течёт только
когда WireTurn подключился. Проверка сквозная, с телефона:

- В WireTurn поднять olcRTC-профиль → локальный SOCKS5 (напр. `127.0.0.1:8808`).
- `curl --socks5-hostname 127.0.0.1:8808 https://icanhazip.com` (с устройства через
  этот SOCKS) → **exit-IP цепочки** (`meat`), не адрес телефона/оператора.

Приёмка канала целиком — чеклист в `README.md` §6.3.

---

## Заметки

- **Только TCP/TLS:443** — в БС UDP мёртв, UDP-TURN не поднимется; провайдеры выше
  ходят по TCP.
- **Пул платформ (надёжность):** старт на `wbstream`; при отвале — `telemost` /
  `jitsi`, health-check + failover (частично умеет сам WireTurn / `Turnable`).
- **`crypto.key` секретен** — общий у srv и клиента (URI его несёт); не публиковать.
- **Общий seam с MVP1:** olcRTC и Диск используют один `bypass-in` в 3x-ui →
  сосуществуют; сверху цепочка одна.
- **Расширенное ядро (опц.):** `TheAirBlow/Turnable` (TURN+SFU+multi-peer+mux) — если
  нужен multi-peer/агрегация; базовый кейс закрывает `olcrtc`.

> Примечание по плану: этот деплой-путь (готовый olcrtc) заменяет ранее описанный в
> `server-go/PLAN.md` S9-мегаплан «писать свой pion+VP8/SEI-кодек» — он избыточен,
> т.к. готовый сервер делает всё. Актуализацию PLAN отложили (сейчас только деплой).
