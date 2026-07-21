# Обход белых списков РФ — проектный документ

> Цель: дать мобильному клиенту в режиме белых списков (БС) доступ в открытый
> интернет через существующую цепочку Xray, используя разрешённые сервисы как
> транспорт. Разработка идёт итерациями MVP0 → MVP1 → MVP2, после каждой —
> развёртывание и проверка на реальном мобильном в БС.

Дата: 2026-07-19 · Обновлён: 2026-07-19 · Статус: план, старт с MVP0 (Functions)

## Итоговая раскладка каналов

| MVP | Канал | Транспорт | Клиент | Платформы | Роль |
|-----|-------|-----------|--------|-----------|------|
| **MVP0** | Yandex Cloud Functions | VLESS+XHTTP (штатный) | **Happ** (или Karing/Shadowrocket/v2rayN) | **iOS + Android** | первый боевой, ноль кода на телефоне |
| **MVP1** | Яндекс.Диск | WebDAV-туннель | **WireTurn** | **Android only** | аварийный резерв: текст/ssh |
| **MVP2** | olcRTC | WebRTC (видео) | **WireTurn** | **Android only** | скоростной |

**iOS-реальность:** нативно и «через подписку» на iPhone работает **только
Functions** (XHTTP-клиенты Happ / Karing / Shadowrocket). Диск и olcRTC — **только
Android** через WireTurn (компаньон отдаёт локальный SOCKS5, подписочный клиент
цепляется через `detour`). iOS-путь через `kulikov0` (.ipa, proxy-only)
сознательно **не берём**.

---

## 0. Среда и подтверждённые решения

**Среда:**

- VPS цепочки: **Ubuntu 24.04 LTS**. Xray-core, конфиг `/usr/local/etc/xray/config.json`.
- Домашний узел: **xkeen на домашнем роутере (Keenetic)** + Win11-ПК за ним.
  Разработка серверных частей — в **WSL2 (Ubuntu)** на Win11 (паритет с VPS).
- Существующая цепочка/реверс живые.

**Реквизиты (из реальных конфигов):**

- **xkeen (домашний роутер = точка приземления Диск/olcRTC):** наверх идёт outbound
  `vless-reality` → `meat.isgood.host:443` (reality, SNI `meat-2.isgood.host`,
  fp firefox, id `6af8e684-…`). Входы — прозрачный прокси `redirect`/`tproxy`
  на 61219. ollama на LAN-хосте `192.168.1.40:11434`, отдаётся через `to-ollama`.
  Reverse-bridge `rev.ollama`.
- **VPS (потребитель ollama-туннеля, НЕ landing):** `ollama-local` dokodemo →
  `to-latvia` → `soybeans.isgood.host:443`.
- LAN-IP роутера: **`192.168.1.1`**.

**Решения (подтверждено):**

| Вопрос | Решение |
|--------|---------|
| Backend для XHTTP (MVP0 Functions) | **публичный сервер цепочки** (сервер3/вход), НЕ дом за NAT — функция сама коннектится к backend'у |
| ОС для сборки/серверов | WSL2 Ubuntu на Win11 → тот же бинарь, что на VPS |
| Диск-канал (MVP1) | **прямой WebDAV** (`webdav.yandex.ru` в БС доступен), клиент WireTurn, сервер `webdav-tunnel` — своего кода не нужно |
| Reverse ollama | **не трогаем** (см. 2.4) — bypass идёт мимо него |
| iOS | только Functions; Диск/olcRTC — Android/WireTurn |

**Блокеров нет:** конфиги получены, хосты проверены (`webdav.yandex.ru`,
`cloud-api.yandex.net`, `functions.yandexcloud.net` — в БС доступны).

---

## 1. Контекст и модель среды

В режиме БС оператор фильтрует трафик на двух уровнях одновременно:

- **L3 (IP/CIDR).** Пакет к неразрешённой подсети дропается на 2-м хопе (ТСПУ),
  ещё до DPI. Для не-whitelist IP — 100% packet loss по всем портам.
- **L7 (SNI + TLS-фингерпринт).** Даже на разрешённом IP DPI смотрит SNI в
  ClientHello и фингерпринт. Палевный fingerprint → RST.

Практические следствия, зашитые в дизайн:

1. **Свой VPS напрямую недостижим** — его IP не в списке. Значит вход всегда
   через разрешённый сервис (Yandex Cloud, видеозвонки, Диск).
2. **UDP почти весь вырезан** (QUIC, DNS, WireGuard = NO_RESP даже на whitelist
   IP). Работают TCP 80/443/22. → все транспорты поверх **TCP/TLS:443**.
3. **Нужна маскировка под браузер** — `fp=chrome`/`firefox` (uTLS) в Xray, Reality
   сама по себе не спасает.
4. **Yandex.Cloud — главная точка обхода**: ~1 из 5 whitelist-IP принадлежит ему
   (AS200350, ~12.9k IP), выкинуть невозможно. Functions и Диск опираются на него.
5. **Белые списки неоднородны** по операторам/регионам/вышкам. Отсюда — не один
   канал, а несколько с автопереключением.

Источник данных по спискам (обновляется еженедельно): `openlibrecommunity/twl`.

---

## 2. Целевая архитектура

### 2.1. Топология

Два разных пути в зависимости от канала:

```
MVP0 Functions (штатный XHTTP, iOS+Android):
  Телефон(БС) ─VLESS+XHTTP→ *.functions.yandexcloud.net ─→ backend (публичный сервер цепочки) ─→ выход
  (дом НЕ участвует)

MVP1 Диск / MVP2 olcRTC (WireTurn, Android):
  Телефон(БС) ─WebDAV / WebRTC→ разрешённый сервис ─→ домашний узел (WSL2)
        │ локальный SOCKS5 → bypass-in на xkeen → vless-reality → meat → выход
```

Ключевая идея для Диск/olcRTC: **к домашнему узлу никто не подключается входящим**
— и телефон, и дом идут исходящими на разрешённую точку встречи (хосты Диска / SFU
видео). Дом на проводном канале (БС на него не действует) свободно достаёт и до
сервиса, и до цепочки. Functions в доме не нуждается — backend публичный.

### 2.2. Единый seam интеграции

Для Диск/olcRTC серверная часть на домашнем узле отдаёт **локальный SOCKS5**, а он
заходит в цепочку через `bypass-in` на xkeen (см. 2.4). Цепочка не знает, какой
транспорт под ней — каналы взаимозаменяемы.

Клиенты:

- **Functions** — штатный XHTTP: любой Xray-клиент (Happ / Karing / Shadowrocket /
  v2rayN) подключается **прямо по подписке**, без компаньона, на iOS и Android.
- **Диск / olcRTC** — нештатные транспорты: на телефоне нужен **WireTurn**
  (Android), который поднимает WebDAV/WebRTC-туннель и отдаёт локальный SOCKS5.
  Подписочный клиент при желании цепляется к нему через `detour`, но WireTurn и сам
  умеет TUN/split-tunnel.

### 2.3. Тиры каналов (скорость/роль)

| Тир | Канал | Скорость | Роль |
|-----|-------|----------|------|
| 1 | olcRTC (видео) | до ~10 MB/s (DataChannel) | основной, скорость |
| 2 | Functions | средняя (≤3.5 МБ/вызов) | устойчивый, кросс-платформенный |
| 3 | Яндекс.Диск (WebDAV) | КБ/с, высокая задержка | аварийный, текст/ssh |

Тиры **не смешиваются в один поток** (медленный путь убьёт реордер-буфер).
Порядок **реализации** (MVP0→1→2) ≠ номера тиров: сначала делаем самый простой в
доставке (Functions), затем резерв (Диск), потом скорость (olcRTC).

### 2.4. Reverse НЕ трогаем — bypass идёт мимо

Reverse (bridge/portal) обслуживает **обратное** направление (достучаться до ollama
на домашнем узле со стороны публичного сервера). У Диск/olcRTC направление
противоположное — трафик **приземляется** на дом и уходит **вверх** в цепочку.
→ **Reverse/ollama не трогаем.** Добавляем параллельный вход и переиспользуем
существующий outbound наверх.

**Интеграционный seam (дельта в конфиг xkeen):**

`03_inbounds.json` — добавить socks-inbound на LAN роутера:

```json
{ "tag": "bypass-in", "listen": "0.0.0.0", "port": 10800,
  "protocol": "socks", "settings": { "auth": "noauth", "udp": false } }
```

`05_routing.json` — правило (существующие скоупятся по `redirect`/`tproxy`/`bridge`,
не заденут наше и наоборот):

```json
{ "type": "field", "inboundTag": ["bypass-in"], "outboundTag": "vless-reality" }
```

Серверная часть Диск/olcRTC (WSL2) использует upstream SOCKS `192.168.1.1:10800`.
Проверка: `xkeen -restart`, затем с WSL2
`curl --socks5 192.168.1.1:10800 https://api.ipify.org` → IP выхода `meat`, не
домашний РФ-адрес.

### 2.5. Готовая база — своего кода почти нет

| Задача | Готовое решение | Статус |
|--------|-----------------|--------|
| Клиент Диск+olcRTC (Android) | `spkprsnts/WireTurn` — WebDAV + WebRTC + встроенный Xray, TUN, split-tunnel, dual-route | как есть |
| Сервер Диск (WebDAV-туннель) | `spkprsnts/webdav-tunnel` — TCP over WebDAV, SOCKS5, yamux, AES-256-GCM | как есть (один codebase с WireTurn) |
| Ядро olcRTC (сервер) | `openlibrecommunity/olcrtc` — 4 транспорта (DataChannel/VP8/SEI/Video), upstream-SOCKS | как есть |
| olcRTC на роутере | `tankionline2005/OlcRTC-OpenWRT` | опц. |
| Подписки/мульти-инстанс | `BigDaddy3334/olcrtc-manager-panel` | опц. |
| Ядро (расширенное) | `TheAirBlow/Turnable` — TURN+SFU, multi-peer, mux | опц. |
| Клиент Functions | Happ / Karing / Shadowrocket (штатный XHTTP) | как есть |

Вывод: кастомная разработка сведена к минимуму — в основном сборка/деплой готового
и правка конфигов. olcRTC и webdav-tunnel цепляются в цепочку **штатно** через
upstream-SOCKS → `bypass-in`.

---

## 3. План разработки по этапам

Каждый MVP самодостаточен, заканчивается развёртываемым артефактом и **чеклистом
приёмки** на реальном мобильном в БС.

Общий критерий: **с мобильного в БС открывается `https://google.com` через цепочку,
exit-IP = выход цепочки (не домашний РФ-адрес).**

### 3.1. Ближайшие шаги (backlog)

**MVP0 — Functions (первый, ноль кода на телефоне):**

1. **Backend на публичном сервере цепочки** (сервер3/вход, НЕ дом за NAT): Xray
   VLESS+**XHTTP** inbound (`/api`, packet-up, TLS), маршрут в цепочку.
2. **Yandex Cloud:** консоль → каталог → сервисный аккаунт → функция-релей
   (env `BACKEND`) → публичная → URL `*.functions.yandexcloud.net`. Алерты биллинга.
3. **Клиент:** профиль VLESS+XHTTP в подписке (Happ/Karing/Shadowrocket). Кода нет.
4. Приёмка (4.4): сёрфинг с телефона в БС.

**MVP1 — Диск (резерв, WebDAV, Android):**

5. **Дом:** WSL2 Ubuntu на Win11; правка xkeen (`bypass-in` + routing, см. 2.4);
   проверка seam `curl --socks5 192.168.1.1:10800 …` → exit `meat`.
6. Развернуть `spkprsnts/webdav-tunnel` server на WSL2, нацелить на
   `webdav.yandex.ru`, upstream-SOCKS → `bypass-in`.
7. WireTurn (Android) — WebDAV-профиль на Яндекс.Диск. Приёмка (5.4).

**MVP2 — olcRTC (скорость, WireTurn, Android):**

8. `openlibrecommunity/olcrtc` server на домашнем узле; upstream-SOCKS → `bypass-in`.
9. WireTurn — WebRTC-профиль (WB Stream), затем пул платформ. Приёмка (6.4).

Параллелится: пока идёт Functions, дома ставится WSL2 и правится xkeen (нужно для
MVP1/MVP2).

---

## 4. MVP0 — Yandex Cloud Functions (VLESS+XHTTP)

**Зачем первым.** Всегда-разрешённый домен `*.functions.yandexcloud.net` у всех
операторов, бессрочный free tier, **ноль кода на телефоне**, кросс-платформенно
(iOS+Android через любой XHTTP-клиент). **Белый IP не нужен** — паразитируешь на
уже-разрешённых IP Yandex.Cloud; backend может быть любым зарубежным VPS.

### 4.1. Принцип

Функция — разрешённая **прокладка (mirror)**, не exit. Форвардит HTTP на backend
(публичный сервер цепочки) с Xray-транспортом **XHTTP**.

```
Телефон(БС) ─HTTPS→ <id>.functions.yandexcloud.net ─→ функция-релей ─→ backend Xray(VLESS+XHTTP) ─→ цепочка
```

**Почему XHTTP:** функция живёт ≤10 мин на вызов и не держит постоянный сокет.
XHTTP (бывш. SplitHTTP) спроектирован под request/response HTTP-инфраструктуру
(CDN/serverless), с дискретными пакетами и переустановкой — единственный транспорт
Xray, который туда ложится. **XHTTP есть в Xray-ядре** (Happ, Shadowrocket, v2rayN)
и в **модиф. sing-box Karing**; в vanilla sing-box (NekoBox) XHTTP нет — не брать.

### 4.2. Лимиты функций (проверено, определяют дизайн)

| Лимит | Значение | Следствие |
|-------|----------|-----------|
| Размер запроса/ответа | ≤ 3.5 МБ | XHTTP режет на пакеты, не длинный стрим |
| Таймаут вызова | ≤ 600 c | XHTTP переустанавливает, норма |
| Стриминг ответа | не гарантирован | режим packet-up |
| Free tier | 1M вызовов + 100k ГБ-сек/мес | следить за биллингом: пакет = вызов |

### 4.3. Функция-релей (Node.js, скелет)

```js
const BACKEND = "https://BACKEND_HOST"; // публичный backend с Xray XHTTP inbound
module.exports.handler = async (event) => {
  const url = BACKEND + (event.url || event.path || "/");
  const body = event.isBase64Encoded ? Buffer.from(event.body, "base64") : event.body;
  const r = await fetch(url, {
    method: event.httpMethod,
    headers: { ...event.headers, host: undefined },
    body: ["GET","HEAD"].includes(event.httpMethod) ? undefined : body,
  });
  const buf = Buffer.from(await r.arrayBuffer());
  return { statusCode: r.status, headers: Object.fromEntries(r.headers),
           isBase64Encoded: true, body: buf.toString("base64") };
};
```

Клиентский профиль: VLESS+XHTTP, `Host`/`address` = домен функции,
`SNI = functions.yandexcloud.net`, `fp=chrome`, путь `/api`.

### 4.4. Куда заходить / приёмка

- Консоль: `https://console.yandex.cloud` → каталог → Сервисные аккаунты (роль
  `functions.functionInvoker`) → Cloud Functions → создать → сделать публичной.
- Биллинг: включить алерты. Свой домен и API Gateway **не нужны** (Gateway забанили).
- [ ] `curl https://<id>.functions.yandexcloud.net/health` отвечает от backend.
- [ ] С телефона (Happ/Karing) в БС сёрфинг работает, exit = цепочка.
- [ ] Free tier не превышен за сутки теста.

### 4.5. Ограничения

- Скорость средняя (3.5 МБ/вызов + cold start) — не для тяжёлого видео.
- Вызовы тратятся быстро → мониторить биллинг.

---

## 5. MVP1 — Яндекс.Диск (WebDAV-туннель, аварийный резерв)

**Роль.** Несгораемый резерв: КБ/с, задержка секунды, но переживает отвал
видео/функций. Для текста/ssh/мессенджера, не для сёрфинга. **Android only.**

**Ключевое упрощение:** `webdav.yandex.ru` **в БС доступен** → строим на **прямом
WebDAV**, без web-upload-костылей и **без своего кода**:

- **Клиент (Android):** WireTurn, режим **WebDAV** (встроенный `libwebdav`).
- **Сервер (WSL2):** `spkprsnts/webdav-tunnel` server, нацелен на `webdav.yandex.ru`;
  локальный SOCKS5 → `bypass-in` xkeen → цепочка.
- Клиент и сервер — один codebase (spkprsnts) → совместимы, кастом не нужен.

Принцип: трафик сериализуется в нумерованные бинарные чанк-файлы, заливается/
забирается по WebDAV поверх HTTPS (маскируется под работу с облачным диском).
yamux-мультиплекс, AES-256-GCM.

### 5.1. Куда заходить

- **Пароль приложения** для WebDAV: Яндекс ID → Безопасность → Пароли приложений
  (тип WebDAV). (OAuth тут не нужен — WebDAV авторизуется логином + app-паролем.)
- WebDAV-URL: `https://webdav.yandex.ru`.
- Сервер: `github.com/spkprsnts/webdav-tunnel`. Клиент: WireTurn (Android APK).
- Два аккаунта Диска — опционально, для изоляции нагрузки (up/down по разным
  аккаунтам); базово хватает одного с двумя папками.

### 5.2. Критерии приёмки

- [ ] webdav-tunnel server на WSL2 поднят, SOCKS5 → `bypass-in` → exit цепочки
      (`curl --socks5 …` даёт IP цепочки).
- [ ] WireTurn (WebDAV) с телефона в БС открывает сайт (медленно, КБ/с).
- [ ] Чанки чистятся, квота Диска не растёт бесконтрольно.

### 5.3. Ограничения

- КБ/с, секундные задержки — резерв.
- **iOS нет** (WireTurn Android-only).
- Анти-абуз Диска при диком churn файлов → умеренный темп.
- Фолбэк, если `webdav.yandex.ru` когда-то выпадет: web-upload REST
  (`cloud-api.yandex.net` + `storage.yandex.net`) — потребует своего адаптера.

---

## 6. MVP2 — olcRTC (WebRTC-видео, скоростной канал)

**Зачем.** Самый быстрый канал (DataChannel до ~10 MB/s). Паразитирует на
whitelisted-видеозвонках через WebRTC SFU, а не голый TURN-relay (голый TURN
прибили: VK шейпит до 250 кб/с, Яндекс релеит только на свои IP). **Android only**
(через WireTurn).

### 6.1. Принцип

```
Телефон(БС) ─WebRTC/TCP:443→ SFU (WB Stream / Telemost / SaluteJazz) → домашний узел → SOCKS5 → цепочка
```

- Сервисы: `stream.wb.ru` (самый стабильный), `telemost.yandex.ru`, `salutejazz.ru`.
- Поверх **TCP/TLS:443** (при полном UDP-киле UDP-TURN не поднимется).
- 4 транспорта olcRTC: **DataChannel** (скорость), **VP8Channel**/**SEIChannel**/
  **VideoChannel** (видео-стеганография — запас на операторов с глубокой инспекцией).

### 6.2. Что делаем

- Сервер — `openlibrecommunity/olcrtc` на домашнем узле; интеграция **штатная**
  через upstream-SOCKS (`socks.proxy_addr/port` → `bypass-in`).
- Клиент — **WireTurn** (Android). Опц. на роутере — `OlcRTC-OpenWRT`.
- **Мультипат:** 2–3 платформы одновременно (WB Stream + SaluteJazz + Telemost),
  health-check, авто-failover; multi-peer агрегация есть в WireTurn/Turnable.
- Кеш кредов SFU с коротким TTL и перезапросом web-API при отвале.

> **Кандидат в пул (research/PoC): Контур.Толк (`ktalk.ru`).** Готовой реализации
> нет; аккаунты — не блокер (регистрация бесплатная, можно нагенерить). Барьер в
> другом: режим TURN-relay на внешний IP у Контура режется (дропает non-Kontur
> IP). Проверять надо **режим 2 — SFU с двумя аккаунтами в одной комнате**, где
> SFU форвардит DataChannel между участниками (как olcRTC с WB Stream/Telemost).
> Задачи PoC: (1) реверс web-API ktalk (guest/room/token/WS — как lionheart для WB
> Stream); (2) проверить, релеит ли SFU произвольный DataChannel между двумя
> участниками. Оправдан как запас на случай, если WB Stream прикроют. Трекер живых
> carrier-платформ — `net4people/bbs` issue #618.

### 6.3. Критерии приёмки

- [ ] Канал через WB Stream поднимается, WireTurn отдаёт SOCKS5 → цепочка.
- [ ] С телефона в БС сёрфинг/видео на приемлемой скорости, exit = цепочка.
- [ ] Падение платформы → авто-переключение.
- [ ] Скорость заметно выше MVP0 (десятки Мбит, упор в мобильную соту).

### 6.4. Ограничения

- Реверс web-API платформ — самое хрупкое звено → спасает пул платформ.
- Потолок скорости ставит мобильная сота (~40–60 Мбит), не релей.
- **iOS нет** (kulikov0-путь не берём).

---

## 7. После MVP2 — transport-manager (объединение каналов)

Когда все три готовы — менеджер, держащий единый локальный SOCKS5 и переключающий
подложку по тирам: тир 1 (olcRTC) → при отвале тир 2 (Functions) → тир 3 (Диск) с
деградацией до «текст». Цепочка сверху не меняется — только подложка. На Android
роль частично закрывает сам WireTurn (dual-route, профили).

---

## 8. Консоли и ссылки

| Что | Где |
|-----|-----|
| Yandex Cloud консоль / Functions | `https://console.yandex.cloud` |
| Яндекс ID — пароли приложений (WebDAV) | `https://id.yandex.ru` → Безопасность |
| WebDAV Диска | `https://webdav.yandex.ru` |
| WireTurn (клиент Диск+olcRTC) | `github.com/spkprsnts/WireTurn` |
| webdav-tunnel (сервер Диск) | `github.com/spkprsnts/webdav-tunnel` |
| olcRTC ядро / роутер / панель | `openlibrecommunity/olcrtc` · `tankionline2005/OlcRTC-OpenWRT` · `BigDaddy3334/olcrtc-manager-panel` |
| Turnable (расшир. ядро) | `github.com/TheAirBlow/Turnable` |
| Списки IP/SNI | `github.com/openlibrecommunity/twl` |
| 3x-ui | твоя панель на сервере цепочки |

---

## 9. Сквозные требования (все MVP)

- Транспорт **только поверх TCP/TLS:443** (UDP в БС мёртв).
- VLESS-слой цепочки: **uTLS `fp`**, SNI из whitelist (`storage.yandex.net`,
  `userapi.com`, `yastatic.net` и т.п. из twl).
- Полезная нагрузка bypass-каналов **шифруется** (AES) независимо от TLS.
- Клиенты: **Functions** — любой XHTTP-клиент (Happ/Karing/Shadowrocket, iOS+And);
  **Диск/olcRTC** — WireTurn (Android).

---

## 10. Риски и эксплуатация

- **Хрупкость реверс-API** (Диск, olcRTC) — держим ≥2 каналов и пул платформ.
- **Публичность убивает дыры** — не выкладываем рабочие эндпоинты.
- **Биллинг Functions** — алерты, чтобы free tier не перерос в списание.
- **Неоднородность БС** — тестировать на нескольких операторах/точках.
- **iOS ограничен** одним каналом (Functions) — учитывать при раздаче доступа.
- **ToS сервисов** — использование чужой инфраструктуры как транспорта нарушает
  их условия; осознанный выбор оператора решения.
