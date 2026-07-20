# Сервер webdav-tunnel на WSL2 (external WebDAV → цепочка)

Сервер живёт на домашнем узле (WSL2 Ubuntu). Он поллит Яндекс.Диск по WebDAV,
собирает трафик клиента и выпускает его **не в интернет напрямую**, а через
`-proxy` в `bypass-in` на роутере → в твою цепочку.

## Предусловие: bypass-in на xkeen

Если ещё не сделано (тот же seam, что для olcRTC). В `03_inbounds.json`:

```json
{ "tag": "bypass-in", "listen": "0.0.0.0", "port": 10800,
  "protocol": "socks", "settings": { "auth": "noauth", "udp": false } }
```

В `05_routing.json` (рядом с ollama-правилами, не вместо):

```json
{ "type": "field", "inboundTag": ["bypass-in"], "outboundTag": "vless-reality" }
```

`xkeen -restart`. Проверка с WSL2:
`curl --socks5 192.168.1.1:10800 https://api.ipify.org` → должен вернуть IP выхода
цепочки (`meat`), не домашний.

## Шаг 1. Пароль приложения Яндекса

Яндекс ID → **Безопасность → Пароли приложений** → создать пароль типа **WebDAV**.
Этот пароль (не пароль от аккаунта!) используют **и сервер, и WireTurn** — аккаунт
один на обе стороны.

## Шаг 2. Взять бинарь webdav-tunnel

Сначала глянь готовый бинарь в релизах:
`https://github.com/spkprsnts/webdav-tunnel/releases` — если есть под linux/amd64,
скачай и пропусти сборку.

Иначе собрать (нужен Go 1.22+):

```bash
# в WSL2 Ubuntu
sudo apt update && sudo apt install -y golang-go git   # Go 1.22 в Ubuntu 24.04
git clone https://github.com/spkprsnts/webdav-tunnel.git
cd webdav-tunnel
go build -o webdav-tunnel .
```

(Если apt-Go старее 1.22 — поставь свежий с go.dev.)

## Шаг 3. Запустить сервер (external WebDAV mode)

```bash
./webdav-tunnel \
  -mode server \
  -webdav https://webdav.yandex.ru \
  -login ТВОЙ_ЯНДЕКС_ЛОГИН \
  -password ПАРОЛЬ_ПРИЛОЖЕНИЯ \
  -enc \
  -proxy socks5://192.168.1.1:10800
```

Что делают флаги:
- `-mode server` + `-webdav https://webdav.yandex.ru` — использовать Яндекс.Диск как
  посредник (не свой встроенный WebDAV).
- `-enc` — шифровать чанки AES-256-GCM (ключ из пароля). **Обязательно включить и на
  клиенте.**
- `-proxy socks5://192.168.1.1:10800` — весь исходящий трафик сервера идёт в
  `bypass-in` → цепочку. Это и делает exit = цепочка, а не домашний IP.

После старта сервер печатает **client URI**, например:

```
server: ════════════════════════════════════════════
server: client -uri webdavs://ЛОГИН:ПАРОЛЬ@webdav.yandex.ru?chunk-size=131071&coalesce=5ms&poll-max=100ms&poll-min=50ms&puts=16&read-max=16&read-min=3&enc=1
server: ════════════════════════════════════════════
```

**Скопируй эту `webdavs://...`-строку целиком** — она пойдёт в WireTurn (в ней уже
зашиты тюнинг и `enc=1`). URI содержит пароль в открытом виде — храни как секрет.

## Шаг 4. Автозапуск (systemd)

Смотри `webdav-tunnel.service` в этой папке. Кратко:

```bash
sudo cp webdav-tunnel /usr/local/bin/
sudo cp webdav-tunnel.service /etc/systemd/system/
# отредактируй логин/пароль в юните
sudo systemctl daemon-reload
sudo systemctl enable --now webdav-tunnel
```

В WSL2 systemd должен быть включён (`/etc/wsl.conf` → `[boot] systemd=true`, затем
`wsl --shutdown` из Windows). Если возиться не хочешь — запускай в `tmux`/`nohup`.

## Тюнинг под анти-абуз Диска

Чем реже поллинг и крупнее чанки — тем меньше файловых операций и ниже риск
троттлинга, но выше задержка. Дефолты из URI обычно ок; если Яндекс начнёт ругаться
— увеличь `poll-max`/`poll-min` и `chunk-size` (параметры в URI, см.
`docs/tuning.md` проекта). Это резервный канал — гонимся за «просто работает», не
за скоростью.
