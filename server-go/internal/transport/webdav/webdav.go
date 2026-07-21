// Package webdav — транспорт MVP1: рандеву через WebDAV-хранилище (Яндекс.Диск),
// нумерованные чанк-файлы, AES-256-GCM, REST-аплоад s2c. Wire-совместим с WireTurn.
//
// Порт server/wt/server.py (серверная сторона):
//   - discovery: PROPFIND depth=1 tunnel/ → sid'ы, затем GET init на кандидата.
//     (depth=2 Яндекс отдаёт 403 Forbidden — проверено; оптимизация неприменима);
//   - s2c upload через REST по умолчанию (при OAuthToken) — ×3.7 к записи (замер #8);
//     download остаётся WebDAV GET; латентность прячем параллелизмом, не striping;
//   - staleness проверяется per-session (по hb), живые чужие сессии не трогаем.
package webdav

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Perebevaska/vpm/server-go/internal/config"
	"github.com/Perebevaska/vpm/server-go/internal/crypto"
	"github.com/Perebevaska/vpm/server-go/internal/dav"
	"github.com/Perebevaska/vpm/server-go/internal/pipe"
	"github.com/Perebevaska/vpm/server-go/internal/transport"
)

const (
	staleSessionAge = 90 * time.Second
	pollEvery       = 3 * time.Second
	srvHBEvery      = 20 * time.Second // как часто освежаем srv-hb (клиент следит за живостью сервера)
	reapMisses      = 3                // сколько подряд промахов листинга до реапа known-сессии
)

type Transport struct {
	acc    config.Account
	client *dav.Client
	up     dav.Uploader
	key    []byte

	once      sync.Once
	sessionCh chan transport.Session
	ctx       context.Context
	cancel    context.CancelFunc
	known     map[string]bool // только poll-горутина
	miss      map[string]int  // подряд промахов листинга по sid (дебаунс реапа)
}

func New(acc config.Account) *Transport {
	client := dav.NewClient(acc.WebDAVURL, acc.Login, acc.AppPassword, 60*time.Second)
	var up dav.Uploader = client // дефолт — WebDAV PUT
	if acc.OAuthToken != "" {
		up = dav.NewRestUploader(acc.OAuthToken, 60*time.Second) // s2c через REST (×3.7)
	}
	var key []byte
	if acc.Enc {
		key = crypto.DeriveKey(acc.AppPassword)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Transport{
		acc: acc, client: client, up: up, key: key,
		sessionCh: make(chan transport.Session, 8),
		ctx:       ctx, cancel: cancel,
		known: map[string]bool{},
		miss:  map[string]int{},
	}
}

func (t *Transport) Accept(ctx context.Context) (transport.Session, error) {
	t.once.Do(func() { go t.poll() })
	select {
	case s := <-t.sessionCh:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.ctx.Done():
		return nil, context.Canceled
	}
}

func (t *Transport) Close() error {
	t.cancel()
	return nil
}

// ── поллинг сессий ──────────────────────────────────────────────────────────

func (t *Transport) poll() {
	t.client.Mkcol(t.ctx, "tunnel")
	for t.ctx.Err() == nil {
		present, ok := t.discoverSids()
		// Реапим ТОЛЬКО при успешном листинге и лишь после reapMisses промахов
		// подряд. Иначе транзиентная ошибка PROPFIND (429/timeout под нагрузкой)
		// вернула бы пусто → снос known-сессии → повторный pickup того же sid →
		// ДУБЛЬ yamux-сервера на одной c2s/s2c-папке (воруют чанки друг у друга).
		if ok {
			for sid := range t.known {
				if present[sid] {
					t.miss[sid] = 0
					continue
				}
				t.miss[sid]++
				if t.miss[sid] >= reapMisses {
					delete(t.known, sid)
					delete(t.miss, sid)
				}
			}
			for sid := range present {
				if t.known[sid] {
					continue
				}
				if t.hasInit(sid) { // сессия готова (init дописан последним)
					t.known[sid] = true
					t.miss[sid] = 0
					go t.pickup(sid)
				}
			}
		}
		sleepCtx(t.ctx, pollEvery)
	}
}

// discoverSids: PROPFIND depth=1 tunnel/ → множество sid'ов (директорий).
// Второе значение false при ошибке листинга — тогда poll НЕ реапит (см. выше).
func (t *Transport) discoverSids() (map[string]bool, bool) {
	present := map[string]bool{}
	hrefs, err := t.client.Propfind(t.ctx, "tunnel", "1")
	if err != nil {
		return present, false
	}
	for _, h := range hrefs {
		sid := afterTunnel(h)
		if sid == "" || strings.Contains(sid, "/") {
			continue
		}
		present[sid] = true
	}
	return present, true
}

func (t *Transport) hasInit(sid string) bool {
	_, status, _ := t.client.Get(t.ctx, "tunnel/"+sid+"/init")
	return status == 200
}

func (t *Transport) pickup(sid string) {
	if age := t.sessionAge(sid); age >= 0 && age > staleSessionAge {
		// протухшая сессия — снести (init первым, чтобы discover перестал её видеть)
		t.client.Delete(t.ctx, "tunnel/"+sid+"/init")
		t.client.Delete(t.ctx, "tunnel/"+sid)
		return
	}
	// сигнал клиенту «подхватил»
	t.writeSrvHB(sid)

	p := pipe.New(t.client, t.up, sid, "s2c", "c2s", t.key) // сервер: пишет s2c, читает c2s
	p.Start()
	s := &session{Pipe: p, client: t.client, stopHB: make(chan struct{})}
	go t.heartbeat(sid, s.stopHB) // освежаем srv-hb, пока сессия жива
	select {
	case t.sessionCh <- s:
	case <-t.ctx.Done():
		s.Close()
	}
}

func (t *Transport) writeSrvHB(sid string) {
	t.client.Put(t.ctx, "tunnel/"+sid+"/srv-hb",
		[]byte(strconv.FormatInt(time.Now().Unix(), 10)))
}

// heartbeat периодически переписывает srv-hb, чтобы клиент видел сервер живым и
// не рвал сессию по таймауту (иначе реконнект → новый sid → churn/обрыв заливки).
func (t *Transport) heartbeat(sid string, stop <-chan struct{}) {
	tk := time.NewTicker(srvHBEvery)
	defer tk.Stop()
	for {
		select {
		case <-tk.C:
			t.writeSrvHB(sid)
		case <-stop:
			return
		case <-t.ctx.Done():
			return
		}
	}
}

func (t *Transport) sessionAge(sid string) time.Duration {
	data, status, _ := t.client.Get(t.ctx, "tunnel/"+sid+"/hb")
	if status != 200 || len(data) == 0 {
		return -1
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return -1
	}
	return time.Since(time.Unix(ts, 0))
}

// session — Pipe + уборка директории при закрытии (yamux зовёт Close по завершении).
type session struct {
	*pipe.Pipe
	client    *dav.Client
	stopHB    chan struct{}
	closeOnce sync.Once
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopHB) // остановить srv-hb refresher
		s.Pipe.Close()
		s.Pipe.Cleanup() // удалить tunnel/<sid>
	})
	return nil
}

// afterTunnel: путь после ".../tunnel/". Для tunnel/ → "", tunnel/<sid>/ → "<sid>",
// tunnel/<sid>/init → "<sid>/init".
func afterTunnel(href string) string {
	i := strings.Index(href, "tunnel/")
	if i < 0 {
		return ""
	}
	return strings.Trim(href[i+len("tunnel/"):], "/")
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
