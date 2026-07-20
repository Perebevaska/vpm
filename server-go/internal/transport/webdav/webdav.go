// Package webdav — транспорт MVP1: рандеву через WebDAV-хранилище (Яндекс.Диск),
// нумерованные чанк-файлы, AES-256-GCM, REST-аплоад s2c. Wire-совместим с WireTurn.
//
// СТАТУС: каркас. Логику портировать из рабочей Python-версии
// (server/wt/{webdav,pipe,crypto,rest_upload}.py). Требования — DESIGN.md,
// продиктованы замером #8:
//   - discovery: PROPFIND depth=2 tunnel/ → sid'ы + наличие init за 1 запрос;
//   - s2c upload через REST по умолчанию (при OAuthToken) — ×3.7 к записи;
//     download остаётся WebDAV GET (72ms, и так быстрый);
//   - латентно-связаны → прятать латентность параллелизмом (read-ahead/put-workers),
//     НЕ striping'ом (429=0, троттла нет);
//   - srv-hb, stale-очистка ТОЛЬКО своих протухших (по hb), не сносить живые.
package webdav

import (
	"context"
	"time"

	"github.com/Perebevaska/vpm/server-go/internal/config"
	"github.com/Perebevaska/vpm/server-go/internal/transport"
)

// Тюнинг-дефолты (откалибровано замером #8; см. DESIGN.md). Переопределяемы per-account позже.
const (
	ChunkDataSize = 256*1024 - 1           // 262143: меньше медленных PUT'ов на байт
	ReadAhead     = 16                     // GET ~72ms — прячем латентность префетчем
	PutWorkers    = 16                     // 429=0 на замере → можно шире
	PollMin       = 50 * time.Millisecond  // GET ~72ms — быстрее нет смысла
	PollMax       = 300 * time.Millisecond // не троттл → быстрее детектим появление чанка
)

type Transport struct {
	acc config.Account
	// TODO: http-клиент per-account с bounded MaxConnsPerHost, enc-ключ,
	//       REST-аплоадер (дефолт при acc.OAuthToken), адаптивный poll по 429.
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
