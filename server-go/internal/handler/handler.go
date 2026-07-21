// Package handler — что делаем с клиентской сессией. Ортогонально транспорту:
//
//	MVP1 (WebDAV) : YamuxPassthrough — yamux, per-stream [host][port] + dial + relay.
//	MVP2 (olcRTC) : VLESSBridge     — мост сессии в VLESS-цепочку (xray делает роутинг).
//
// Транспорт × Handler = точка расширения разворота.
package handler

import (
	"context"
	"encoding/binary"
	"io"
	"log"
	"net"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/Perebevaska/vpm/server-go/internal/egress"
	"github.com/Perebevaska/vpm/server-go/internal/transport"
)

// SessionHandler обслуживает одну сессию до её закрытия.
type SessionHandler interface {
	Serve(ctx context.Context, s transport.Session)
}

func yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.EnableKeepAlive = true
	c.ConnectionWriteTimeout = 120 * time.Second
	// Приёмное окно c2s (upload): держим узким, чтобы клиент не заливал наперёд
	// мегабайты в Диск быстрее, чем сервер их дренит в egress — иначе чанки
	// копятся, растёт request-churn на аккаунте и Яндекс троттлит. yamux растит
	// окно WindowUpdate'ами до этого потолка; апгрейд окна клиента принимаем как есть.
	c.MaxStreamWindowSize = 4 * 1024 * 1024
	c.LogOutput = io.Discard
	return c
}

// ── MVP1: WebDAV passthrough ────────────────────────────────────────────────

// YamuxPassthrough — модель MVP1: клиент мультиплексирует TCP через yamux,
// каждый поток начинается адресом [2 host_len][host][2 port], дальше сырой релей.
type YamuxPassthrough struct {
	Dialer egress.Dialer
}

func (h YamuxPassthrough) Serve(ctx context.Context, s transport.Session) {
	ys, err := yamux.Server(s, yamuxConfig())
	if err != nil {
		log.Printf("[%s] yamux server: %v", s.ID(), err)
		return
	}
	defer ys.Close()
	for {
		stream, err := ys.Accept()
		if err != nil {
			return
		}
		go h.serveStream(ctx, s.ID(), stream)
	}
}

func (h YamuxPassthrough) serveStream(ctx context.Context, sid string, stream net.Conn) {
	defer stream.Close()
	host, port, err := readTarget(stream)
	if err != nil {
		return
	}
	log.Printf("[%s] connect %s:%d", sid, host, port)
	up, err := h.Dialer.Dial(ctx, host, port)
	if err != nil {
		log.Printf("[%s] dial %s:%d: %v", sid, host, port, err)
		return
	}
	defer up.Close()
	relay(stream, up)
}

// ── MVP2: olcRTC → VLESS bridge (заглушка) ──────────────────────────────────

// VLESSBridge — модель MVP2: сессия несёт VLESS-трафик, мостим её в цепочку
// (xray на server1/server3 делает роутинг и egress). Здесь — точка реализации.
type VLESSBridge struct {
	ChainAddr string // host:port VLESS-инбаунда цепочки
}

func (h VLESSBridge) Serve(ctx context.Context, s transport.Session) {
	// TODO(MVP2): установить TCP к h.ChainAddr и прокинуть сессию 1:1
	// (io.Copy в обе стороны). yamux здесь не нужен — мультиплексирует xray.
	log.Printf("[%s] VLESSBridge: not implemented (MVP2)", s.ID())
	_ = s.Close()
}

// ── общее ───────────────────────────────────────────────────────────────────

// readTarget: [2 BE host_len][host][2 BE port].
func readTarget(r io.Reader) (string, uint16, error) {
	var lb [2]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return "", 0, err
	}
	hb := make([]byte, binary.BigEndian.Uint16(lb[:]))
	if _, err := io.ReadFull(r, hb); err != nil {
		return "", 0, err
	}
	var pb [2]byte
	if _, err := io.ReadFull(r, pb[:]); err != nil {
		return "", 0, err
	}
	return string(hb), binary.BigEndian.Uint16(pb[:]), nil
}

// relay двунаправленно копирует между потоком и egress. При EOF одной стороны
// делаем half-close (CloseWrite) на другую и ждём ЗАВЕРШЕНИЯ обоих направлений —
// иначе закрытие по первому EOF обрубает ещё летящие данные встречного потока.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite() // сигналим EOF пиру, встречное направление ещё дренится
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
