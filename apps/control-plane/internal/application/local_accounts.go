package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"golang.org/x/crypto/argon2"
)

const (
	localPasswordMemoryKiB   = 64 * 1024
	localPasswordIterations  = 3
	localPasswordParallelism = 2
	localPasswordSaltBytes   = 16
	localPasswordKeyBytes    = 32
	localLoginWindow         = 15 * time.Minute
	localLoginLockout        = 15 * time.Minute
	localLoginMaxFailures    = 5
)

type LocalAccounts struct {
	repository   ports.LocalAccountRepository
	sessions     *Sessions
	now          func() time.Time
	randomReader func([]byte) (int, error)
	dummyHash    string
}

type developmentLocalAdminRepository interface {
	EnsureDevelopmentLocalAdmin(context.Context, string) error
}

type LocalLoginInput struct {
	Username  string
	Password  string
	ClientKey string
}

func NewLocalAccounts(repository ports.LocalAccountRepository, sessions *Sessions) (*LocalAccounts, error) {
	if repository == nil || sessions == nil {
		return nil, errors.New("local-account repository and browser sessions are required")
	}
	service := &LocalAccounts{
		repository:   repository,
		sessions:     sessions,
		now:          time.Now,
		randomReader: rand.Read,
	}
	hash, err := service.hashPassword("kubequeue-nonexistent-account-password")
	if err != nil {
		return nil, fmt.Errorf("initialize local password verifier: %w", err)
	}
	service.dummyHash = hash
	return service, nil
}

func (a *LocalAccounts) HashPassword(password string) (string, error) {
	if err := domain.ValidateNewLocalPassword(password); err != nil {
		return "", err
	}
	return a.hashPassword(password)
}

// SeedDevelopmentLocalAdmin idempotently creates or re-enables the development-only admin account.
// The deliberately weak password bypasses normal password policy only through this explicit path.
func (a *LocalAccounts) SeedDevelopmentLocalAdmin(ctx context.Context) error {
	repository, ok := a.repository.(developmentLocalAdminRepository)
	if !ok {
		return errors.New("development local-admin seeding is unsupported")
	}
	hash, err := a.hashPassword("admin")
	if err != nil {
		return fmt.Errorf("hash development local-admin password: %w", err)
	}
	if err := repository.EnsureDevelopmentLocalAdmin(ctx, hash); err != nil {
		return fmt.Errorf("seed development local admin: %w", err)
	}
	return nil
}

func (a *LocalAccounts) Login(
	ctx context.Context, input LocalLoginInput,
) (SessionCredentials, error) {
	username := domain.NormalizeLocalUsername(input.Username)
	keys := []string{
		localThrottleKey("account", username),
		localThrottleKey("client", input.ClientKey),
	}
	now := a.now().UTC()
	for _, key := range keys {
		allowed, err := a.repository.LocalLoginAllowed(ctx, key, now)
		if err != nil {
			return SessionCredentials{}, fmt.Errorf("check local login throttle: %w", err)
		}
		if !allowed {
			return SessionCredentials{}, domain.ErrLocalAuthenticationLimited
		}
	}
	account, lookupErr := a.repository.LocalAccountByUsername(ctx, username)
	password := input.Password
	if domain.ValidateLocalUsername(input.Username) != nil ||
		len(input.Password) < 1 || len(input.Password) > 128 {
		lookupErr = domain.ErrLocalAuthenticationFailed
		password = "invalid-local-password"
	}
	hash := a.dummyHash
	if lookupErr == nil && !account.Disabled {
		hash = account.PasswordHash
	}
	valid := verifyLocalPassword(password, hash)
	if lookupErr != nil || account.Disabled || !valid {
		for _, key := range keys {
			if err := a.repository.RecordLocalLoginFailure(
				ctx, key, now, localLoginWindow, localLoginMaxFailures, localLoginLockout,
			); err != nil {
				return SessionCredentials{}, fmt.Errorf("record local login failure: %w", err)
			}
		}
		return SessionCredentials{}, domain.ErrLocalAuthenticationFailed
	}
	for _, key := range keys {
		if err := a.repository.ClearLocalLoginFailures(ctx, key); err != nil {
			return SessionCredentials{}, fmt.Errorf("clear local login throttle: %w", err)
		}
	}
	return a.sessions.Create(ctx, CreateSessionInput{
		Actor:                account.Actor(),
		AuthenticationMethod: domain.AuthenticationMethodLocal,
	})
}

func (a *LocalAccounts) ChangePassword(
	ctx context.Context, currentCredential, currentPassword, newPassword string,
) (SessionCredentials, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil || actor.AuthenticationMethod != domain.AuthenticationMethodLocal {
		return SessionCredentials{}, domain.ErrLocalAuthenticationFailed
	}
	if err := domain.ValidateNewLocalPassword(newPassword); err != nil {
		return SessionCredentials{}, err
	}
	if len(currentPassword) < 1 || len(currentPassword) > 128 {
		_ = verifyLocalPassword("invalid-local-password", a.dummyHash)
		return SessionCredentials{}, domain.ErrLocalAuthenticationFailed
	}
	account, err := a.repository.LocalAccountByPrincipal(ctx, actor.PrincipalID)
	if err != nil || !verifyLocalPassword(currentPassword, account.PasswordHash) {
		return SessionCredentials{}, domain.ErrLocalAuthenticationFailed
	}
	hash, err := a.hashPassword(newPassword)
	if err != nil {
		return SessionCredentials{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "local_account.password.change", "principal", string(actor.PrincipalID),
		"", "UPDATED", "credential_rotated", "sessions_revoked",
	)
	if err != nil {
		return SessionCredentials{}, err
	}
	now := a.now().UTC()
	if err := a.repository.ChangeLocalPassword(
		ctx, actor.PrincipalID, account.PasswordHash, hash, now,
	); err != nil {
		return SessionCredentials{}, err
	}
	return a.sessions.Create(ctx, CreateSessionInput{
		Actor:                actor,
		AuthenticationMethod: domain.AuthenticationMethodLocal,
		RotateCredential:     currentCredential,
	})
}

func (a *LocalAccounts) ResetPassword(
	ctx context.Context, principalID domain.PrincipalID, newPassword string,
) error {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return err
	}
	owner, err := a.repository.IsInstallationOwner(ctx, actor)
	if err != nil {
		return fmt.Errorf("verify installation owner: %w", err)
	}
	if !owner {
		return domain.ErrAccessDenied
	}
	if err := domain.ValidateNewLocalPassword(newPassword); err != nil {
		return err
	}
	target, err := a.repository.LocalAccountByPrincipal(ctx, principalID)
	if err != nil || target.InstallationID != actor.InstallationID {
		return domain.ErrLocalAccountNotFound
	}
	hash, err := a.hashPassword(newPassword)
	if err != nil {
		return err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "local_account.password.reset", "principal", string(principalID),
		"", "UPDATED", "credential_rotated", "sessions_revoked",
	)
	if err != nil {
		return err
	}
	return a.repository.ResetLocalPassword(ctx, principalID, hash, a.now().UTC())
}

func (a *LocalAccounts) hashPassword(password string) (string, error) {
	salt := make([]byte, localPasswordSaltBytes)
	if _, err := a.randomReader(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey(
		[]byte(password), salt, localPasswordIterations, localPasswordMemoryKiB,
		localPasswordParallelism, localPasswordKeyBytes,
	)
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		localPasswordMemoryKiB, localPasswordIterations, localPasswordParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func verifyLocalPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	for _, parameter := range strings.Split(parts[3], ",") {
		name, value, ok := strings.Cut(parameter, "=")
		if !ok {
			return false
		}
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return false
		}
		switch name {
		case "m":
			memory = uint32(parsed)
		case "t":
			iterations = uint32(parsed)
		case "p":
			if parsed > 255 {
				return false
			}
			parallelism = uint8(parsed)
		default:
			return false
		}
	}
	if memory != localPasswordMemoryKiB || iterations != localPasswordIterations ||
		parallelism != localPasswordParallelism {
		return false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[4])
	expected, hashErr := base64.RawStdEncoding.DecodeString(parts[5])
	if saltErr != nil || hashErr != nil || len(salt) != localPasswordSaltBytes ||
		len(expected) != localPasswordKeyBytes {
		return false
	}
	actual := argon2.IDKey(
		[]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)),
	)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func localThrottleKey(scope, value string) string {
	valueDigest := sha256.Sum256([]byte(scope + "\x00" + strings.TrimSpace(value)))
	return base64.RawURLEncoding.EncodeToString(valueDigest[:])
}
