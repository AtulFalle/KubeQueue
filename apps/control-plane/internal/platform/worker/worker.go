// Package worker contains worker process composition.
package worker

import (
	"context"
	"fmt"
	"os"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/kubernetes"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/reconciler"
)

// Run continuously discovers, schedules, and reconciles Kubernetes Jobs.
func Run(ctx context.Context) error {
	if os.Getenv("KUBEQUEUE_DISABLE_KUBERNETES") == "true" {
		<-ctx.Done()
		return nil
	}
	store, err := persistence.Open(ctx, os.Getenv("KUBEQUEUE_DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("open worker store: %w", err)
	}
	defer func() { _ = store.Close() }()
	client, err := kubernetes.InCluster()
	if err != nil {
		return fmt.Errorf("configure Kubernetes client: %w", err)
	}
	return reconciler.New(store, client).Run(ctx)
}
