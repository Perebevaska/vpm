package dav

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const restBase = "https://cloud-api.yandex.net/v1/disk"

// Uploader — аплоад чанка по пути (относительно корня Диска). Реализуют и
// *Client (WebDAV PUT), и *RestUploader (Yandex REST) — Pipe использует любой.
type Uploader interface {
	Put(ctx context.Context, path string, data []byte) error
}

// RestUploader льёт чанки через Yandex REST API (быстрее WebDAV на запись, ×3.7
// по замеру #8). Файл ложится по тому же пути → WireTurn читает его WebDAV-GET.
type RestUploader struct {
	token string
	hc    *http.Client
}

func NewRestUploader(oauthToken string, timeout time.Duration) *RestUploader {
	return &RestUploader{token: oauthToken, hc: &http.Client{Timeout: timeout}}
}

// Put: GET presigned href (OAuth) → PUT тела.
func (u *RestUploader) Put(ctx context.Context, path string, data []byte) error {
	diskPath := "/" + strings.TrimLeft(path, "/")

	// шаг 1 — href
	q := url.Values{"path": {diskPath}, "overwrite": {"true"}}
	req, err := http.NewRequestWithContext(ctx, "GET", restBase+"/resources/upload?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "OAuth "+u.token)
	resp, err := u.hc.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode == 429 {
		drain(resp)
		return &RateLimited{retryAfter(resp.Header)}
	}
	if resp.StatusCode != 200 {
		drain(resp)
		return fmt.Errorf("REST upload-href %s: %s", path, resp.Status)
	}
	var href struct {
		Href string `json:"href"`
	}
	dErr := json.NewDecoder(resp.Body).Decode(&href)
	resp.Body.Close()
	if dErr != nil {
		return dErr
	}

	// шаг 2 — PUT тела (href уже авторизован)
	put, err := http.NewRequestWithContext(ctx, "PUT", href.Href, bytes.NewReader(data))
	if err != nil {
		return err
	}
	put.ContentLength = int64(len(data))
	pr, err := u.hc.Do(put)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, pr.Body)
	pr.Body.Close()
	if pr.StatusCode == 429 {
		return &RateLimited{retryAfter(pr.Header)}
	}
	if pr.StatusCode != 201 && pr.StatusCode != 202 {
		return fmt.Errorf("REST PUT %s: %s", path, pr.Status)
	}
	return nil
}
