package dav

import (
	"context"
	"time"
)

// Limiter — token-bucket поверх всех запросов к одному аккаунту Диска. Яндекс
// режет ВСПЛЕСКИ (пачка запросов подряд → 429), поэтому сглаживаем: burst
// токенов на старте, долив rps токенов/сек. Пустой бакет → Wait блокирует до
// следующего токена. Нужен, чтобы «безопасно» переживать пачку коннектов
// (Telegram открывает ~10 DC разом) без 429.
//
// nil-Limiter безопасен: Wait/Stop на nil — no-op (лимит выключен).
type Limiter struct {
	tokens chan struct{}
	stop   chan struct{}
}

// NewLimiter: burst токенов доступно сразу, далее долив каждые 1/rps секунды.
// rps<=0 или burst<=0 → nil (без лимита).
func NewLimiter(rps, burst int) *Limiter {
	if rps <= 0 || burst <= 0 {
		return nil
	}
	l := &Limiter{tokens: make(chan struct{}, burst), stop: make(chan struct{})}
	for i := 0; i < burst; i++ {
		l.tokens <- struct{}{}
	}
	go func() {
		t := time.NewTicker(time.Second / time.Duration(rps))
		defer t.Stop()
		for {
			select {
			case <-t.C:
				select {
				case l.tokens <- struct{}{}: // долить, если есть место
				default:
				}
			case <-l.stop:
				return
			}
		}
	}()
	return l
}

// Wait блокирует до свободного токена или отмены ctx.
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	select {
	case <-l.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop останавливает долив (при завершении аккаунта).
func (l *Limiter) Stop() {
	if l != nil {
		close(l.stop)
	}
}
