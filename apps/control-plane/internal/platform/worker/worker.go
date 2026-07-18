// Package worker contains worker process composition.
//
// Scheduling and Kubernetes reconciliation are intentionally added in later milestones.
package worker

import "context"

// Run blocks until the process is asked to stop.
func Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
