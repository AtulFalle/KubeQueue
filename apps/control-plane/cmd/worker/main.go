package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("worker starting")
	if err := worker.Run(ctx); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
