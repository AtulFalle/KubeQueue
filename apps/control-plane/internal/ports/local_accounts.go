package ports

import (
	"context"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

// LocalAccountRepository is the credential and recovery persistence boundary
// consumed by local human authentication use cases.
type LocalAccountRepository interface {
	LocalAccountByUsername(context.Context, string) (domain.LocalAccount, error)
	LocalAccountByPrincipal(context.Context, domain.PrincipalID) (domain.LocalAccount, error)
	LocalLoginAllowed(context.Context, string, time.Time) (bool, error)
	RecordLocalLoginFailure(context.Context, string, time.Time, time.Duration, int, time.Duration) error
	ClearLocalLoginFailures(context.Context, string) error
	ChangeLocalPassword(context.Context, domain.PrincipalID, string, string, time.Time) error
	ResetLocalPassword(context.Context, domain.PrincipalID, string, time.Time) error
	IsInstallationOwner(context.Context, domain.Actor) (bool, error)
}
