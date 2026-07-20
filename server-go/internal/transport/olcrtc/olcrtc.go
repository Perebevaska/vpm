// Package olcrtc — транспорт MVP2: обход через видео-сервисы (WebRTC).
//
// СТАТУС: заглушка. Крупная веха. Реализация потребует:
//   - pion/webrtc: ICE/DTLS/data-channels;
//   - реверс сигналинга olcRTC WireTurn (room-based, olcrtc://...@room) — как реверсили WebDAV;
//   - кодирование данных в видеокадры (VP8Channel/SEIChannel), чтобы ехать по реальным
//     видео-платформам — самая тяжёлая часть;
//   - сессия несёт VLESS → связка с handler.VLESSBridge (не yamux).
//
// Изоляция multi-user для MVP2 делается через xray UUID, а не per-account Диск.
package olcrtc

import (
	"context"

	"github.com/Perebevaska/vpm/server-go/internal/config"
	"github.com/Perebevaska/vpm/server-go/internal/transport"
)

type Transport struct {
	acc config.Account
}

func New(acc config.Account) *Transport {
	return &Transport{acc: acc}
}

func (t *Transport) Accept(ctx context.Context) (transport.Session, error) {
	return nil, transport.ErrNotImplemented
}

func (t *Transport) Close() error { return nil }
