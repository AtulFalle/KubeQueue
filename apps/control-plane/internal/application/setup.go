package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/google/uuid"
)

type SetupRepository interface {
	Ping(context.Context) error
	VerifySchema(context.Context) error
	WorkerStatus(context.Context) (domain.WorkerStatus, error)
	HasVerifiedInstallationOwner(context.Context) (bool, error)
	ClaimSetup(context.Context, domain.SetupClaimInput, string) (domain.SetupClaim, error)
	SetupRecovery(context.Context) (domain.SetupRecovery, error)
}

type Setup struct {
	repository   SetupRepository
	hashPassword func(string) (string, error)
	publicURL    string
	now          func() time.Time
}

func (s *Setup) WithLocalPasswordHasher(hasher func(string) (string, error)) *Setup {
	s.hashPassword = hasher
	return s
}

func NewSetup(repository SetupRepository, publicURL string) (*Setup, error) {
	if repository == nil {
		return nil, errors.New("setup repository is required")
	}
	return &Setup{
		repository: repository, publicURL: strings.TrimSpace(publicURL), now: time.Now,
	}, nil
}

func (s *Setup) Status(ctx context.Context) domain.SetupStatus {
	result := s.readiness(ctx)
	owner, ownerErr := s.repository.HasVerifiedInstallationOwner(ctx)
	if ownerErr == nil && owner {
		result.State = "COMPLETED"
		result.Available = false
	} else if ownerErr == nil {
		result.State = "AVAILABLE"
		result.Available = true
	}
	return result
}

func (s *Setup) readiness(ctx context.Context) domain.SetupStatus {
	result := domain.SetupStatus{
		State: "UNAVAILABLE",
		API:   domain.SetupReadiness{Ready: true},
	}
	if err := s.repository.Ping(ctx); err != nil {
		result.Database.Message = "database is unavailable"
	} else {
		result.Database.Ready = true
	}
	if err := s.repository.VerifySchema(ctx); err != nil {
		result.Schema.Message = "database schema is not compatible"
	} else {
		result.Schema.Ready = true
	}
	worker, err := s.repository.WorkerStatus(ctx)
	if err != nil {
		result.Worker.Message = "worker status is unavailable"
		result.KubernetesAuthority.Message = "Kubernetes authority has not been observed"
		result.Release.Message = "release identity has not been observed"
	} else {
		if worker.HeartbeatAt != nil {
			age := s.now().UTC().Sub(worker.HeartbeatAt.UTC())
			result.Worker.Ready = age >= -5*time.Second && age <= 30*time.Second &&
				worker.State != domain.WorkerStateUnavailable
		}
		if !result.Worker.Ready {
			result.Worker.Message = "worker heartbeat is unavailable or stale"
		}
		result.KubernetesAuthority.Ready = len(worker.Namespaces) > 0
		for _, namespace := range worker.Namespaces {
			result.KubernetesAuthority.Ready = result.KubernetesAuthority.Ready &&
				namespace.Authorized && namespace.InformerSynced
		}
		if !result.KubernetesAuthority.Ready {
			result.KubernetesAuthority.Message = "required namespace authority is not ready"
		}
		result.Release.Ready = strings.TrimSpace(worker.ReleaseVersion) != ""
		if !result.Release.Ready {
			result.Release.Message = "release version has not been reported"
		}
	}
	if parsed, parseErr := url.Parse(s.publicURL); parseErr == nil &&
		(parsed.Scheme == "https" || (parsed.Scheme == "http" && setupLoopbackHost(parsed.Hostname()))) &&
		parsed.Host != "" && parsed.User == nil && (parsed.Path == "" || parsed.Path == "/") {
		result.PublicURL.Ready = true
	} else {
		result.PublicURL.Message = "public URL is not configured as an HTTPS origin"
	}
	return result
}

func setupLoopbackHost(host string) bool {
	address := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (address != nil && address.IsLoopback())
}

func setupReadinessAllowsClaim(status domain.SetupStatus) bool {
	return status.API.Ready &&
		status.Database.Ready &&
		status.Schema.Ready &&
		status.Worker.Ready &&
		status.KubernetesAuthority.Ready &&
		status.Release.Ready &&
		status.PublicURL.Ready
}

func (s *Setup) Claim(
	ctx context.Context, input domain.SetupClaimInput,
) (domain.SetupClaim, error) {
	if s.hashPassword == nil {
		return domain.SetupClaim{}, errors.New("local password hashing is unavailable")
	}
	if !setupReadinessAllowsClaim(s.readiness(ctx)) {
		return domain.SetupClaim{}, domain.ErrSetupUnavailable
	}
	if err := input.Validate(); err != nil {
		return domain.SetupClaim{}, err
	}
	owner, err := s.repository.HasVerifiedInstallationOwner(ctx)
	if err != nil {
		return domain.SetupClaim{}, fmt.Errorf("check installation owner: %w", err)
	}
	if owner {
		return domain.SetupClaim{}, domain.ErrSetupUnavailable
	}
	passwordHash, err := s.hashPassword(input.LocalAdmin.Password)
	if err != nil {
		return domain.SetupClaim{}, fmt.Errorf("hash local owner password: %w", err)
	}
	fingerprintInput := input
	fingerprintInput.LocalAdmin.Password = ""
	fingerprintInput.LocalAdmin.PasswordHash = ""
	encoded, err := json.Marshal(fingerprintInput)
	if err != nil {
		return domain.SetupClaim{}, fmt.Errorf("fingerprint setup claim: %w", err)
	}
	fingerprint := sha256.Sum256(encoded)
	input.LocalAdmin.Password = ""
	input.LocalAdmin.PasswordHash = passwordHash
	input.LocalAdmin.PrincipalID = domain.PrincipalID(
		"local_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
	)
	actor := domain.Actor{
		PrincipalID:          input.LocalAdmin.PrincipalID,
		InstallationID:       "default",
		AuthenticationMethod: domain.AuthenticationMethodLocal,
		CredentialID:         "local:" + string(input.LocalAdmin.PrincipalID),
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "setup.complete", "installation", "default", "",
		"COMPLETED", "installation", "owner", "project", "policy", "quota",
	)
	if err != nil {
		return domain.SetupClaim{}, err
	}
	return s.repository.ClaimSetup(ctx, input, hex.EncodeToString(fingerprint[:]))
}

func (s *Setup) Recovery(ctx context.Context) (domain.SetupRecovery, error) {
	return s.repository.SetupRecovery(ctx)
}
