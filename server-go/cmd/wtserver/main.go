// Command wtserver — мульти-аккаунтный WireTurn-совместимый туннель-сервер.
//
// Каркас разворота: транспорт-агностичное ядро (supervisor + account + egress)
// и сменные транспорты (webdav = MVP1, olcrtc = MVP2).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Perebevaska/vpm/server-go/internal/config"
	"github.com/Perebevaska/vpm/server-go/internal/supervisor"
)

func main() {
	cfgPath := flag.String("config", "accounts.json", "путь к JSON-конфигу аккаунтов")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.SetOutput(os.Stderr)
	if err := supervisor.Run(ctx, cfg); err != nil {
		log.Fatalf("supervisor: %v", err)
	}
	log.Printf("shutdown")
}
