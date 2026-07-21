// Package account — воркер на аккаунт: изолированный поллинг транспорта и
// запуск обработчика на каждую сессию. Одна горутина на воркер, дёшево.
package account

import (
	"context"
	"errors"
	"log"

	"github.com/Perebevaska/vpm/server-go/internal/handler"
	"github.com/Perebevaska/vpm/server-go/internal/transport"
)

type Worker struct {
	Name    string
	T       transport.Transport
	Handler handler.SessionHandler
}

func (w Worker) Run(ctx context.Context) {
	log.Printf("account %q: worker up", w.Name)
	defer w.T.Close()
	for {
		s, err := w.T.Accept(ctx)
		if err != nil {
			if errors.Is(err, transport.ErrNotImplemented) {
				log.Printf("account %q: transport not implemented yet — worker idle", w.Name)
				<-ctx.Done()
				return
			}
			if ctx.Err() != nil {
				return
			}
			log.Printf("account %q: accept: %v", w.Name, err)
			continue
		}
		go func(s transport.Session) {
			defer s.Close() // Cleanup папки + стоп srv-hb после завершения Serve
			w.Handler.Serve(ctx, s)
		}(s)
	}
}
