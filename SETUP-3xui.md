# Деплой `wtserver` рядом с 3x-ui

`wtserver` (Диск-канал, MVP1) ставится **на тот же сервер цепочки, где крутится
3x-ui** (сейчас server3, при миграции — server2). Он поллит Яндекс.Диск и выпускает
трафик **не напрямую в интернет**, а через локальный **SOCKS-inbound 3x-ui**, а
xray-роутинг 3x-ui заводит его в онвард-цепочку → `meat`. Так exit = цепочка, а не
IP этого сервера.

```
Телефон(БС) ─WebDAV→ webdav.yandex.ru ─→ wtserver ─socks5 127.0.0.1:10800→ 3x-ui/xray ─→ цепочка → meat → выход
```

Seam транспорт-агностичен: тот же SOCKS-inbound позже подхватит olcRTC-сервер (MVP2)
без изменений в 3x-ui.

---

## Шаг 1. SOCKS-inbound в 3x-ui

Панель 3x-ui → **Inbounds → Add**:

- Protocol: **socks**
- Listen IP: `127.0.0.1` (только локально — inbound не торчит наружу)
- Port: `10800`
- Authentication: **none** (слушаем только localhost)
- UDP: off

Дать ему запоминаемый tag (например `bypass-in`). Если правишь xray-конфиг руками,
эквивалент:

```json
{ "tag": "bypass-in", "listen": "127.0.0.1", "port": 10800,
  "protocol": "socks", "settings": { "auth": "noauth", "udp": false } }
```

## Шаг 2. Routing: inbound → онвард-цепочка

Панель 3x-ui → **Xray Settings / Routing** — правило: трафик из `bypass-in` уводить
в outbound, который идёт наверх по цепочке (тот же, что несёт server→meat). Ручной
эквивалент в `routing.rules`:

```json
{ "type": "field", "inboundTag": ["bypass-in"], "outboundTag": "vless-reality" }
```

`outboundTag` — имя твоего существующего онвард-outbound (server2/server1 → xkeen →
`meat`). Ничего в reverse/ollama не трогаем — это отдельное направление.

Применить: перезапустить xray из панели (Restart).

## Шаг 3. Конфиг `wtserver`

`/etc/wtserver/accounts.json` (креды — реальные, в репозиторий не коммитить):

```json
{
  "egress": { "proxy": "socks5://127.0.0.1:10800" },
  "accounts": [
    {
      "id": "user1",
      "transport": "webdav",
      "enc": true,
      "webdav_url": "https://webdav.yandex.ru",
      "login": "ТВОЙ_ЯНДЕКС_ЛОГИН",
      "app_password": "ПАРОЛЬ_ПРИЛОЖЕНИЯ_WEBDAV",
      "oauth_token": "OAUTH_ДЛЯ_REST_АПЛОАДА_ОПЦ"
    }
  ]
}
```

`egress.proxy` указывает на SOCKS-inbound из шага 1. `oauth_token` опционален
(ускоряет s2c-запись через REST ×3.7). Сборка бинаря — см. `server-go/README.md`
(`go build -o wtserver ./cmd/wtserver`), положить в `/usr/local/bin/wtserver`.

## Шаг 4. systemd-юнит

`/etc/systemd/system/wtserver.service`:

```ini
[Unit]
Description=wtserver — WebDAV disk-tunnel (Yandex Disk -> 3x-ui chain)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/wtserver -config /etc/wtserver/accounts.json
Restart=on-failure
RestartSec=5
# hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/etc/wtserver
DynamicUser=true

[Install]
WantedBy=multi-user.target
```

Запуск:

```
systemctl daemon-reload
systemctl enable --now wtserver
journalctl -u wtserver -f          # логи: "supervisor: 1 accounts", "worker up", connect'ы
```

## Шаг 5. Проверка seam

С сервера, где стоит 3x-ui + wtserver:

```
curl --socks5 127.0.0.1:10800 https://api.ipify.org
```

→ должен вернуть **exit-IP цепочки** (`meat`), не IP этого сервера. Если так —
egress-путь рабочий; дальше поднимать клиента (`SETUP-wireturn-client.md`) и гнать
трафик с телефона.

Приёмка канала целиком — чеклист в `README.md` §5.2.

---

## Миграция server3 → server2

Привязка только к `127.0.0.1:10800`, поэтому переезд тривиален: на новом сервере
повторить шаги 1–4 (inbound + routing + бинарь + unit + accounts.json), проверить
шаг 5, погасить старый `wtserver` на server3. Клиентский WireTurn-профиль не
меняется — он ходит на `webdav.yandex.ru`, а не на сервер.

## Заметки

- SOCKS-inbound слушает только `127.0.0.1` — не открывать наружу (это открытый
  прокси без авторизации). Если wtserver и 3x-ui на разных хостах — использовать
  приватную сеть/файрвол и `auth`.
- Один аккаунт Диска делят телефон и сервер (общий бюджет запросов) — под нагрузкой
  троттлится. Диск = текстовый резерв; тяжёлое → MVP2 olcRTC.
