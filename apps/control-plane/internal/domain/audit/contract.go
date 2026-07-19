// Package audit defines the pure, immutable contract for security audit events.
//
// The contract deliberately has no field capable of carrying a manifest, credential,
// cookie, environment value, or arbitrary before/after payload. Summaries contain
// only bounded state codes, changed field names, and redaction counts.
package audit

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	maxIdentifierLength = 128
	maxGroups           = 64
	maxActionLength     = 96
	maxTargetTypeLength = 64
	maxReasonLength     = 96
	maxUserAgentLength  = 512
	maxSummaryFields    = 64
	maxSummaryState     = 64
)

type ErrorCode string

const (
	ErrorRequired          ErrorCode = "audit.required"
	ErrorInvalid           ErrorCode = "audit.invalid"
	ErrorTooLong           ErrorCode = "audit.too_long"
	ErrorTooMany           ErrorCode = "audit.too_many"
	ErrorSensitiveContent  ErrorCode = "audit.sensitive_content"
	ErrorInconsistentEvent ErrorCode = "audit.inconsistent_event"
)

// ValidationError is intentionally value-free so rejected secrets cannot enter logs.
type ValidationError struct {
	Field string
	Code  ErrorCode
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Code)
}

type EventID struct{ value string }
type RequestID struct{ value string }
type TraceID struct{ value string }
type PrincipalID struct{ value string }
type CredentialID struct{ value string }
type Group struct{ value string }
type Action struct{ value string }
type TargetType struct{ value string }
type TargetID struct{ value string }
type InstallationID struct{ value string }
type ProjectID struct{ value string }
type TeamID struct{ value string }
type Namespace struct{ value string }
type ReasonCode struct{ value string }

func NewEventID(value string) (EventID, error) {
	value, err := stableIdentifier("event_id", value, maxIdentifierLength)
	return EventID{value: value}, err
}

func NewRequestID(value string) (RequestID, error) {
	value, err := stableIdentifier("request_id", value, maxIdentifierLength)
	return RequestID{value: value}, err
}

func NewTraceID(value string) (TraceID, error) {
	value, err := stableIdentifier("trace_id", value, maxIdentifierLength)
	return TraceID{value: value}, err
}

func NewPrincipalID(value string) (PrincipalID, error) {
	value, err := stableIdentifier("actor.principal_id", value, maxIdentifierLength)
	return PrincipalID{value: value}, err
}

func NewCredentialID(value string) (CredentialID, error) {
	value, err := stableIdentifier("actor.credential_id", value, maxIdentifierLength)
	return CredentialID{value: value}, err
}

func NewGroup(value string) (Group, error) {
	value, err := stableIdentifier("actor.effective_groups", value, maxIdentifierLength)
	return Group{value: value}, err
}

func NewAction(value string) (Action, error) {
	value, err := dottedCode("action", value, maxActionLength)
	return Action{value: value}, err
}

func NewTargetType(value string) (TargetType, error) {
	value, err := dottedCode("target.type", value, maxTargetTypeLength)
	return TargetType{value: value}, err
}

func NewTargetID(value string) (TargetID, error) {
	value, err := stableIdentifier("target.id", value, maxIdentifierLength)
	return TargetID{value: value}, err
}

func NewInstallationID(value string) (InstallationID, error) {
	value, err := stableIdentifier("scope.installation_id", value, maxIdentifierLength)
	return InstallationID{value: value}, err
}

func NewProjectID(value string) (ProjectID, error) {
	value, err := optionalStableIdentifier("scope.project_id", value)
	return ProjectID{value: value}, err
}

func NewTeamID(value string) (TeamID, error) {
	value, err := optionalStableIdentifier("scope.team_id", value)
	return TeamID{value: value}, err
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)

func NewNamespace(value string) (Namespace, error) {
	if value == "" {
		return Namespace{}, nil
	}
	if len(value) > 63 {
		return Namespace{}, validationError("scope.namespace", ErrorTooLong)
	}
	if !dnsLabel.MatchString(value) {
		return Namespace{}, validationError("scope.namespace", ErrorInvalid)
	}
	return Namespace{value: value}, nil
}

func NewReasonCode(value string) (ReasonCode, error) {
	value, err := dottedCode("reason", value, maxReasonLength)
	return ReasonCode{value: value}, err
}

func (v EventID) String() string        { return v.value }
func (v RequestID) String() string      { return v.value }
func (v TraceID) String() string        { return v.value }
func (v PrincipalID) String() string    { return v.value }
func (v CredentialID) String() string   { return v.value }
func (v Group) String() string          { return v.value }
func (v Action) String() string         { return v.value }
func (v TargetType) String() string     { return v.value }
func (v TargetID) String() string       { return v.value }
func (v InstallationID) String() string { return v.value }
func (v ProjectID) String() string      { return v.value }
func (v TeamID) String() string         { return v.value }
func (v Namespace) String() string      { return v.value }
func (v ReasonCode) String() string     { return v.value }

type AuthenticationMethod string

const (
	AuthenticationOIDCClientCredentials AuthenticationMethod = "OIDC_CLIENT_CREDENTIALS"
	AuthenticationOIDCSession           AuthenticationMethod = "OIDC_SESSION"
	AuthenticationServiceAccountToken   AuthenticationMethod = "SERVICE_ACCOUNT_TOKEN"
	AuthenticationBreakGlass            AuthenticationMethod = "BREAK_GLASS"
	AuthenticationLegacyToken           AuthenticationMethod = "LEGACY_TOKEN"
)

func (m AuthenticationMethod) valid() bool {
	switch m {
	case AuthenticationOIDCClientCredentials, AuthenticationOIDCSession,
		AuthenticationServiceAccountToken, AuthenticationBreakGlass,
		AuthenticationLegacyToken:
		return true
	default:
		return false
	}
}

// Actor identifies the effective authenticated authority. Its group set is sorted and unique.
type Actor struct {
	principalID PrincipalID
	method      AuthenticationMethod
	credential  CredentialID
	groups      []Group
}

func NewActor(
	principalID PrincipalID,
	method AuthenticationMethod,
	credential CredentialID,
	effectiveGroups []Group,
) (Actor, error) {
	if principalID.value == "" {
		return Actor{}, validationError("actor.principal_id", ErrorRequired)
	}
	if !method.valid() {
		return Actor{}, validationError("actor.authentication_method", ErrorInvalid)
	}
	if credential.value == "" {
		return Actor{}, validationError("actor.credential_id", ErrorRequired)
	}
	if len(effectiveGroups) > maxGroups {
		return Actor{}, validationError("actor.effective_groups", ErrorTooMany)
	}

	groups := append([]Group(nil), effectiveGroups...)
	for _, group := range groups {
		if group.value == "" {
			return Actor{}, validationError("actor.effective_groups", ErrorInvalid)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].value < groups[j].value })
	for index := 1; index < len(groups); index++ {
		if groups[index-1] == groups[index] {
			return Actor{}, validationError("actor.effective_groups", ErrorInvalid)
		}
	}
	return Actor{
		principalID: principalID,
		method:      method,
		credential:  credential,
		groups:      groups,
	}, nil
}

func (a Actor) PrincipalID() PrincipalID                   { return a.principalID }
func (a Actor) AuthenticationMethod() AuthenticationMethod { return a.method }
func (a Actor) CredentialID() CredentialID                 { return a.credential }
func (a Actor) EffectiveGroups() []Group                   { return append([]Group(nil), a.groups...) }

type Target struct {
	targetType TargetType
	id         TargetID
}

func NewTarget(targetType TargetType, id TargetID) (Target, error) {
	if targetType.value == "" {
		return Target{}, validationError("target.type", ErrorRequired)
	}
	if id.value == "" {
		return Target{}, validationError("target.id", ErrorRequired)
	}
	return Target{targetType: targetType, id: id}, nil
}

func (t Target) Type() TargetType { return t.targetType }
func (t Target) ID() TargetID     { return t.id }

type Scope struct {
	installationID InstallationID
	projectID      ProjectID
	teamID         TeamID
	namespace      Namespace
}

func NewScope(
	installationID InstallationID,
	projectID ProjectID,
	teamID TeamID,
	namespace Namespace,
) (Scope, error) {
	if installationID.value == "" {
		return Scope{}, validationError("scope.installation_id", ErrorRequired)
	}
	if namespace.value != "" && projectID.value == "" {
		return Scope{}, validationError("scope.project_id", ErrorRequired)
	}
	return Scope{
		installationID: installationID,
		projectID:      projectID,
		teamID:         teamID,
		namespace:      namespace,
	}, nil
}

func (s Scope) InstallationID() InstallationID { return s.installationID }
func (s Scope) ProjectID() ProjectID           { return s.projectID }
func (s Scope) TeamID() TeamID                 { return s.teamID }
func (s Scope) Namespace() Namespace           { return s.namespace }

type AuthorizationDecision string

const (
	DecisionAllow AuthorizationDecision = "ALLOW"
	DecisionDeny  AuthorizationDecision = "DENY"
)

func (d AuthorizationDecision) valid() bool {
	return d == DecisionAllow || d == DecisionDeny
}

type Result string

const (
	ResultSuccess Result = "SUCCESS"
	ResultFailure Result = "FAILURE"
)

func (r Result) valid() bool {
	return r == ResultSuccess || r == ResultFailure
}

type SourceProvenance string

const (
	SourceDirectPeer   SourceProvenance = "DIRECT_PEER"
	SourceTrustedProxy SourceProvenance = "TRUSTED_PROXY"
)

// Source contains an address already resolved from the direct peer or a configured trusted proxy.
// Transport code must not pass an untrusted forwarding header to this constructor.
type Source struct {
	address    netip.Addr
	provenance SourceProvenance
	userAgent  string
}

func NewTrustworthySource(
	address netip.Addr,
	provenance SourceProvenance,
	userAgent string,
) (Source, error) {
	if !address.IsValid() {
		return Source{}, validationError("source.address", ErrorInvalid)
	}
	if provenance != SourceDirectPeer && provenance != SourceTrustedProxy {
		return Source{}, validationError("source.provenance", ErrorInvalid)
	}
	userAgent = strings.TrimSpace(userAgent)
	if len(userAgent) > maxUserAgentLength {
		return Source{}, validationError("source.user_agent", ErrorTooLong)
	}
	if containsControl(userAgent) {
		return Source{}, validationError("source.user_agent", ErrorInvalid)
	}
	if containsSecretBearingText(userAgent) {
		return Source{}, validationError("source.user_agent", ErrorSensitiveContent)
	}
	return Source{
		address:    address.Unmap(),
		provenance: provenance,
		userAgent:  userAgent,
	}, nil
}

func (s Source) Address() netip.Addr          { return s.address }
func (s Source) Provenance() SourceProvenance { return s.provenance }
func (s Source) UserAgent() string            { return s.userAgent }

type SummaryState struct{ value string }
type SummaryField struct{ value string }

var stateCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func NewSummaryState(value string) (SummaryState, error) {
	if value == "" {
		return SummaryState{}, nil
	}
	if len(value) > maxSummaryState {
		return SummaryState{}, validationError("summary.state", ErrorTooLong)
	}
	if !stateCode.MatchString(value) {
		return SummaryState{}, validationError("summary.state", ErrorInvalid)
	}
	return SummaryState{value: value}, nil
}

func NewSummaryField(value string) (SummaryField, error) {
	value, err := dottedCode("summary.changed_fields", value, maxIdentifierLength)
	if err != nil {
		return SummaryField{}, err
	}
	if isSensitiveFieldName(value) {
		return SummaryField{}, validationError("summary.changed_fields", ErrorSensitiveContent)
	}
	return SummaryField{value: value}, nil
}

func (s SummaryState) String() string { return s.value }
func (f SummaryField) String() string { return f.value }

// Summary is metadata-only. It cannot represent field values or serialized resources.
type Summary struct {
	state          SummaryState
	changedFields  []SummaryField
	redactionCount uint16
	truncated      bool
}

func NewSummary(
	state SummaryState,
	changedFields []SummaryField,
	redactionCount uint16,
	truncated bool,
) (Summary, error) {
	if len(changedFields) > maxSummaryFields {
		return Summary{}, validationError("summary.changed_fields", ErrorTooMany)
	}
	fields := append([]SummaryField(nil), changedFields...)
	for _, field := range fields {
		if field.value == "" || isSensitiveFieldName(field.value) {
			return Summary{}, validationError("summary.changed_fields", ErrorSensitiveContent)
		}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].value < fields[j].value })
	for index := 1; index < len(fields); index++ {
		if fields[index-1] == fields[index] {
			return Summary{}, validationError("summary.changed_fields", ErrorInvalid)
		}
	}
	return Summary{
		state:          state,
		changedFields:  fields,
		redactionCount: redactionCount,
		truncated:      truncated,
	}, nil
}

func (s Summary) State() SummaryState { return s.state }
func (s Summary) ChangedFields() []SummaryField {
	return append([]SummaryField(nil), s.changedFields...)
}
func (s Summary) RedactionCount() uint16 { return s.redactionCount }
func (s Summary) Truncated() bool        { return s.truncated }

type EventInput struct {
	ID         EventID
	OccurredAt time.Time
	RequestID  RequestID
	TraceID    TraceID
	Actor      Actor
	Action     Action
	Target     Target
	Scope      Scope
	Decision   AuthorizationDecision
	Result     Result
	Reason     ReasonCode
	Source     Source
	Before     *Summary
	After      *Summary
}

// Event is immutable after construction. Slice-bearing values are defensively copied.
type Event struct {
	id         EventID
	occurredAt time.Time
	requestID  RequestID
	traceID    TraceID
	actor      Actor
	action     Action
	target     Target
	scope      Scope
	decision   AuthorizationDecision
	result     Result
	reason     ReasonCode
	source     Source
	before     *Summary
	after      *Summary
}

func NewEvent(input EventInput) (Event, error) {
	switch {
	case input.ID.value == "":
		return Event{}, validationError("event_id", ErrorRequired)
	case input.OccurredAt.IsZero():
		return Event{}, validationError("occurred_at", ErrorRequired)
	case input.OccurredAt.Year() < 1 || input.OccurredAt.Year() > 9999:
		return Event{}, validationError("occurred_at", ErrorInvalid)
	case input.RequestID.value == "":
		return Event{}, validationError("request_id", ErrorRequired)
	case input.TraceID.value == "":
		return Event{}, validationError("trace_id", ErrorRequired)
	case input.Actor.principalID.value == "":
		return Event{}, validationError("actor", ErrorRequired)
	case input.Action.value == "":
		return Event{}, validationError("action", ErrorRequired)
	case input.Target.targetType.value == "":
		return Event{}, validationError("target", ErrorRequired)
	case input.Scope.installationID.value == "":
		return Event{}, validationError("scope", ErrorRequired)
	case !input.Decision.valid():
		return Event{}, validationError("decision", ErrorInvalid)
	case !input.Result.valid():
		return Event{}, validationError("result", ErrorInvalid)
	case input.Reason.value == "":
		return Event{}, validationError("reason", ErrorRequired)
	case !input.Source.address.IsValid():
		return Event{}, validationError("source", ErrorRequired)
	case input.Decision == DecisionDeny && input.Result != ResultFailure:
		return Event{}, validationError("result", ErrorInconsistentEvent)
	}

	return Event{
		id:         input.ID,
		occurredAt: input.OccurredAt.Round(0).UTC(),
		requestID:  input.RequestID,
		traceID:    input.TraceID,
		actor:      cloneActor(input.Actor),
		action:     input.Action,
		target:     input.Target,
		scope:      input.Scope,
		decision:   input.Decision,
		result:     input.Result,
		reason:     input.Reason,
		source:     input.Source,
		before:     cloneSummaryPointer(input.Before),
		after:      cloneSummaryPointer(input.After),
	}, nil
}

func (e Event) ID() EventID                     { return e.id }
func (e Event) OccurredAt() time.Time           { return e.occurredAt }
func (e Event) RequestID() RequestID            { return e.requestID }
func (e Event) TraceID() TraceID                { return e.traceID }
func (e Event) Actor() Actor                    { return cloneActor(e.actor) }
func (e Event) Action() Action                  { return e.action }
func (e Event) Target() Target                  { return e.target }
func (e Event) Scope() Scope                    { return e.scope }
func (e Event) Decision() AuthorizationDecision { return e.decision }
func (e Event) Result() Result                  { return e.result }
func (e Event) Reason() ReasonCode              { return e.reason }
func (e Event) Source() Source                  { return e.source }
func (e Event) Before() (Summary, bool)         { return summaryValue(e.before) }
func (e Event) After() (Summary, bool)          { return summaryValue(e.after) }

// OrderingKey is a stable, lexically sortable cursor key: timestamp first, event ID second.
func (e Event) OrderingKey() string {
	return e.occurredAt.Format("2006-01-02T15:04:05.000000000Z") + "/" + e.id.value
}

func cloneActor(actor Actor) Actor {
	actor.groups = append([]Group(nil), actor.groups...)
	return actor
}

func cloneSummary(summary Summary) Summary {
	summary.changedFields = append([]SummaryField(nil), summary.changedFields...)
	return summary
}

func cloneSummaryPointer(summary *Summary) *Summary {
	if summary == nil {
		return nil
	}
	cloned := cloneSummary(*summary)
	return &cloned
}

func summaryValue(summary *Summary) (Summary, bool) {
	if summary == nil {
		return Summary{}, false
	}
	return cloneSummary(*summary), true
}

var stableID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]*$`)
var code = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

func stableIdentifier(field, value string, limit int) (string, error) {
	if value == "" {
		return "", validationError(field, ErrorRequired)
	}
	if len(value) > limit {
		return "", validationError(field, ErrorTooLong)
	}
	if !stableID.MatchString(value) || containsSecretBearingText(value) {
		return "", validationError(field, ErrorInvalid)
	}
	return value, nil
}

func optionalStableIdentifier(field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return stableIdentifier(field, value, maxIdentifierLength)
}

func dottedCode(field, value string, limit int) (string, error) {
	if value == "" {
		return "", validationError(field, ErrorRequired)
	}
	if len(value) > limit {
		return "", validationError(field, ErrorTooLong)
	}
	if !code.MatchString(value) {
		return "", validationError(field, ErrorInvalid)
	}
	return value, nil
}

func validationError(field string, code ErrorCode) error {
	return &ValidationError{Field: field, Code: code}
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func containsSecretBearingText(value string) bool {
	lower := strings.ToLower(value)
	markers := [...]string{
		"authorization:", "proxy-authorization:", "bearer ", "basic ",
		"cookie:", "set-cookie:", "refresh_token", "refresh-token",
		"access_token", "access-token", "client_secret", "client-secret",
		"password=", "passwd=", "token=", "database_url", "database-url",
		"connection_string", "connection-string",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return looksLikeJWT(value)
}

func looksLikeJWT(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if len(part) < 8 {
			return false
		}
		for _, character := range part {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') &&
				character != '-' && character != '_' {
				return false
			}
		}
	}
	return true
}

func isSensitiveFieldName(value string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(value))
	forbidden := [...]string{
		"manifest", "template", "environment", "env", "command", "args",
		"authorization", "password", "passwd", "token", "cookie", "secret",
		"privatekey", "databaseurl", "connectionstring", "stringdata",
	}
	for _, term := range forbidden {
		if strings.Contains(normalized, term) {
			return true
		}
	}
	return false
}

func IsValidationError(err error, code ErrorCode) bool {
	var validation *ValidationError
	return errors.As(err, &validation) && validation.Code == code
}
