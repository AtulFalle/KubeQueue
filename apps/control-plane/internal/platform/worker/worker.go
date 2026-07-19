// Package worker contains worker process composition.
package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/kubernetes"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/config"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/reconciler"
)

// Run continuously discovers, schedules, and reconciles Kubernetes Jobs.
func Run(ctx context.Context) error {
	store, err := persistence.OpenCompatible(ctx, os.Getenv("KUBEQUEUE_DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("open worker store: %w", err)
	}
	defer func() { _ = store.Close() }()
	if os.Getenv("KUBEQUEUE_DISABLE_KUBERNETES") == "true" {
		<-ctx.Done()
		return nil
	}
	client, err := kubernetes.InCluster()
	if err != nil {
		return fmt.Errorf("configure Kubernetes client: %w", err)
	}
	namespaceScope, err := config.NamespaceScopeFromEnvironment()
	if err != nil {
		return err
	}
	workerReconciler := reconciler.New(store, client, namespaceScope)
	return runWithHealthServer(ctx, workerReconciler)
}

type readiness interface {
	Run(context.Context) error
	Ready() bool
}

func runWithHealthServer(ctx context.Context, worker readiness) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Addr:              workerHealthAddress(),
		Handler:           healthHandler(worker.Ready),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()
	workerErrors := make(chan error, 1)
	go func() {
		workerErrors <- worker.Run(runCtx)
	}()

	var result error
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("serve worker health: %w", err)
		}
	case err := <-workerErrors:
		result = err
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		result = errors.Join(result, fmt.Errorf("shutdown worker health: %w", err))
	}
	return result
}

func healthHandler(ready func() bool) http.Handler {
	router := http.NewServeMux()
	router.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	router.HandleFunc("/readyz", func(response http.ResponseWriter, _ *http.Request) {
		if !ready() {
			http.Error(response, "worker is not ready", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	return router
}

func workerHealthAddress() string {
	port := strings.TrimSpace(os.Getenv("KUBEQUEUE_WORKER_HEALTH_PORT"))
	if port == "" {
		port = "8081"
	}
	return ":" + port
}
