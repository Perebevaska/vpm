package dav

import (
	"context"
	"os"
	"testing"
	"time"
)

// REST upload → WebDAV read (доказательство wire-совместимости write-пути).
func TestLiveRestToWebDAV(t *testing.T) {
	url := os.Getenv("WEBDAV_URL")
	login := os.Getenv("WEBDAV_LOGIN")
	pw := os.Getenv("WEBDAV_PASSWORD")
	token := os.Getenv("YANDEX_OAUTH_TOKEN")
	if url == "" || login == "" || pw == "" || token == "" {
		t.Skip("WEBDAV_* / YANDEX_OAUTH_TOKEN unset")
	}
	ctx := context.Background()
	c := NewClient(url, login, pw, 60*time.Second)
	u := NewRestUploader(token, 60*time.Second)

	c.Mkcol(ctx, "gorest")
	blob := []byte("\x00REST-uploaded-by-go\xff")
	if err := u.Put(ctx, "gorest/c.bin", blob); err != nil {
		t.Fatalf("rest put: %v", err)
	}
	data, status, err := c.Get(ctx, "gorest/c.bin")
	if err != nil || status != 200 || string(data) != string(blob) {
		t.Fatalf("webdav read back: status=%d err=%v match=%v", status, err, string(data) == string(blob))
	}
	c.Delete(ctx, "gorest/c.bin")
	c.Delete(ctx, "gorest")
}
