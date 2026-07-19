package application

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

var identityProviderSecretAAD = []byte("kubequeue-oidc-client-secret-v1")

type IdentityProviderRepository interface {
	ListIdentityProviders(context.Context, domain.InstallationID) ([]domain.ManagedIdentityProvider, error)
	IdentityProvider(context.Context, domain.InstallationID, string) (domain.ManagedIdentityProvider, error)
	CreateIdentityProvider(context.Context, domain.ManagedIdentityProvider) (domain.ManagedIdentityProvider, error)
	UpdateIdentityProvider(context.Context, domain.ManagedIdentityProvider, uint64) (domain.ManagedIdentityProvider, error)
	RecordIdentityProviderTest(context.Context, domain.InstallationID, string, uint64, bool, string, time.Time) (domain.ManagedIdentityProvider, error)
	SetIdentityProviderEnabled(context.Context, domain.InstallationID, string, uint64, bool, time.Time) (domain.ManagedIdentityProvider, error)
	IsInstallationOwner(context.Context, domain.Actor) (bool, error)
}

type OIDCPreflighter interface {
	Preflight(context.Context, domain.OIDCProvider) error
}

type IdentityProviders struct {
	repository IdentityProviderRepository
	preflight  OIDCPreflighter
	aead       cipher.AEAD
	now        func() time.Time
	random     io.Reader
}

type LoginMethod struct {
	Type  string
	ID    string
	Label string
}

type OIDCAuthorizationMetadata struct {
	ID          string
	Issuer      string
	ClientID    string
	RedirectURI string
	Scopes      string
}

func NewIdentityProviders(
	repository IdentityProviderRepository, preflight OIDCPreflighter, encryptionKey []byte,
) (*IdentityProviders, error) {
	if repository == nil || preflight == nil {
		return nil, errors.New("identity-provider repository and preflight validator are required")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("create identity-provider secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create identity-provider secret AEAD: %w", err)
	}
	return &IdentityProviders{
		repository: repository, preflight: preflight, aead: aead, now: time.Now, random: rand.Reader,
	}, nil
}

func (s *IdentityProviders) List(ctx context.Context) ([]domain.ManagedIdentityProvider, error) {
	actor, err := s.owner(ctx)
	if err != nil {
		return nil, err
	}
	return s.repository.ListIdentityProviders(ctx, actor.InstallationID)
}

func (s *IdentityProviders) EnabledLoginMethods(
	ctx context.Context,
) ([]LoginMethod, error) {
	providers, err := s.repository.ListIdentityProviders(ctx, "")
	if err != nil {
		return nil, err
	}
	methods := make([]LoginMethod, 0, len(providers))
	for _, provider := range providers {
		if provider.State == domain.IdentityProviderEnabled && provider.CanEnable() {
			methods = append(methods, LoginMethod{
				Type: "OIDC", ID: provider.ID, Label: provider.Configuration.DisplayName,
			})
		}
	}
	return methods, nil
}

func (s *IdentityProviders) AuthorizationMetadata(
	ctx context.Context, id string,
) (OIDCAuthorizationMetadata, error) {
	providers, err := s.repository.ListIdentityProviders(ctx, "")
	if err != nil {
		return OIDCAuthorizationMetadata{}, err
	}
	for _, provider := range providers {
		if provider.ID == id && provider.State == domain.IdentityProviderEnabled && provider.CanEnable() {
			return OIDCAuthorizationMetadata{
				ID: provider.ID, Issuer: provider.Configuration.Issuer,
				ClientID:    provider.Configuration.ClientID,
				RedirectURI: provider.Configuration.RedirectURI,
				Scopes:      "openid profile email",
			}, nil
		}
	}
	return OIDCAuthorizationMetadata{}, domain.ErrIdentityProviderNotFound
}

func (s *IdentityProviders) Get(
	ctx context.Context, id string,
) (domain.ManagedIdentityProvider, error) {
	actor, err := s.owner(ctx)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	return s.repository.IdentityProvider(ctx, actor.InstallationID, id)
}

func (s *IdentityProviders) Create(
	ctx context.Context, id string, configuration domain.IdentityProviderConfiguration,
) (domain.ManagedIdentityProvider, error) {
	actor, err := s.owner(ctx)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	if configuration.ClientSecret == "" && configuration.ClientSecretRef == "" {
		return domain.ManagedIdentityProvider{}, errors.New("an OIDC client secret or secret reference is required")
	}
	now := s.now().UTC()
	provider := domain.ManagedIdentityProvider{
		ID: id, InstallationID: actor.InstallationID, Configuration: configuration,
		ClientSecretConfigured: true, State: domain.IdentityProviderDisabled,
		TestStatus: domain.IdentityProviderNotTested, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := provider.Validate(); err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	provider.ClientSecretCiphertext, err = s.encrypt(configuration.ClientSecret)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	provider.Configuration.ClientSecret = ""
	ctx, err = withAdministrativeAudit(
		ctx, actor, "identity_providers.create", "identity_provider", id, "",
		string(provider.State), "configuration", "client_credential",
	)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	return s.repository.CreateIdentityProvider(ctx, provider)
}

func (s *IdentityProviders) Update(
	ctx context.Context, id string, expected uint64,
	configuration domain.IdentityProviderConfiguration,
) (domain.ManagedIdentityProvider, error) {
	actor, err := s.owner(ctx)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	current, err := s.repository.IdentityProvider(ctx, actor.InstallationID, id)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	if configuration.ClientSecret == "" && configuration.ClientSecretRef == "" {
		configuration.ClientSecretRef = current.Configuration.ClientSecretRef
	}
	updated := current
	updated.Configuration = configuration
	updated.State = domain.IdentityProviderDisabled
	updated.TestStatus = domain.IdentityProviderNotTested
	updated.TestedAt, updated.TestMessage, updated.TestedVersion = nil, "", 0
	updated.Version = expected + 1
	updated.UpdatedAt = s.now().UTC()
	if configuration.ClientSecret != "" {
		updated.ClientSecretCiphertext, err = s.encrypt(configuration.ClientSecret)
		if err != nil {
			return domain.ManagedIdentityProvider{}, err
		}
		updated.Configuration.ClientSecretRef = ""
	} else if configuration.ClientSecretRef != "" {
		updated.ClientSecretCiphertext = ""
	}
	updated.Configuration.ClientSecret = ""
	updated.ClientSecretConfigured = updated.ClientSecretCiphertext != "" ||
		updated.Configuration.ClientSecretRef != ""
	if !updated.ClientSecretConfigured {
		return domain.ManagedIdentityProvider{}, errors.New("an OIDC client secret or secret reference is required")
	}
	if err := updated.Validate(); err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "identity_providers.update", "identity_provider", id, "",
		string(updated.State), "configuration", "test_result", "state",
	)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	return s.repository.UpdateIdentityProvider(ctx, updated, expected)
}

func (s *IdentityProviders) Test(
	ctx context.Context, id string, expected uint64,
) (domain.ManagedIdentityProvider, error) {
	actor, err := s.owner(ctx)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	provider, err := s.repository.IdentityProvider(ctx, actor.InstallationID, id)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	if provider.Version != expected {
		return domain.ManagedIdentityProvider{}, domain.ErrIdentityProviderConflict
	}
	passed, message := true, "Discovery and signing keys are available"
	if err := s.preflight.Preflight(ctx, provider.RuntimeProvider()); err != nil {
		passed, message = false, "Discovery or signing keys are unavailable"
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "identity_providers.test", "identity_provider", id, "",
		string(provider.State), "test_result",
	)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	return s.repository.RecordIdentityProviderTest(
		ctx, actor.InstallationID, id, expected, passed, message, s.now().UTC(),
	)
}

func (s *IdentityProviders) Enable(
	ctx context.Context, id string, expected uint64,
) (domain.ManagedIdentityProvider, error) {
	return s.setEnabled(ctx, id, expected, true)
}

func (s *IdentityProviders) Disable(
	ctx context.Context, id string, expected uint64,
) (domain.ManagedIdentityProvider, error) {
	return s.setEnabled(ctx, id, expected, false)
}

func (s *IdentityProviders) setEnabled(
	ctx context.Context, id string, expected uint64, enabled bool,
) (domain.ManagedIdentityProvider, error) {
	actor, err := s.owner(ctx)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	current, err := s.repository.IdentityProvider(ctx, actor.InstallationID, id)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	if current.Version != expected {
		return domain.ManagedIdentityProvider{}, domain.ErrIdentityProviderConflict
	}
	if (enabled && current.State == domain.IdentityProviderEnabled) ||
		(!enabled && current.State == domain.IdentityProviderDisabled) {
		return current, nil
	}
	if enabled && !current.CanEnable() {
		return domain.ManagedIdentityProvider{}, domain.ErrIdentityProviderTestRequired
	}
	action, state := "identity_providers.disable", domain.IdentityProviderDisabled
	if enabled {
		action, state = "identity_providers.enable", domain.IdentityProviderEnabled
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, action, "identity_provider", id, "", string(state), "state",
	)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	return s.repository.SetIdentityProviderEnabled(
		ctx, actor.InstallationID, id, expected, enabled, s.now().UTC(),
	)
}

func (s *IdentityProviders) ResolveClient(
	ctx context.Context, id string,
) (domain.ManagedIdentityProvider, string, error) {
	providers, err := s.repository.ListIdentityProviders(ctx, "")
	if err != nil {
		return domain.ManagedIdentityProvider{}, "", err
	}
	for _, provider := range providers {
		if provider.ID != id || provider.State != domain.IdentityProviderEnabled ||
			!provider.CanEnable() {
			continue
		}
		if provider.ClientSecretCiphertext == "" {
			return provider, "", errors.New("referenced OIDC client secret is not locally resolvable")
		}
		secret, err := s.decrypt(provider.ClientSecretCiphertext)
		return provider, secret, err
	}
	return domain.ManagedIdentityProvider{}, "", domain.ErrIdentityProviderNotFound
}

func (s *IdentityProviders) owner(ctx context.Context) (domain.Actor, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.Actor{}, err
	}
	owner, err := s.repository.IsInstallationOwner(ctx, actor)
	if err != nil {
		return domain.Actor{}, err
	}
	if !owner {
		return domain.Actor{}, domain.ErrAccessDenied
	}
	return actor, nil
}

func (s *IdentityProviders) encrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(s.random, nonce); err != nil {
		return "", fmt.Errorf("generate client-secret nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(
		s.aead.Seal(nonce, nonce, []byte(value), identityProviderSecretAAD),
	), nil
}

func (s *IdentityProviders) decrypt(value string) (string, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(encoded) < s.aead.NonceSize()+s.aead.Overhead() {
		return "", errors.New("OIDC client secret is unavailable")
	}
	plaintext, err := s.aead.Open(
		nil, encoded[:s.aead.NonceSize()], encoded[s.aead.NonceSize():], identityProviderSecretAAD,
	)
	if err != nil {
		return "", errors.New("OIDC client secret is unavailable")
	}
	return string(plaintext), nil
}
