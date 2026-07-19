package ports

import (
	"context"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
)

// PolicyRepository stores immutable policy versions behind one compare-and-set
// head per installation, project, or namespace scope.
type PolicyRepository interface {
	PolicyHierarchy(
		context.Context,
		domain.InstallationID,
		policyquota.Scope,
	) ([]policyquota.Policy, error)
	CompareAndSetPolicy(
		context.Context,
		domain.InstallationID,
		uint64,
		policyquota.Policy,
	) error
}
