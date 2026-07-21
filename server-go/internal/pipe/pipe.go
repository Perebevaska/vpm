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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Perebevaska/vpm/server-go/internal/crypto"
	"github.com/Perebevaska/vpm/server-go/internal/dav"
)

// Тюнинг-дефолты (замер #8; см. DESIGN.md). Латентно-связаны → шире параллелизм.
const (
	// s2c-чанк = размеру клиентского чанка (131071, дефолт PROTOCOL). Крупнее
	// (пробовали 1МБ) ломает: клиент под свой chunk-size режет/буферит c2s и
	// читает s2c — 1МБ s2c-файлы мешали хендшейку прокси (Telegram не коннектился).
	// Размер должен совпадать со стороной клиента, а не оптимизироваться в одну.
	ChunkDataSize = 128*1024 - 1
	// Параллелизм скромный: канал резервный, текстовый (КБ, не МБ). Много
	// воркеров = всплеск запросов (троттл Диска) + реордеринг (медленный PUT
	// одного seq стопорит in-order читателя). 4 хватает.
	ReadAhead  = 4
	PutWorkers = 4
	PollMin    = 50 * time.Millisecond
	PollMax    = 300 * time.Millisecond
	// Каденция discovery-PROPFIND. Отдельная от fetch-ретраев. Компромисс
	// латентность↔нагрузка: один листинг возвращает ВСЕ готовые c2s-чанки, так
	// что даже частый PROPFIND дешевле старого blind read-ahead (16 GET/цикл).
	// Backoff в простое держим коротким (400мс, не 1с) — иначе интерактивный
	// c2s ждёт до backoff перед забором → пинг в секундах.
	DiscoverMin   = 120 * time.Millisecond
	DiscoverMax   = 400 * time.Millisecond
	CoalesceDelay = 10 * time.Millisecond
	// Idle-таймаут длинный: резервный канал подолгу простаивает в ожидании
	// редких сообщений. Keepalive сервера выключен (см. handler), поэтому idle —
	// единственный сторож мёртвой сессии; 90с рвало бы живой idle-туннель.
	IdleTimeout = 5 * time.Minute

	deleteWorkers = 2
	deleteBuf     = 512

	// Backpressure: очередь на аплоад ограничена, Write блокирует при переполнении
	// outBuf. Иначе под rate-limit'ом (аплоад медленнее записи) буфер и число
	// putChunk-горутин росли без границы → runaway RSS (видели 118МБ на 11МБ файле).
	putQueueLen = 8
	maxOutBuf   = 512 * 1024

	headerData = 0x00
	headerEOF  = 0x01
)

type putJob struct {
	seq     int64
	payload []byte
}

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
	outCond    *sync.Cond // будит Write, когда flusher слил outBuf (backpressure)
	outBuf     []byte
	writeSeq   int64 // только flusher-горутина
	outEOFSent bool
	putCh      chan putJob // очередь на аплоад (ограничена → backpressure)
	putWg      sync.WaitGroup

	finish      chan struct{}
	finishOnce  sync.Once
	flusherDone chan struct{}

	// inbound
	inCh           chan []byte
	leftover       []byte
	readClosed     chan struct{}
	readClosedOnce sync.Once

	delCh chan string // прочитанные чанки на удаление (bounded rate)

	closed  atomic.Bool
	started bool
}

// New создаёт пайп. up — аплоадер write-пути (WebDAV PUT или REST).
func New(client *dav.Client, up dav.Uploader, sid, writeDir, readDir string, key []byte) *Pipe {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipe{
		dav: client, up: up, sid: sid, writeDir: writeDir, readDir: readDir, key: key,
		ctx: ctx, cancel: cancel,
		putCh:       make(chan putJob, putQueueLen),
		finish:      make(chan struct{}),
		flusherDone: make(chan struct{}),
		inCh:        make(chan []byte, 128),
		readClosed:  make(chan struct{}),
		delCh:       make(chan string, deleteBuf),
	}
	p.outCond = sync.NewCond(&p.outMu)
	return p
}

func (p *Pipe) ID() string { return p.sid }

func (p *Pipe) Start() {
	if p.started {
		return
	}
	p.started = true
	go p.runFlusher()
	go p.runReader()
	for i := 0; i < PutWorkers; i++ {
		go p.putWorker()
	}
	for i := 0; i < deleteWorkers; i++ {
		go p.runDeleter()
	}
}

// putWorker — фикс. пул аплоадеров. Пул + ограниченная putCh = верхняя граница
// незалитых чанков в памяти (backpressure вместо неограниченного числа горутин).
func (p *Pipe) putWorker() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case job := <-p.putCh:
			p.doPut(job.seq, job.payload)
			p.putWg.Done()
		}
	}
}

// runDeleter — bounded удаление прочитанных чанков. Ограничение числа воркеров
// держит DELETE-rate низким (иначе на каждый прочитанный чанк летел отдельный
// DELETE-запрос → лишний churn и троттлинг Диска).
func (p *Pipe) runDeleter() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case path := <-p.delCh:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			p.dav.Delete(ctx, path)
			cancel()
		}
	}
}

// enqueueDelete ставит чанк в очередь на удаление без блокировки читателя;
// при переполнении буфера чанк пропускается — итоговую уборку сделает Cleanup.
func (p *Pipe) enqueueDelete(path string) {
	select {
	case p.delCh <- path:
	default:
	}
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
	// Backpressure: ждём, пока flusher сольёт буфер ниже порога. Иначе под
	// rate-limit'ом (аплоад медленнее записи) outBuf растёт без границы.
	for len(p.outBuf) >= maxOutBuf {
		select {
		case <-p.finish:
			p.outMu.Unlock()
			return 0, io.ErrClosedPipe
		default:
		}
		p.outCond.Wait() // отпускает outMu, будится flush'ем / Close'ом
	}
	p.outBuf = append(p.outBuf, data...)
	p.outMu.Unlock()
	return len(data), nil
}

func (p *Pipe) Close() error {
	p.finishOnce.Do(func() {
		close(p.finish)
		p.outMu.Lock()
		p.outCond.Broadcast() // разбудить Write, застрявшие на backpressure
		p.outMu.Unlock()
	})
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
		p.outCond.Broadcast() // буфер сжался → разбудить ждущие Write
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
	select {
	case p.putCh <- putJob{seq, payload}: // блокирует при полной очереди = backpressure
	case <-p.ctx.Done():
		p.putWg.Done()
	}
}

func (p *Pipe) doPut(seq int64, payload []byte) {
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
	for !p.closed.Load() {
		err := p.up.Put(p.ctx, path, body)
		if err == nil {
			return
		}
		if p.ctx.Err() != nil {
			return
		}
		if errors.Is(err, dav.ErrTargetGone) {
			return // папка сессии удалена клиентом — не льём в мёртвую сессию
		}
		var rl *dav.RateLimited
		if errors.As(err, &rl) {
			sleepCtx(p.ctx, rl.Wait)
			continue
		}
		// Транзиентная ошибка (таймаут/5xx/сброс — часто под rate-limit) НЕ
		// роняет сессию: раньше fail() после 15 попыток убивал весь туннель
		// из-за одного чанка. Ретраим с capped backoff; мёртвого пира отсечёт
		// idle-таймаут или реконнект клиента.
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
	availCh := make(chan int64, ReadAhead*4)
	go p.runDiscover(availCh)

	fetchDone := make(chan res, ReadAhead+4)
	var nextDeliver int64 = 1
	inflight := 0
	pending := map[int64]bool{}   // обнаружены на Диске, ещё не качаем
	fetching := map[int64]bool{}  // GET в процессе
	results := map[int64][]byte{} // скачаны, ждут доставки по порядку

	launch := func() {
		for inflight < ReadAhead {
			best := int64(-1) // младший pending в окне [nextDeliver, +ReadAhead)
			for seq := range pending {
				if seq < nextDeliver+ReadAhead && (best < 0 || seq < best) {
					best = seq
				}
			}
			if best < 0 {
				return
			}
			delete(pending, best)
			fetching[best] = true
			inflight++
			seq := best
			go func() {
				payload, ok := p.fetchChunk(seq)
				select {
				case fetchDone <- res{seq, payload, ok}:
				case <-p.ctx.Done():
				}
			}()
		}
	}

	for {
		select {
		case <-p.ctx.Done():
			return
		case seq := <-availCh:
			if seq >= nextDeliver && !fetching[seq] && !pending[seq] {
				if _, done := results[seq]; !done {
					pending[seq] = true
				}
			}
			launch()
		case r := <-fetchDone:
			inflight--
			delete(fetching, r.seq)
			if !r.ok { // fetchChunk сдаётся только при отмене ctx/закрытии
				p.setEOF()
				return
			}
			results[r.seq] = r.payload
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
			launch() // окно могло сдвинуться → добрать pending
		}
	}
}

// runDiscover периодически листит readDir (PROPFIND) и шлёт номера присутствующих
// чанков в availCh. Один листинг вместо слепого read-ahead из ReadAhead GET'ов по
// ещё-не-записанным seq — на порядок меньше запросов к Диску. Интервал адаптивный.
func (p *Pipe) runDiscover(availCh chan<- int64) {
	backoff := DiscoverMin
	for {
		if p.ctx.Err() != nil {
			return
		}
		seqs, ok := p.listReadDir()
		if ok && len(seqs) > 0 {
			backoff = DiscoverMin
			for _, s := range seqs {
				select {
				case availCh <- s:
				case <-p.ctx.Done():
					return
				}
			}
		} else {
			backoff *= 2
			if backoff > DiscoverMax {
				backoff = DiscoverMax
			}
		}
		if !sleepCtx(p.ctx, backoff) {
			return
		}
	}
}

// listReadDir возвращает seq'ы .bin-чанков в readDir. Второе значение — успех
// листинга (false при ошибке/429 — тогда discover просто ждёт и повторит).
func (p *Pipe) listReadDir() ([]int64, bool) {
	hrefs, err := p.dav.Propfind(p.ctx, "tunnel/"+p.sid+"/"+p.readDir, "1")
	if err != nil {
		return nil, false
	}
	out := make([]int64, 0, len(hrefs))
	for _, h := range hrefs {
		name := dav.LastSegment(h)
		if !strings.HasSuffix(name, ".bin") {
			continue
		}
		n, perr := strconv.ParseInt(strings.TrimSuffix(name, ".bin"), 10, 64)
		if perr != nil {
			continue
		}
		out = append(out, n)
	}
	return out, true
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
		p.enqueueDelete(path) // удалить прочитанный чанк (bounded rate, best-effort)
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
