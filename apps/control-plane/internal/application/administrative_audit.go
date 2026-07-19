package application

import (
	"context"
	"net/netip"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/google/uuid"
)

type AuditRequestMetadata struct {
	RequestID string
	TraceID   string
	Source    netip.Addr
	UserAgent string
}

type auditRequestContextKey struct{}

func WithAuditRequestMetadata(
	ctx context.Context,
	metadata AuditRequestMetadata,
) context.Context {
	return context.WithValue(ctx, auditRequestContextKey{}, metadata)
}

func AuditRequestMetadataFromContext(
	ctx context.Context,
) (AuditRequestMetadata, bool) {
	metadata, ok := ctx.Value(auditRequestContextKey{}).(AuditRequestMetadata)
	return metadata, ok
}

func withAdministrativeAudit(
	ctx context.Context,
	actor domain.Actor,
	actionValue string,
	targetTypeValue string,
	targetIDValue string,
	projectIDValue domain.ProjectID,
	stateValue string,
	changedFieldValues ...string,
) (context.Context, error) {
	return withTransactionalAudit(
		ctx, actor, actionValue, targetTypeValue, targetIDValue, projectIDValue,
		stateValue, "request.accepted", changedFieldValues...,
	)
}

func withTransactionalAudit(
	ctx context.Context,
	actor domain.Actor,
	actionValue string,
	targetTypeValue string,
	targetIDValue string,
	projectIDValue domain.ProjectID,
	stateValue string,
	reasonValue string,
	changedFieldValues ...string,
) (context.Context, error) {
	metadata, _ := AuditRequestMetadataFromContext(ctx)
	if metadata.RequestID == "" {
		metadata.RequestID = uuid.NewString()
	}
	if metadata.TraceID == "" {
		metadata.TraceID = uuid.NewString()
	}
	if !metadata.Source.IsValid() {
		metadata.Source = netip.MustParseAddr("127.0.0.1")
	}
	eventID, err := audit.NewEventID(uuid.NewString())
	if err != nil {
		return ctx, err
	}
	requestID, err := audit.NewRequestID(metadata.RequestID)
	if err != nil {
		return ctx, err
	}
	traceID, err := audit.NewTraceID(metadata.TraceID)
	if err != nil {
		return ctx, err
	}
	principalID, err := audit.NewPrincipalID(string(actor.PrincipalID))
	if err != nil {
		return ctx, err
	}
	credentialValue := actor.CredentialID
	if credentialValue == "" {
		credentialValue = "principal:" + string(actor.PrincipalID)
	}
	credentialID, err := audit.NewCredentialID(credentialValue)
	if err != nil {
		return ctx, err
	}
	auditActor, err := audit.NewActor(
		principalID,
		administrativeAuditAuthenticationMethod(actor.AuthenticationMethod),
		credentialID,
		nil,
	)
	if err != nil {
		return ctx, err
	}
	action, err := audit.NewAction(actionValue)
	if err != nil {
		return ctx, err
	}
	targetType, err := audit.NewTargetType(targetTypeValue)
	if err != nil {
		return ctx, err
	}
	targetID, err := audit.NewTargetID(targetIDValue)
	if err != nil {
		return ctx, err
	}
	target, err := audit.NewTarget(targetType, targetID)
	if err != nil {
		return ctx, err
	}
	installationID, err := audit.NewInstallationID(string(actor.InstallationID))
	if err != nil {
		return ctx, err
	}
	projectID, err := audit.NewProjectID(string(projectIDValue))
	if err != nil {
		return ctx, err
	}
	scope, err := audit.NewScope(
		installationID, projectID, audit.TeamID{}, audit.Namespace{},
	)
	if err != nil {
		return ctx, err
	}
	source, err := audit.NewTrustworthySource(
		metadata.Source, audit.SourceDirectPeer, metadata.UserAgent,
	)
	if err != nil {
		source, err = audit.NewTrustworthySource(metadata.Source, audit.SourceDirectPeer, "")
		if err != nil {
			return ctx, err
		}
	}
	summaryFields := make([]audit.SummaryField, len(changedFieldValues))
	for index, value := range changedFieldValues {
		summaryFields[index], err = audit.NewSummaryField(value)
		if err != nil {
			return ctx, err
		}
	}
	state, err := audit.NewSummaryState(stateValue)
	if err != nil {
		return ctx, err
	}
	summary, err := audit.NewSummary(state, summaryFields, 0, false)
	if err != nil {
		return ctx, err
	}
	reason, err := audit.NewReasonCode(reasonValue)
	if err != nil {
		return ctx, err
	}
	event, err := audit.NewEvent(audit.EventInput{
		ID: eventID, OccurredAt: time.Now().UTC(), RequestID: requestID, TraceID: traceID,
		Actor: auditActor, Action: action, Target: target, Scope: scope,
		Decision: audit.DecisionAllow, Result: audit.ResultSuccess, Reason: reason,
		Source: source, After: &summary,
	})
	if err != nil {
		return ctx, err
	}
	policy, err := audit.NewRetentionPolicy(365 * 24 * time.Hour)
	if err != nil {
		return ctx, err
	}
	return ports.WithTransactionalAudit(ctx, ports.TransactionalAudit{
		Event: event, Policy: policy, Hold: audit.NoLegalHold(),
	}), nil
}

func administrativeAuditAuthenticationMethod(method string) audit.AuthenticationMethod {
	switch method {
	case domain.AuthenticationMethodNativeServiceAccount:
		return audit.AuthenticationServiceAccountToken
	case domain.AuthenticationMethodOIDCClientCredentials:
		return audit.AuthenticationOIDCClientCredentials
	case domain.AuthenticationMethodBreakGlass:
		return audit.AuthenticationBreakGlass
	case "OIDC":
		return audit.AuthenticationOIDCSession
	default:
		return audit.AuthenticationLegacyToken
	}
}
