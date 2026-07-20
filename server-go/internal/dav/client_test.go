package dav

import (
	"context"
	"os"
	"testing"
	"time"
)

// live WebDAV round-trip; скип без кредов в env.
func liveClient(t *testing.T) *Client {
	url := os.Getenv("WEBDAV_URL")
	login := os.Getenv("WEBDAV_LOGIN")
	pw := os.Getenv("WEBDAV_PASSWORD")
	if url == "" || login == "" || pw == "" {
		t.Skip("WEBDAV_URL/LOGIN/PASSWORD unset")
	}
	return NewClient(url, login, pw, 60*time.Second)
}

func TestLiveRoundTrip(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := c.Mkcol(ctx, "gochk"); err != nil {
		t.Fatalf("mkcol: %v", err)
	}
	payload := []byte("\x00\x01go-webdav-roundtrip\xff")
	if err := c.Put(ctx, "gochk/a.bin", payload); err != nil {
		t.Fatalf("put: %v", err)
	}
	data, status, err := c.Get(ctx, "gochk/a.bin")
	if err != nil || status != 200 || string(data) != string(payload) {
		t.Fatalf("get: status=%d err=%v match=%v", status, err, string(data) == string(payload))
	}
	if _, status, _ := c.Get(ctx, "gochk/missing.bin"); status != 404 {
		t.Fatalf("expected 404 for missing, got %d", status)
	}
	if hrefs, err := c.Propfind(ctx, "gochk", "1"); err != nil || len(hrefs) == 0 {
		t.Fatalf("propfind: err=%v n=%d", err, len(hrefs))
	}
	if err := c.Delete(ctx, "gochk/a.bin"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := c.Delete(ctx, "gochk"); err != nil {
		t.Fatalf("delete dir: %v", err)
	}
}
