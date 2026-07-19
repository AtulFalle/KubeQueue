package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

var ErrAuthenticationFailed = errors.New("authentication failed")

type OIDCProviderRepository interface {
	ActiveOIDCProviders(context.Context) ([]domain.OIDCProvider, error)
	ResolveOIDCPrincipal(
		context.Context, domain.OIDCProvider, domain.OIDCIdentityClaims,
	) (domain.Actor, error)
}

type OIDCTokenVerifier interface {
	Verify(
		context.Context, string, []domain.OIDCProvider,
	) (domain.OIDCProvider, domain.OIDCIdentityClaims, error)
}

type OIDCAuthentication struct {
	providers OIDCProviderRepository
	verifier  OIDCTokenVerifier
	setup     interface {
		CompleteOIDCLogin(
			context.Context, domain.OIDCProvider, domain.OIDCIdentityClaims, domain.Actor,
		) error
	}
}

func NewOIDCAuthentication(
	providers OIDCProviderRepository,
	verifier OIDCTokenVerifier,
) *OIDCAuthentication {
	return &OIDCAuthentication{providers: providers, verifier: verifier}
}

func (a *OIDCAuthentication) WithSetupCompletion(setup interface {
	CompleteOIDCLogin(
		context.Context, domain.OIDCProvider, domain.OIDCIdentityClaims, domain.Actor,
	) error
}) *OIDCAuthentication {
	a.setup = setup
	return a
}

func (a *OIDCAuthentication) Authenticate(
	ctx context.Context, accessToken string,
) (domain.Actor, error) {
	if accessToken == "" {
		return domain.Actor{}, ErrAuthenticationFailed
	}
	providers, err := a.providers.ActiveOIDCProviders(ctx)
	if err != nil {
		return domain.Actor{}, fmt.Errorf("load OIDC providers: %w", err)
	}
	provider, claims, err := a.verifier.Verify(ctx, accessToken, providers)
	if err != nil {
		return domain.Actor{}, fmt.Errorf("%w: OIDC token rejected", ErrAuthenticationFailed)
	}
	actor, err := a.providers.ResolveOIDCPrincipal(ctx, provider, claims)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrIdentityDisabled),
			errors.Is(err, domain.ErrJITProvisioningDenied):
			return domain.Actor{}, fmt.Errorf("%w: OIDC identity rejected", ErrAuthenticationFailed)
		default:
			return domain.Actor{}, fmt.Errorf("resolve OIDC identity: %w", err)
		}
	}
	actor.IdentityProviderID = provider.ID
	if actor.AuthenticationMethod == "" {
		actor.AuthenticationMethod = "OIDC"
	}
	if a.setup != nil &&
		actor.AuthenticationMethod != domain.AuthenticationMethodOIDCClientCredentials {
		if err := a.setup.CompleteOIDCLogin(ctx, provider, claims, actor); err != nil {
			return domain.Actor{}, fmt.Errorf("complete first-time setup: %w", err)
		}
	}
	return actor, nil
}
