// Package dav — доступ к Яндекс.Диску: WebDAV-клиент + (в restup.go) REST-аплоадер.
//
// Порт server/wt/webdav.py. Методы, нужные протоколу туннеля:
// PUT/GET/DELETE/MKCOL/PROPFIND/OPTIONS. Мимикрия под браузер и no-cache —
// чтобы Яндекс/Cloudflare не отдавали устаревшие 404 и не резали трафик.
package dav

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrTargetGone — цель записи исчезла (папка сессии удалена клиентом): 404/409.
// Писателю нет смысла ретраить — сессия мертва, надо сдаться.
var ErrTargetGone = errors.New("target gone (session dir removed)")

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

// RateLimited — 429 от хранилища; Wait — сколько ждать до повтора.
type RateLimited struct{ Wait time.Duration }

func (e *RateLimited) Error() string { return fmt.Sprintf("rate limited, retry after %v", e.Wait) }

func retryAfter(h http.Header) time.Duration {
	ra := strings.TrimSpace(h.Get("Retry-After"))
	if ra == "" {
		return 5 * time.Second
	}
	if secs, err := strconv.Atoi(ra); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(ra); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 5 * time.Second
}

type Client struct {
	base     string
	login    string
	password string
	hc       *http.Client
	lim      *Limiter // общий на аккаунт; nil = без лимита
}

// SetLimiter привязывает общий на аккаунт token-bucket (сглаживает всплески → 429).
func (c *Client) SetLimiter(l *Limiter) { c.lim = l }

// do берёт токен лимитера (если задан), затем выполняет запрос.
func (c *Client) do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if err := c.lim.Wait(ctx); err != nil {
		return nil, err
	}
	return c.hc.Do(req)
}

func NewClient(base, login, password string, timeout time.Duration) *Client {
	tr := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		// HTTP/2 off: часть облаков троттлит/фингерпринтит бот-HTTP/2.
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConnsPerHost:   32,
		MaxConnsPerHost:       32, // bounded per-account — не заваливать один аккаунт
		IdleConnTimeout:       10 * time.Second,
	}
	return &Client{
		base:     strings.TrimRight(base, "/"),
		login:    login,
		password: password,
		hc:       &http.Client{Timeout: timeout, Transport: tr},
	}
}

func (c *Client) url(path string) string {
	if path == "" {
		return c.base
	}
	return c.base + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) newReq(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return nil, err
	}
	r.SetBasicAuth(c.login, c.password)
	r.Header.Set("User-Agent", userAgent)
	return r, nil
}

func noCache(r *http.Request) {
	r.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	r.Header.Set("Pragma", "no-cache")
}

func (c *Client) Put(ctx context.Context, path string, data []byte) error {
	return c.putImpl(ctx, path, data, true)
}

// PutNoLimit — PUT в обход rate-лимитера. Для критичных мелких контрол-файлов
// (srv-hb): liveness-сигнал не должен голодать в очереди за данными файла, иначе
// клиент под нагрузкой видит протухший srv-hb и рвёт сессию.
func (c *Client) PutNoLimit(ctx context.Context, path string, data []byte) error {
	return c.putImpl(ctx, path, data, false)
}

func (c *Client) putImpl(ctx context.Context, path string, data []byte, limited bool) error {
	r, err := c.newReq(ctx, "PUT", path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.ContentLength = int64(len(data))
	var resp *http.Response
	if limited {
		resp, err = c.do(ctx, r)
	} else {
		resp, err = c.hc.Do(r)
	}
	if err != nil {
		return err
	}
	drain(resp)
	if resp.StatusCode == 429 {
		return &RateLimited{retryAfter(resp.Header)}
	}
	if resp.StatusCode == 404 || resp.StatusCode == 409 {
		return fmt.Errorf("PUT %s: %s: %w", path, resp.Status, ErrTargetGone)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("PUT %s: %s", path, resp.Status)
	}
	return nil
}

// Get возвращает тело и HTTP-статус. 404 → (nil, 404, nil).
func (c *Client) Get(ctx context.Context, path string) ([]byte, int, error) {
	r, err := c.newReq(ctx, "GET", path, nil)
	if err != nil {
		return nil, 0, err
	}
	noCache(r)
	resp, err := c.do(ctx, r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		io.Copy(io.Discard, resp.Body)
		return nil, 429, &RateLimited{retryAfter(resp.Header)}
	}
	if resp.StatusCode == 404 {
		io.Copy(io.Discard, resp.Body)
		return nil, 404, nil
	}
	if resp.StatusCode >= 400 {
		io.Copy(io.Discard, resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

func (c *Client) Delete(ctx context.Context, path string) error {
	// 423 Locked: Яндекс кратко лочит папку при параллельных операциях — короткий ретрай.
	for attempt := 0; attempt < 4; attempt++ {
		r, err := c.newReq(ctx, "DELETE", path, nil)
		if err != nil {
			return err
		}
		resp, err := c.do(ctx, r)
		if err != nil {
			return err
		}
		drain(resp)
		if resp.StatusCode == 429 {
			return &RateLimited{retryAfter(resp.Header)}
		}
		if resp.StatusCode == 423 && attempt < 3 {
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
			continue
		}
		if resp.StatusCode >= 400 && resp.StatusCode != 404 && resp.StatusCode != 423 {
			return fmt.Errorf("DELETE %s: %s", path, resp.Status)
		}
		return nil
	}
	return nil
}

func (c *Client) Mkcol(ctx context.Context, path string) error {
	r, err := c.newReq(ctx, "MKCOL", path, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	drain(resp)
	if resp.StatusCode == 429 {
		return &RateLimited{retryAfter(resp.Header)}
	}
	// 405 = уже есть, 409 = нет родителя (best-effort).
	if resp.StatusCode >= 400 && resp.StatusCode != 405 && resp.StatusCode != 409 {
		return fmt.Errorf("MKCOL %s: %s", path, resp.Status)
	}
	return nil
}

// Propfind возвращает href'ы вложений. depth: "1" или "2".
func (c *Client) Propfind(ctx context.Context, path, depth string) ([]string, error) {
	if !strings.HasSuffix(path, "/") {
		path += "/" // без слэша Apache отдаёт 301 → GET → HTML
	}
	body := `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/></D:prop></D:propfind>`
	r, err := c.newReq(ctx, "PROPFIND", path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Depth", depth)
	r.Header.Set("Content-Type", "application/xml")
	noCache(r)
	resp, err := c.do(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		io.Copy(io.Discard, resp.Body)
		return nil, &RateLimited{retryAfter(resp.Header)}
	}
	if resp.StatusCode == 404 {
		io.Copy(io.Discard, resp.Body)
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("PROPFIND %s: %s", path, resp.Status)
	}
	var ms struct {
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		out = append(out, r.Href)
	}
	return out, nil
}

// Ping — проверка связи/авторизации (OPTIONS).
func (c *Client) Ping(ctx context.Context) error {
	r, err := c.newReq(ctx, "OPTIONS", "", nil)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	drain(resp)
	if resp.StatusCode == 401 {
		return fmt.Errorf("authentication failed (401)")
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("OPTIONS: %s", resp.Status)
	}
	return nil
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// LastSegment — последний сегмент href'а (sid из tunnel/<sid>/).
func LastSegment(href string) string {
	s := strings.TrimRight(href, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}
