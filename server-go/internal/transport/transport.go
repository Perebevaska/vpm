// Package transport определяет абстракцию несущего транспорта.
//
// Идея разворота: аккаунт-изоляция, поллинг и релей — транспорт-агностичны,
// а сам способ доставки байт от клиента меняется плагином. MVP1 = WebDAV
// (поллинг чанк-файлов), MVP2 = olcRTC (WebRTC поверх видео-сервисов).
package transport

import (
	"context"
	"errors"
	"io"
)

// Session — дуплексный байт-канал одной клиентской сессии. Поверх него
// работает SessionHandler (yamux-passthrough для MVP1 или VLESS-мост для MVP2).
type Session interface {
	io.ReadWriteCloser
	ID() string
}

// Transport выдаёт новые клиентские сессии. Реализации: webdav, olcrtc.
type Transport interface {
	// Accept блокирует до появления новой сессии или отмены ctx.
	Accept(ctx context.Context) (Session, error)
	Close() error
}

// ErrNotImplemented — стаб-транспорт ещё не реализован (каркас разворота).
var ErrNotImplemented = errors.New("transport not implemented")
