package httpserver

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/serviceaccountcredential"
)

type serviceAccountStore interface {
	application.ServiceAccountRepository
	application.Authorizer
}

func serviceAccountsFromEnvironment(
	store serviceAccountStore,
) (*application.ServiceAccounts, error) {
	encoded := strings.TrimSpace(os.Getenv("KUBEQUEUE_SERVICE_ACCOUNT_DIGEST_KEY"))
	if encoded == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) < serviceaccountcredential.MinimumDigestKeyBytes {
		return nil, errors.New(
			"KUBEQUEUE_SERVICE_ACCOUNT_DIGEST_KEY must be base64-encoded and at least 256 bits",
		)
	}
	lifecycle, err := serviceaccountcredential.NewLifecycle(
		key, serviceaccountcredential.DefaultPolicy(),
	)
	if err != nil {
		return nil, fmt.Errorf("configure service-account credential lifecycle: %w", err)
	}
	service, err := application.NewServiceAccounts(
		store, store, lifecycle, defaultServiceAccountDelegablePermissions(),
	)
	if err != nil {
		return nil, fmt.Errorf("configure service-account application: %w", err)
	}
	return service, nil
}

func defaultServiceAccountDelegablePermissions() []domain.Permission {
	return []domain.Permission{
		domain.PermissionJobsList,
		domain.PermissionJobsRead,
		domain.PermissionJobsManifestRead,
		domain.PermissionJobsSubmit,
		domain.PermissionJobsPause,
		domain.PermissionJobsResume,
		domain.PermissionJobsTerminate,
		domain.PermissionJobsRetry,
		domain.PermissionJobsTakeControl,
		domain.PermissionJobsArchive,
		domain.PermissionJobEventsRead,
		domain.PermissionEventStreamRead,
		domain.PermissionQueueRead,
		domain.PermissionQueueEntryUpdate,
		domain.PermissionQueueProjectReorder,
		domain.PermissionSystemStatusRead,
	}
}
