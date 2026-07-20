// Package pipe — дуплексный байт-канал поверх нумерованных чанк-файлов WebDAV.
//
// Порт server/wt/pipe.py на горутины. Реализует transport.Session
// (io.ReadWriteCloser + ID) — поверх сидит yamux.
//
//	writer  — единый flusher-горутина коалесцирует байты, режет на чанки
//	          <= ChunkDataSize, [header|ts]+enc, аплоад пулом (Uploader).
//	          seq строго непрерывны (иначе читатель встанет), EOF — последним.
//	reader  — read-ahead окно параллельных GET, переупорядочение по seq,
//	          снятие заголовка, доставка по порядку в inbound-канал.
package pipe

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Perebevaska/vpm/server-go/internal/crypto"
	"github.com/Perebevaska/vpm/server-go/internal/dav"
)

// Тюнинг-дефолты (замер #8; см. DESIGN.md). Латентно-связаны → шире параллелизм.
const (
	ChunkDataSize = 256*1024 - 1
	ReadAhead     = 16
	PutWorkers    = 16
	PollMin       = 50 * time.Millisecond
	PollMax       = 300 * time.Millisecond
	CoalesceDelay = 10 * time.Millisecond
	IdleTimeout   = 90 * time.Second

	putMaxAttempts = 15
	headerData     = 0x00
	headerEOF      = 0x01
)

func chunkPath(sid, dir string, seq int64) string {
	// tunnel/<sid>/<dir>/%010d.bin
	return "tunnel/" + sid + "/" + dir + "/" + pad10(seq) + ".bin"
}

func pad10(n int64) string {
	b := []byte("0000000000")
	i := len(b) - 1
	for n > 0 && i >= 0 {
		b[i] = byte('0' + n%10)
		n /= 10
		i--
	}
	return string(b)
}

type Pipe struct {
	dav      *dav.Client
	up       dav.Uploader
	sid      string
	writeDir string
	readDir  string
	key      []byte

	ctx    context.Context
	cancel context.CancelFunc

	// writer
	outMu      sync.Mutex
	outBuf     []byte
	writeSeq   int64 // только flusher-горутина
	outEOFSent bool
	putSem     chan struct{}
	putWg      sync.WaitGroup

	finish      chan struct{}
	finishOnce  sync.Once
	flusherDone chan struct{}

	// inbound
	inCh           chan []byte
	leftover       []byte
	readClosed     chan struct{}
	readClosedOnce sync.Once

	closed  atomic.Bool
	started bool
}

// New создаёт пайп. up — аплоадер write-пути (WebDAV PUT или REST).
func New(client *dav.Client, up dav.Uploader, sid, writeDir, readDir string, key []byte) *Pipe {
	ctx, cancel := context.WithCancel(context.Background())
	return &Pipe{
		dav: client, up: up, sid: sid, writeDir: writeDir, readDir: readDir, key: key,
		ctx: ctx, cancel: cancel,
		putSem:      make(chan struct{}, PutWorkers),
		finish:      make(chan struct{}),
		flusherDone: make(chan struct{}),
		inCh:        make(chan []byte, 128),
		readClosed:  make(chan struct{}),
	}
}

func (p *Pipe) ID() string { return p.sid }

func (p *Pipe) Start() {
	if p.started {
		return
	}
	p.started = true
	go p.runFlusher()
	go p.runReader()
}

// ── io.ReadWriteCloser ──────────────────────────────────────────────────────

func (p *Pipe) Read(buf []byte) (int, error) {
	if len(p.leftover) > 0 {
		n := copy(buf, p.leftover)
		p.leftover = p.leftover[n:]
		return n, nil
	}
	// сперва без блокировки — не потерять буферизованные сегменты при закрытии
	select {
	case seg := <-p.inCh:
		return p.take(buf, seg), nil
	default:
	}
	t := time.NewTimer(IdleTimeout)
	defer t.Stop()
	select {
	case seg := <-p.inCh:
		return p.take(buf, seg), nil
	case <-p.readClosed:
		return 0, io.EOF
	case <-p.ctx.Done():
		return 0, io.EOF
	case <-t.C:
		return 0, io.EOF // idle timeout — сессия мертва
	}
}

func (p *Pipe) take(buf, seg []byte) int {
	n := copy(buf, seg)
	if n < len(seg) {
		p.leftover = seg[n:]
	}
	return n
}

func (p *Pipe) Write(data []byte) (int, error) {
	select {
	case <-p.finish:
		return 0, io.ErrClosedPipe
	default:
	}
	p.outMu.Lock()
	p.outBuf = append(p.outBuf, data...)
	p.outMu.Unlock()
	return len(data), nil
}

func (p *Pipe) Close() error {
	p.finishOnce.Do(func() { close(p.finish) })
	if p.started {
		waitCh(p.flusherDone, 20*time.Second) // финальный флаш + EOF эмитнуты
		wg := make(chan struct{})
		go func() { p.putWg.Wait(); close(wg) }()
		waitCh(wg, 15*time.Second) // EOF доставлен приёмнику
	}
	p.closed.Store(true)
	p.cancel()
	p.readClosedOnce.Do(func() { close(p.readClosed) })
	return nil
}

// Cleanup удаляет директорию сессии с Диска (свежий ctx — p.ctx уже отменён).
func (p *Pipe) Cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p.dav.Delete(ctx, "tunnel/"+p.sid)
}

// ── writer ──────────────────────────────────────────────────────────────────

func (p *Pipe) runFlusher() {
	defer close(p.flusherDone)
	t := time.NewTicker(CoalesceDelay)
	defer t.Stop()
	for {
		select {
		case <-p.finish:
			p.flush(true, true)
			return
		case <-t.C:
			p.flush(true, false) // по тику шлём и неполный хвост (иначе завис бы до Close)
		}
	}
}

func (p *Pipe) flush(force, sendEOF bool) {
	for {
		p.outMu.Lock()
		if len(p.outBuf) == 0 {
			p.outMu.Unlock()
			break
		}
		if !force && len(p.outBuf) < ChunkDataSize {
			p.outMu.Unlock()
			break
		}
		take := len(p.outBuf)
		if take > ChunkDataSize {
			take = ChunkDataSize
		}
		data := make([]byte, take)
		copy(data, p.outBuf[:take])
		p.outBuf = p.outBuf[take:]
		p.outMu.Unlock()
		p.emitChunk(headerData, data)
	}
	if sendEOF && !p.outEOFSent {
		p.outEOFSent = true
		p.emitChunk(headerEOF, nil)
	}
}

func (p *Pipe) emitChunk(header byte, data []byte) {
	p.writeSeq++ // единственный flusher → без атомиков, seq непрерывны
	seq := p.writeSeq
	payload := make([]byte, 9+len(data))
	payload[0] = header
	binary.BigEndian.PutUint64(payload[1:9], uint64(time.Now().UnixNano()))
	copy(payload[9:], data)
	p.putWg.Add(1)
	go p.putChunk(seq, payload)
}

func (p *Pipe) putChunk(seq int64, payload []byte) {
	defer p.putWg.Done()
	p.putSem <- struct{}{}
	defer func() { <-p.putSem }()

	body := payload
	if len(p.key) > 0 {
		b, err := crypto.EncryptChunk(p.key, payload)
		if err != nil {
			p.fail()
			return
		}
		body = b
	}
	path := chunkPath(p.sid, p.writeDir, seq)
	backoff := 500 * time.Millisecond
	attempts := 0
	for !p.closed.Load() {
		err := p.up.Put(p.ctx, path, body)
		if err == nil {
			return
		}
		if p.ctx.Err() != nil {
			return
		}
		var rl *dav.RateLimited
		if errors.As(err, &rl) {
			sleepCtx(p.ctx, rl.Wait)
			continue
		}
		attempts++
		if attempts >= putMaxAttempts {
			p.fail() // иначе читатель встанет на этом seq
			return
		}
		sleepCtx(p.ctx, backoff)
		backoff = time.Duration(float64(backoff) * 1.5)
		if backoff > 10*time.Second {
			backoff = 10 * time.Second
		}
	}
}

// ── reader ──────────────────────────────────────────────────────────────────

func (p *Pipe) runReader() {
	type res struct {
		seq     int64
		payload []byte
		ok      bool
	}
	fetchDone := make(chan res, ReadAhead+4)
	var nextDeliver, nextFetch int64 = 1, 1
	inflight := 0
	results := map[int64][]byte{}

	launch := func() {
		seq := nextFetch
		nextFetch++
		inflight++
		go func() {
			payload, ok := p.fetchChunk(seq)
			select {
			case fetchDone <- res{seq, payload, ok}:
			case <-p.ctx.Done():
			}
		}()
	}
	for inflight < ReadAhead {
		launch()
	}

	for {
		select {
		case <-p.ctx.Done():
			return
		case r := <-fetchDone:
			inflight--
			if !r.ok {
				p.setEOF()
				return
			}
			results[r.seq] = r.payload
			launch()
			for {
				payload, ok := results[nextDeliver]
				if !ok {
					break
				}
				delete(results, nextDeliver)
				if payload[0] == headerEOF {
					p.setEOF()
					return
				}
				if !p.deliver(payload[9:]) {
					return
				}
				nextDeliver++
			}
		}
	}
}

func (p *Pipe) fetchChunk(seq int64) ([]byte, bool) {
	path := chunkPath(p.sid, p.readDir, seq)
	backoff := PollMin
	for !p.closed.Load() {
		if p.ctx.Err() != nil {
			return nil, false
		}
		data, status, err := p.dav.Get(p.ctx, path)
		var rl *dav.RateLimited
		if errors.As(err, &rl) {
			if !sleepCtx(p.ctx, rl.Wait) {
				return nil, false
			}
			continue
		}
		needRetry := err != nil || status == 404
		payload := data
		if !needRetry && len(p.key) > 0 {
			dec, derr := crypto.DecryptChunk(p.key, data)
			if derr != nil {
				needRetry = true // ещё дозаписывается / битый
			} else {
				payload = dec
			}
		}
		if !needRetry && len(payload) < 9 {
			needRetry = true
		}
		if needRetry {
			wait := backoff + time.Duration(rand.Int63n(int64(backoff/2+1)))
			backoff *= 2
			if backoff > PollMax {
				backoff = PollMax
			}
			if !sleepCtx(p.ctx, wait) {
				return nil, false
			}
			continue
		}
		go func() { // удалить прочитанный чанк (best-effort)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			p.dav.Delete(ctx, path)
		}()
		return payload, true
	}
	return nil, false
}

// deliver возвращает false, если пайп закрыт (доставка невозможна).
func (p *Pipe) deliver(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	seg := make([]byte, len(data))
	copy(seg, data)
	select {
	case p.inCh <- seg:
		return true
	case <-p.ctx.Done():
		return false
	}
}

func (p *Pipe) setEOF() {
	p.readClosedOnce.Do(func() { close(p.readClosed) })
}

func (p *Pipe) fail() {
	p.closed.Store(true)
	p.cancel()
	p.setEOF()
}

// ── helpers ─────────────────────────────────────────────────────────────────

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func waitCh(done <-chan struct{}, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}
