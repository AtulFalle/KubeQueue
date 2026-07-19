package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/breakglass"
)

type BreakGlassRepository interface {
	BreakGlassCredential(context.Context, string) (breakglass.Credential, error)
	RecordBreakGlassAttempt(context.Context, bool, time.Time) error
}

type BreakGlass struct {
	repository BreakGlassRepository
	digestKey  []byte
	now        func() time.Time
	mu         sync.Mutex
	window     time.Time
	failures   int
}

func NewBreakGlass(repository BreakGlassRepository, digestKey []byte) (*BreakGlass, error) {
	if repository == nil || len(digestKey) < breakglass.MinimumDigestKey {
		return nil, errors.New("break-glass repository and digest key are required")
	}
	return &BreakGlass{
		repository: repository,
		digestKey:  append([]byte(nil), digestKey...),
		now:        time.Now,
	}, nil
}

func (b *BreakGlass) Authenticate(ctx context.Context, candidate string) (domain.Actor, error) {
	now := b.now().UTC()
	if !b.allowInProcess(now) {
		_ = b.repository.RecordBreakGlassAttempt(ctx, false, now)
		return domain.Actor{}, breakglass.ErrRateLimited
	}
	prefix, err := breakglass.Parse(candidate)
	if err != nil {
		b.failed(now)
		_ = b.repository.RecordBreakGlassAttempt(ctx, false, now)
		return domain.Actor{}, breakglass.ErrInvalidCredential
	}
	credential, err := b.repository.BreakGlassCredential(ctx, prefix)
	if err != nil {
		b.failed(now)
		_ = b.repository.RecordBreakGlassAttempt(ctx, false, now)
		return domain.Actor{}, breakglass.ErrInvalidCredential
	}
	if err := credential.Validate(candidate, b.digestKey, now); err != nil {
		b.failed(now)
		_ = b.repository.RecordBreakGlassAttempt(ctx, false, now)
		return domain.Actor{}, breakglass.ErrInvalidCredential
	}
	if err := b.repository.RecordBreakGlassAttempt(ctx, true, now); err != nil {
		return domain.Actor{}, fmt.Errorf("record break-glass use: %w", err)
	}
	b.succeeded()
	return domain.Actor{
		PrincipalID:          "break_glass_owner",
		InstallationID:       "default",
		AuthenticationMethod: domain.AuthenticationMethodBreakGlass,
		CredentialID:         "break-glass:" + credential.Slot,
		CredentialPermissions: []domain.Permission{
			domain.PermissionInternalAll,
		},
		CredentialScope: domain.AccessScope{
			InstallationID: "default", InstallationWide: true,
		},
	}, nil
}

func (b *BreakGlass) allowInProcess(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.window.IsZero() || !now.Before(b.window.Add(breakglass.FailureWindow)) {
		b.window, b.failures = now, 0
	}
	return b.failures < breakglass.FailureLimit
}

func (b *BreakGlass) failed(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.window.IsZero() || !now.Before(b.window.Add(breakglass.FailureWindow)) {
		b.window, b.failures = now, 0
	}
	b.failures++
}

func (b *BreakGlass) succeeded() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.window, b.failures = time.Time{}, 0
}
