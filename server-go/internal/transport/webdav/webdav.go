// Package webdav — транспорт MVP1: рандеву через WebDAV-хранилище (Яндекс.Диск),
// нумерованные чанк-файлы, AES-256-GCM, REST-аплоад s2c. Wire-совместим с WireTurn.
//
// СТАТУС: каркас. Логику портировать из рабочей Python-версии
// (server/wt/{webdav,pipe,crypto,rest_upload}.py):
//   - PROPFIND depth=2 tunnel/ → sid'ы + наличие init за 1 запрос (оптимизация);
//   - на новый sid → Session поверх Pipe (read c2s / write s2c, coalesce, read-ahead);
//   - srv-hb, stale-очистка ТОЛЬКО своих протухших (по hb), не сносить живые.
package webdav

import (
	"context"

	"github.com/Perebevaska/vpm/server-go/internal/config"
	"github.com/Perebevaska/vpm/server-go/internal/transport"
)

type Transport struct {
	acc config.Account
	// TODO: http-клиент per-account с bounded MaxConnsPerHost, enc-ключ, REST-аплоадер,
	//       адаптивный poll-интервал по 429-фидбеку.
}

func New(acc config.Account) *Transport {
	return &Transport{acc: acc}
}

// Accept — поллит tunnel/ своего аккаунта, отдаёт новую сессию.
func (t *Transport) Accept(ctx context.Context) (transport.Session, error) {
	// TODO(MVP1-port): реализовать поллинг + Pipe-сессию.
	return nil, transport.ErrNotImplemented
}

func (t *Transport) Close() error { return nil }
