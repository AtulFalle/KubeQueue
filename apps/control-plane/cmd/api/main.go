// Package main starts the KubeQueue API process.
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
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := httpserver.Run(ctx); err != nil {
		slog.Error("API stopped", "error", err)
		return 1
	}
	return 0
}
