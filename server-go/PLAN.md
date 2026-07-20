# PLAN — разворот на Go (мульти-аккаунт, мульти-транспорт)

Живой план. Обновляется после каждого этапа. Ветка: `feat/go-skeleton`.

Легенда: ⬜ todo · 🔄 в работе · ✅ готово · ⛔ нужен пользователь.

## Автономные этапы (порт Python→Go, без участия пользователя)

- ✅ **S0. Каркас** — supervisor/account/egress/handler/transport-интерфейсы, компилируется. (`7b6b584`)
- ✅ **S0.1 DESIGN с учётом замера #8** — striping отклонён, REST-дефолт, тюнинг-дефолты. (`f941939`)
- ✅ **S1. PLAN.md** — этот файл. (`b3b92d2`)
- ✅ **S2. crypto** — `internal/crypto`: derive_key + AES-256-GCM (nonce‖ct‖tag). `go test` зелёный; кросс-язык Python↔Go проверен в обе стороны (интероп доказан).
- ✅ **S3. WebDAV-клиент** — `internal/dav/client.go`: PUT/GET/DELETE/MKCOL/PROPFIND/OPTIONS, basic-auth, browser-UA, no-cache, 429/423. Live round-trip на Яндексе PASS.
- 🔄 **S4. REST-аплоадер** — `internal/dav`: Yandex REST upload (href+PUT). Тест: REST-put → WebDAV-get.
- ⬜ **S5. Pipe (сессия)** — `internal/pipe`: дуплекс поверх чанков (coalesce-запись, read-ahead чтение, порядок, EOF, enc), реализует `transport.Session` (io.ReadWriteCloser + ID). Тюнинг из DESIGN. Тест: двусторонний loopback через Яндекс.
- ⬜ **S6. webdav.Transport.Accept** — поллинг `tunnel/` (PROPFIND depth=2 → sid+init за 1 запрос), srv-hb, self-only stale-cleanup, отдача Pipe-сессий. REST-аплоад s2c по умолчанию при OAuthToken.
- ⬜ **S7. End-to-end интеграция** — Go-сервер ↔ `webdav-tunnel -mode client` (стенд-ин WireTurn) через живой Яндекс, curl сквозь SOCKS5 → проверка egress. Wire-совместимость.
- ⬜ **S8. Полировка** — bounded http.Client per-account, адаптивный poll-заглушка, README, финальный build/vet, чистка.

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
