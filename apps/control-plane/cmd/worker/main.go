// Package main starts the KubeQueue worker process.
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
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("worker starting")
	if err := worker.Run(ctx); err != nil {
		slog.Error("worker stopped", "error", err)
		return 1
	}
	return 0
}
