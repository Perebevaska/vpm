// Package supervisor — запускает воркер на каждый аккаунт и связывает
// транспорт с обработчиком. Точка сборки разворота.
package supervisor

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/Perebevaska/vpm/server-go/internal/account"
	"github.com/Perebevaska/vpm/server-go/internal/config"
	"github.com/Perebevaska/vpm/server-go/internal/egress"
	"github.com/Perebevaska/vpm/server-go/internal/handler"
	"github.com/Perebevaska/vpm/server-go/internal/transport"
	"github.com/Perebevaska/vpm/server-go/internal/transport/olcrtc"
	"github.com/Perebevaska/vpm/server-go/internal/transport/webdav"
)

func Run(ctx context.Context, cfg *config.Config) error {
	dialer, err := egress.New(cfg.Egress.Proxy)
	if err != nil {
		return err
	}
	eg := "direct"
	if cfg.Egress.Proxy != "" {
		eg = cfg.Egress.Proxy
	}
	log.Printf("supervisor: %d accounts, egress=%s", len(cfg.Accounts), eg)

	var wg sync.WaitGroup
	for _, acc := range cfg.Accounts {
		t, h, err := build(acc, dialer)
		if err != nil {
			return err
		}
		w := account.Worker{Name: acc.ID, T: t, Handler: h}
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(ctx) }()
	}
	wg.Wait()
	return nil
}

// build подбирает транспорт и обработчик под тип аккаунта.
//
//	webdav → YamuxPassthrough (сами дилим цель, MVP1)
//	olcrtc → VLESSBridge      (мостим в цепочку, MVP2)
func build(acc config.Account, dialer egress.Dialer) (transport.Transport, handler.SessionHandler, error) {
	switch acc.Transport {
	case "webdav":
		return webdav.New(acc), handler.YamuxPassthrough{Dialer: dialer}, nil
	case "olcrtc":
		return olcrtc.New(acc), handler.VLESSBridge{ChainAddr: acc.ChainAddr}, nil
	default:
		return nil, nil, fmt.Errorf("account %s: unknown transport %q", acc.ID, acc.Transport)
	}
}
