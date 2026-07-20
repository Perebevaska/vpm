# PLAN — разворот на Go (мульти-аккаунт, мульти-транспорт)

Живой план. Обновляется после каждого этапа. Ветка: `feat/go-skeleton`.

Легенда: ⬜ todo · 🔄 в работе · ✅ готово · ⛔ нужен пользователь.

## Автономные этапы (порт Python→Go, без участия пользователя)

- ✅ **S0. Каркас** — supervisor/account/egress/handler/transport-интерфейсы, компилируется. (`7b6b584`)
- ✅ **S0.1 DESIGN с учётом замера #8** — striping отклонён, REST-дефолт, тюнинг-дефолты. (`f941939`)
- ✅ **S1. PLAN.md** — этот файл. (`b3b92d2`)
- ✅ **S2. crypto** — `internal/crypto`: derive_key + AES-256-GCM (nonce‖ct‖tag). `go test` зелёный; кросс-язык Python↔Go проверен в обе стороны (интероп доказан).
- ✅ **S3. WebDAV-клиент** — `internal/dav/client.go`: PUT/GET/DELETE/MKCOL/PROPFIND/OPTIONS, basic-auth, browser-UA, no-cache, 429/423. Live round-trip на Яндексе PASS.
- ✅ **S4. REST-аплоадер** — `internal/dav/restup.go`: Yandex REST upload (href+PUT), интерфейс `Uploader`. REST-put → WebDAV-get PASS.
- ✅ **S5. Pipe (сессия)** — `internal/pipe/pipe.go`: дуплекс на горутинах (единый flusher, read-ahead reorder, EOF, enc), реализует `transport.Session`. Тюнинг-константы здесь. Двусторонний loopback (300k/180k, enc) через Яндекс PASS.
- ✅ **S6. webdav.Transport.Accept** — поллинг `tunnel/`, srv-hb, per-session staleness (по hb), отдача Pipe-сессий, уборка dir при Close. REST-аплоад s2c при OAuthToken. ⚠️ PROPFIND **depth=2 Яндекс отдаёт 403** → откат на depth=1 + GET init на кандидата.
- ✅ **S7. End-to-end интеграция** — Go-сервер (`wtserver`) ↔ `webdav-tunnel -mode client` (стенд-ин WireTurn) через живой Яндекс: linked 7s, curl #1→egress, curl #2→ifconfig.me, сервер логирует оба `connect`. Wire-совместимость с WireTurn доказана.
- 🔄 **S8. Полировка** — README для server-go, финальный build/vet, чистка временных, (опц.) адаптивный poll.

## Требуют участия пользователя

- ⛔ **S9. MVP2 olcRTC** — реверс сигналинга WireTurn olcRTC + WebRTC (pion) + видео-кодирование (VP8/SEI) + `VLESSBridge`. Крупная веха, нужны решения/данные. Не автономно.
- ⛔ **S10. Прод-развёртывание** — systemd, реальные аккаунты, egress в цепочку (server1), мониторинг. Нужны креды/инфра.

## Заметки
- Замер #8: латентно-связаны, не троттл (429=0). Выигрыш — параллелизм (горутины), не striping.
- Wire-протокол зафиксирован в `../server/PROTOCOL.md`; порт обязан ему соответствовать (интероп с WireTurn).
- Оракул для тестов: `/root/webdav-tunnel/webdav-tunnel -mode client` (тот же протокол, что WireTurn).
- Креды тестов: `/root/webdav-tunnel/.env` (в дереве не хранить).

## Журнал
- S0/S0.1 — каркас + дизайн. Готово до начала автономного порта.
- S1 — PLAN.md.
- S2 — crypto Go: derive_key + AES-256-GCM. Кросс-язык Python↔Go в обе стороны OK.
- S3 — WebDAV-клиент Go. Live round-trip (ping/mkcol/put/get/404/propfind/delete) PASS.
- S4 — REST-аплоадер Go. REST-put → WebDAV-get PASS (write-путь wire-совместим).
- S5 — Pipe Go (горутины/каналы). Двусторонний loopback 300k/180k enc через Яндекс PASS.
- S6 — webdav.Transport.Accept (поллинг/сессии). Открыл: Яндекс запрещает PROPFIND depth=2 (403) → depth=1 + GET init.
- S7 — E2E: Go-сервер wtserver ↔ Go-клиент через Яндекс. curl сквозь SOCKS5 дошёл (egress 91.197.0.63 + ifconfig.me). Wire-совместимо с WireTurn.
