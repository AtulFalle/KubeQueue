// Package main starts the KubeQueue API process.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/httpserver"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/migration"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runProcess := httpserver.Run
	processName := "API"
	if len(os.Args) == 2 && os.Args[1] == "migrate" {
		runProcess = migration.Run
		processName = "migration"
	}
	if err := runProcess(ctx); err != nil {
		slog.Error(processName+" stopped", "error", err)
		return 1
	}
	return 0
}
