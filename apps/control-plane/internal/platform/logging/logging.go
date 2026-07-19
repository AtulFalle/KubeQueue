// Package logging initializes the process-wide structured logger.
package logging

import (
	"log/slog"
	"os"
)

// Initialize installs a JSON logger with stable process metadata. Request logs
// add request_id and trace_id at the HTTP boundary.
func Initialize(process string) {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler).With(
		"service", "kubequeue",
		"process", process,
	))
}
