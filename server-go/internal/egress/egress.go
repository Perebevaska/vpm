// Package egress — выход трафика сервера: напрямую или через upstream SOCKS5
// (в цепочку bypass-in → vless-reality). Транспорт-агностичен.
package egress

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"time"
)

// Dialer открывает соединение к цели. host может быть именем — резолв делает
// сторона выхода (для SOCKS5 — цепочка).
type Dialer interface {
	Dial(ctx context.Context, host string, port uint16) (net.Conn, error)
}

// New строит Dialer из строки прокси; пусто → Direct.
func New(proxy string) (Dialer, error) {
	if proxy == "" {
		return Direct{}, nil
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Scheme != "socks5" || u.Host == "" {
		return nil, fmt.Errorf("egress proxy must be socks5://[user:pass@]host:port")
	}
	s := &SOCKS5{Addr: u.Host}
	if u.User != nil {
		s.User = u.User.Username()
		s.Pass, _ = u.User.Password()
	}
	return s, nil
}

// Direct — прямой выход (exit = IP этой машины).
type Direct struct{}

func (Direct) Dial(ctx context.Context, host string, port uint16) (net.Conn, error) {
	d := &net.Dialer{}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
}

// SOCKS5 — выход через upstream SOCKS5, CONNECT с domain ATYP.
type SOCKS5 struct {
	Addr string
	User string
	Pass string
}

func (s *SOCKS5) Dial(ctx context.Context, host string, port uint16) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second}
	c, err := d.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return nil, err
	}
	if err := s.handshake(c, host, port); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (s *SOCKS5) handshake(c net.Conn, host string, port uint16) error {
	methods := []byte{0x00}
	if s.User != "" {
		methods = append(methods, 0x02)
	}
	if _, err := c.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	rep := make([]byte, 2)
	if _, err := readFull(c, rep); err != nil {
		return err
	}
	if rep[0] != 0x05 {
		return fmt.Errorf("socks: bad version")
	}
	switch rep[1] {
	case 0x00:
	case 0x02:
		u, p := []byte(s.User), []byte(s.Pass)
		msg := append([]byte{0x01, byte(len(u))}, u...)
		msg = append(append(msg, byte(len(p))), p...)
		if _, err := c.Write(msg); err != nil {
			return err
		}
		st := make([]byte, 2)
		if _, err := readFull(c, st); err != nil {
			return err
		}
		if st[1] != 0x00 {
			return fmt.Errorf("socks: auth failed")
		}
	default:
		return fmt.Errorf("socks: no acceptable auth method")
	}

	hb := []byte(host)
	req := append([]byte{0x05, 0x01, 0x00, 0x03, byte(len(hb))}, hb...)
	req = binary.BigEndian.AppendUint16(req, port)
	if _, err := c.Write(req); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err := readFull(c, head); err != nil {
		return err
	}
	if head[1] != 0x00 {
		return fmt.Errorf("socks: connect failed (rep=%d)", head[1])
	}
	var skip int
	switch head[3] {
	case 0x01:
		skip = 4 + 2
	case 0x04:
		skip = 16 + 2
	case 0x03:
		ln := make([]byte, 1)
		if _, err := readFull(c, ln); err != nil {
			return err
		}
		skip = int(ln[0]) + 2
	default:
		return fmt.Errorf("socks: bad ATYP in reply")
	}
	return discard(c, skip)
}

func readFull(c net.Conn, b []byte) (int, error) {
	got := 0
	for got < len(b) {
		n, err := c.Read(b[got:])
		if err != nil {
			return got, err
		}
		got += n
	}
	return got, nil
}

func discard(c net.Conn, n int) error {
	buf := make([]byte, n)
	_, err := readFull(c, buf)
	return err
}
