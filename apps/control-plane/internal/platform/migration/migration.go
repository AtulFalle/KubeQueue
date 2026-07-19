// Package migration owns composition of the schema migration process.
package migration

import (
	"context"
	"os"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
)

const timeout = 4 * time.Minute

// Run applies pending migrations and exits.
func Run(ctx context.Context) error {
	migrationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return persistence.Migrate(migrationContext, os.Getenv("KUBEQUEUE_DATABASE_URL"))
}
