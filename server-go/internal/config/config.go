// Package config — конфиг мульти-аккаунтного сервера (JSON, без внешних зависимостей).
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Egress — общий выход по умолчанию (может переопределяться per-account позже).
type Egress struct {
	Proxy string `json:"proxy"` // "socks5://[user:pass@]host:port" или "" = direct
}

// Account — один изолированный тенант: свои креды, своё хранилище/квота, свой enc-ключ.
type Account struct {
	ID        string `json:"id"`
	Transport string `json:"transport"` // "webdav" | "olcrtc"
	Enc       bool   `json:"enc"`

	// webdav-специфичное
	WebDAVURL   string `json:"webdav_url,omitempty"`
	Login       string `json:"login,omitempty"`
	AppPassword string `json:"app_password,omitempty"`
	OAuthToken  string `json:"oauth_token,omitempty"` // для REST-аплоада s2c

	// olcrtc-специфичное (MVP2) — заполнить при реализации транспорта
	OlcrtcURL string `json:"olcrtc_url,omitempty"`
	ChainAddr string `json:"chain_addr,omitempty"` // куда мостить VLESS (server1)
}

type Config struct {
	Egress   Egress    `json:"egress"`
	Accounts []Account `json:"accounts"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(c.Accounts) == 0 {
		return nil, fmt.Errorf("config %s: no accounts", path)
	}
	for i, a := range c.Accounts {
		if a.ID == "" {
			return nil, fmt.Errorf("account #%d: empty id", i)
		}
		if a.Transport != "webdav" && a.Transport != "olcrtc" {
			return nil, fmt.Errorf("account %s: transport must be webdav|olcrtc", a.ID)
		}
	}
	return &c, nil
}
