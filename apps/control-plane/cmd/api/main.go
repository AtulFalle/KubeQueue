// Package main starts the KubeQueue API process.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/httpserver"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/logging"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/migration"
)

func main() {
	os.Exit(run())
}

func run() int {
	runProcess := httpserver.Run
	processName := "api"
	if len(os.Args) == 2 && os.Args[1] == "migrate" {
		runProcess = migration.Run
		processName = "migration"
	}
	logging.Initialize(processName)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runProcess(ctx); err != nil {
		slog.Error(processName+" stopped", "error", err)
		return 1
	}
	return 0
}
