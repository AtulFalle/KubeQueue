// Package policyquota defines pure, persistence-neutral project policy and
// quota rules.
package policyquota

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ScopeKind string

const (
	ScopeInstallation ScopeKind = "INSTALLATION"
	ScopeProject      ScopeKind = "PROJECT"
	ScopeNamespace    ScopeKind = "NAMESPACE"
)

type Scope struct {
	Kind      ScopeKind
	Project   string
	Namespace string
}

type PolicyRef struct {
	ID      string
	Version uint64
	Scope   Scope
}

type LifecycleAction string

const (
	ActionPause   LifecycleAction = "PAUSE"
	ActionResume  LifecycleAction = "RESUME"
	ActionCancel  LifecycleAction = "CANCEL"
	ActionRetry   LifecycleAction = "RETRY"
	ActionReorder LifecycleAction = "REORDER"
)

type PriorityRange struct {
	Min     int
	Max     int
	Default int
}

type ScopedLimits struct {
	MaxConcurrent *uint64
	MaxQueued     *uint64
	MaxRetained   *uint64
}

type QuotaLimits struct {
	Global    ScopedLimits
	Project   ScopedLimits
	Namespace ScopedLimits
}

// Rules is an overlay. Nil scalar values inherit from the parent. A nil
// registry allowlist means unrestricted/inherited; a non-nil empty slice
// denies every registry. Role entries inherit independently, and an explicit
// empty action slice denies that role every lifecycle action.
type Rules struct {
	Quotas                    QuotaLimits
	Priority                  *PriorityRange
	MaxDelayedStart           *time.Duration
	RoleLifecycleActions      map[string][]LifecycleAction
	MaxExecutionDuration      *time.Duration
	AllowedImageRegistries    []string
	HasImageRegistryAllowlist bool
}

type Policy struct {
	Ref   PolicyRef
	Rules Rules
}

type EffectivePolicy struct {
	Applied []PolicyRef
	Rules   Rules
}

var (
	ErrInvalidPolicy   = errors.New("invalid policy")
	ErrPolicyExpansion = errors.New("policy expands parent")
)

// Compose validates and combines installation, project, and namespace
// policies in order. Every explicit child rule must be at least as restrictive
// as the effective parent rule.
func Compose(policies ...Policy) (EffectivePolicy, error) {
	if len(policies) == 0 {
		return EffectivePolicy{}, fmt.Errorf("%w: installation policy is required", ErrInvalidPolicy)
	}

	var effective EffectivePolicy
	for index, policy := range policies {
		if err := validatePolicyRef(policy.Ref, index, effective.Applied); err != nil {
			return EffectivePolicy{}, err
		}
		normalized, err := normalizeRules(policy.Rules)
		if err != nil {
			return EffectivePolicy{}, fmt.Errorf("%w: %s version %d: %w", ErrInvalidPolicy, policy.Ref.ID, policy.Ref.Version, err)
		}
		if index > 0 {
			if err := validateNarrowing(effective.Rules, normalized); err != nil {
				return EffectivePolicy{}, fmt.Errorf("%w: %s version %d: %w", ErrPolicyExpansion, policy.Ref.ID, policy.Ref.Version, err)
			}
		}
		effective.Rules = overlay(effective.Rules, normalized)
		effective.Applied = append(effective.Applied, policy.Ref)
	}
	if err := validateComplete(effective.Rules); err != nil {
		return EffectivePolicy{}, fmt.Errorf("%w: effective policy: %w", ErrInvalidPolicy, err)
	}
	return effective, nil
}

func validatePolicyRef(ref PolicyRef, index int, parents []PolicyRef) error {
	if strings.TrimSpace(ref.ID) == "" || ref.Version == 0 {
		return fmt.Errorf("%w: policy ID and positive version are required", ErrInvalidPolicy)
	}
	expected := []ScopeKind{ScopeInstallation, ScopeProject, ScopeNamespace}
	if index >= len(expected) || ref.Scope.Kind != expected[index] {
		return fmt.Errorf("%w: policy %s has scope %s at hierarchy position %d", ErrInvalidPolicy, ref.ID, ref.Scope.Kind, index)
	}
	switch ref.Scope.Kind {
	case ScopeInstallation:
		if ref.Scope.Project != "" || ref.Scope.Namespace != "" {
			return fmt.Errorf("%w: installation scope cannot name a project or namespace", ErrInvalidPolicy)
		}
	case ScopeProject:
		if strings.TrimSpace(ref.Scope.Project) == "" || ref.Scope.Namespace != "" {
			return fmt.Errorf("%w: project scope requires only a project", ErrInvalidPolicy)
		}
	case ScopeNamespace:
		if strings.TrimSpace(ref.Scope.Project) == "" || strings.TrimSpace(ref.Scope.Namespace) == "" {
			return fmt.Errorf("%w: namespace scope requires a project and namespace", ErrInvalidPolicy)
		}
		if len(parents) < 2 || ref.Scope.Project != parents[1].Scope.Project {
			return fmt.Errorf("%w: namespace scope must belong to the parent project", ErrInvalidPolicy)
		}
	default:
		return fmt.Errorf("%w: unknown scope %q", ErrInvalidPolicy, ref.Scope.Kind)
	}
	return nil
}

func normalizeRules(rules Rules) (Rules, error) {
	if rules.Priority != nil {
		if rules.Priority.Min > rules.Priority.Max {
			return Rules{}, errors.New("priority minimum exceeds maximum")
		}
		if rules.Priority.Default < rules.Priority.Min || rules.Priority.Default > rules.Priority.Max {
			return Rules{}, errors.New("default priority is outside the permitted range")
		}
	}
	if err := positiveDuration(rules.MaxDelayedStart, "maximum delayed-start horizon"); err != nil {
		return Rules{}, err
	}
	if err := positiveDuration(rules.MaxExecutionDuration, "maximum execution duration"); err != nil {
		return Rules{}, err
	}

	normalized := rules
	normalized.RoleLifecycleActions = make(map[string][]LifecycleAction, len(rules.RoleLifecycleActions))
	for role, actions := range rules.RoleLifecycleActions {
		role = strings.TrimSpace(role)
		if role == "" {
			return Rules{}, errors.New("lifecycle-action role cannot be empty")
		}
		set := make(map[LifecycleAction]struct{}, len(actions))
		for _, action := range actions {
			if !validAction(action) {
				return Rules{}, fmt.Errorf("unknown lifecycle action %q", action)
			}
			set[action] = struct{}{}
		}
		normalized.RoleLifecycleActions[role] = sortedActions(set)
	}

	if rules.HasImageRegistryAllowlist {
		set := make(map[string]struct{}, len(rules.AllowedImageRegistries))
		for _, registry := range rules.AllowedImageRegistries {
			value, err := NormalizeRegistry(registry)
			if err != nil {
				return Rules{}, err
			}
			set[value] = struct{}{}
		}
		normalized.AllowedImageRegistries = sortedStrings(set)
	} else {
		normalized.AllowedImageRegistries = nil
	}
	return normalized, nil
}

func positiveDuration(value *time.Duration, name string) error {
	if value != nil && *value <= 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	return nil
}

func validAction(action LifecycleAction) bool {
	switch action {
	case ActionPause, ActionResume, ActionCancel, ActionRetry, ActionReorder:
		return true
	default:
		return false
	}
}

func validateNarrowing(parent, child Rules) error {
	if err := validateQuotaNarrowing(parent.Quotas, child.Quotas); err != nil {
		return err
	}
	if child.Priority != nil && parent.Priority != nil {
		if child.Priority.Min < parent.Priority.Min || child.Priority.Max > parent.Priority.Max {
			return errors.New("priority range is wider than parent")
		}
	}
	if expandsDuration(parent.MaxDelayedStart, child.MaxDelayedStart) {
		return errors.New("delayed-start horizon exceeds parent")
	}
	if expandsDuration(parent.MaxExecutionDuration, child.MaxExecutionDuration) {
		return errors.New("execution duration exceeds parent")
	}
	for role, actions := range child.RoleLifecycleActions {
		parentActions, exists := parent.RoleLifecycleActions[role]
		if !exists && len(actions) > 0 {
			return fmt.Errorf("role %q adds lifecycle actions absent from parent", role)
		}
		if !isActionSubset(actions, parentActions) {
			return fmt.Errorf("role %q lifecycle actions exceed parent", role)
		}
	}
	if child.HasImageRegistryAllowlist && parent.HasImageRegistryAllowlist &&
		!isStringSubset(child.AllowedImageRegistries, parent.AllowedImageRegistries) {
		return errors.New("image registry allowlist exceeds parent")
	}
	return nil
}

func validateQuotaNarrowing(parent, child QuotaLimits) error {
	checks := []struct {
		name          string
		parent, child ScopedLimits
	}{
		{"global", parent.Global, child.Global},
		{"project", parent.Project, child.Project},
		{"namespace", parent.Namespace, child.Namespace},
	}
	for _, check := range checks {
		fields := []struct {
			name          string
			parent, child *uint64
		}{
			{"concurrency", check.parent.MaxConcurrent, check.child.MaxConcurrent},
			{"queued jobs", check.parent.MaxQueued, check.child.MaxQueued},
			{"retained jobs", check.parent.MaxRetained, check.child.MaxRetained},
		}
		for _, field := range fields {
			if field.child != nil && field.parent != nil && *field.child > *field.parent {
				return fmt.Errorf("%s %s limit exceeds parent", check.name, field.name)
			}
		}
	}
	return nil
}

func expandsDuration(parent, child *time.Duration) bool {
	return parent != nil && child != nil && *child > *parent
}

func overlay(parent, child Rules) Rules {
	result := parent
	result.Quotas.Global = overlayLimits(parent.Quotas.Global, child.Quotas.Global)
	result.Quotas.Project = overlayLimits(parent.Quotas.Project, child.Quotas.Project)
	result.Quotas.Namespace = overlayLimits(parent.Quotas.Namespace, child.Quotas.Namespace)
	if child.Priority != nil {
		value := *child.Priority
		result.Priority = &value
	}
	if child.MaxDelayedStart != nil {
		value := *child.MaxDelayedStart
		result.MaxDelayedStart = &value
	}
	if child.MaxExecutionDuration != nil {
		value := *child.MaxExecutionDuration
		result.MaxExecutionDuration = &value
	}
	if result.RoleLifecycleActions == nil {
		result.RoleLifecycleActions = make(map[string][]LifecycleAction)
	}
	for role, actions := range child.RoleLifecycleActions {
		result.RoleLifecycleActions[role] = append([]LifecycleAction(nil), actions...)
	}
	if child.HasImageRegistryAllowlist {
		result.HasImageRegistryAllowlist = true
		result.AllowedImageRegistries = append([]string(nil), child.AllowedImageRegistries...)
	}
	return result
}

func overlayLimits(parent, child ScopedLimits) ScopedLimits {
	return ScopedLimits{
		MaxConcurrent: overlayUint(parent.MaxConcurrent, child.MaxConcurrent),
		MaxQueued:     overlayUint(parent.MaxQueued, child.MaxQueued),
		MaxRetained:   overlayUint(parent.MaxRetained, child.MaxRetained),
	}
}

func overlayUint(parent, child *uint64) *uint64 {
	if child != nil {
		value := *child
		return &value
	}
	if parent == nil {
		return nil
	}
	value := *parent
	return &value
}

func validateComplete(rules Rules) error {
	scopes := []struct {
		name   string
		limits ScopedLimits
	}{
		{"global", rules.Quotas.Global},
		{"project", rules.Quotas.Project},
		{"namespace", rules.Quotas.Namespace},
	}
	for _, scope := range scopes {
		if scope.limits.MaxConcurrent == nil || scope.limits.MaxQueued == nil || scope.limits.MaxRetained == nil {
			return fmt.Errorf("%s concurrency, queued, and retained limits are required", scope.name)
		}
	}
	if rules.Priority == nil || rules.MaxDelayedStart == nil || rules.MaxExecutionDuration == nil {
		return errors.New("priority, delayed-start, and execution-duration rules are required")
	}
	return nil
}

func isActionSubset(child, parent []LifecycleAction) bool {
	set := make(map[LifecycleAction]struct{}, len(parent))
	for _, value := range parent {
		set[value] = struct{}{}
	}
	for _, value := range child {
		if _, exists := set[value]; !exists {
			return false
		}
	}
	return true
}

func isStringSubset(child, parent []string) bool {
	set := make(map[string]struct{}, len(parent))
	for _, value := range parent {
		set[value] = struct{}{}
	}
	for _, value := range child {
		if _, exists := set[value]; !exists {
			return false
		}
	}
	return true
}

func sortedActions(set map[LifecycleAction]struct{}) []LifecycleAction {
	values := make([]LifecycleAction, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func sortedStrings(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// NormalizeRegistry returns a stable registry host form for policy comparison.
func NormalizeRegistry(registry string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(registry))
	value = strings.TrimSuffix(value, ".")
	if value == "" || strings.Contains(value, "://") || strings.ContainsAny(value, "/@ \t\r\n") {
		return "", fmt.Errorf("invalid image registry %q", registry)
	}
	if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ":") {
		return "", fmt.Errorf("invalid image registry %q", registry)
	}
	return value, nil
}

// RegistryForImage applies the Docker image-name default: an image whose
// first component has no dot or port and is not localhost uses docker.io.
func RegistryForImage(image string) (string, error) {
	value := strings.TrimSpace(image)
	if value == "" || strings.Contains(value, "://") || strings.ContainsAny(value, " \t\r\n") {
		return "", fmt.Errorf("invalid image reference %q", image)
	}
	if !strings.Contains(value, "/") {
		return "docker.io", nil
	}
	first := strings.SplitN(value, "/", 2)[0]
	if !strings.Contains(first, ".") && !strings.Contains(first, ":") && !strings.EqualFold(first, "localhost") {
		return "docker.io", nil
	}
	return NormalizeRegistry(first)
}

func (policy EffectivePolicy) AllowsImage(image string) (bool, error) {
	if !policy.Rules.HasImageRegistryAllowlist {
		return true, nil
	}
	registry, err := RegistryForImage(image)
	if err != nil {
		return false, err
	}
	for _, allowed := range policy.Rules.AllowedImageRegistries {
		if registry == allowed {
			return true, nil
		}
	}
	return false, nil
}
