package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/httpserver"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := httpserver.Run(ctx); err != nil {
		slog.Error("API stopped", "error", err)
		os.Exit(1)
	}
}
