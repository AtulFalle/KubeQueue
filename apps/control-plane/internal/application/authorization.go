package application

import (
	"context"
	"errors"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

var ErrMissingPrincipal = errors.New("authenticated principal is required")

type Authorizer interface {
	Authorize(context.Context, domain.Actor, domain.Permission, domain.AuthorizationScope) error
	AccessibleScope(context.Context, domain.Actor, domain.Permission) (domain.AccessScope, error)
}

type actorContextKey struct{}

func WithActor(ctx context.Context, actor domain.Actor) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actor)
}

func ActorFromContext(ctx context.Context) (domain.Actor, error) {
	actor, ok := ctx.Value(actorContextKey{}).(domain.Actor)
	if !ok || actor.PrincipalID == "" || actor.InstallationID == "" {
		return domain.Actor{}, ErrMissingPrincipal
	}
	return actor, nil
}
