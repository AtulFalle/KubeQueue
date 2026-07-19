package policyquota

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestComposeUsesMostRestrictiveRules(t *testing.T) {
	hour := time.Hour
	halfHour := 30 * time.Minute
	fifteenMinutes := 15 * time.Minute
	twoHours := 2 * time.Hour

	installation := completePolicy("installation", 1, Scope{Kind: ScopeInstallation}, 100)
	installation.Rules.Priority = &PriorityRange{Min: -100, Max: 100, Default: 0}
	installation.Rules.MaxDelayedStart = &twoHours
	installation.Rules.MaxExecutionDuration = &hour
	installation.Rules.RoleLifecycleActions = map[string][]LifecycleAction{
		"operator": {ActionPause, ActionResume, ActionCancel, ActionRetry, ActionReorder},
		"viewer":   {},
	}
	installation.Rules.HasImageRegistryAllowlist = true
	installation.Rules.AllowedImageRegistries = []string{"REGISTRY.EXAMPLE.COM.", "docker.io"}

	project := Policy{
		Ref: PolicyRef{ID: "project", Version: 4, Scope: Scope{Kind: ScopeProject, Project: "project-a"}},
		Rules: Rules{
			Quotas: QuotaLimits{
				Project:   ScopedLimits{MaxConcurrent: uint64Pointer(40), MaxQueued: uint64Pointer(60)},
				Namespace: ScopedLimits{MaxConcurrent: uint64Pointer(20)},
			},
			Priority:             &PriorityRange{Min: -10, Max: 50, Default: 5},
			MaxDelayedStart:      &hour,
			MaxExecutionDuration: &halfHour,
			RoleLifecycleActions: map[string][]LifecycleAction{
				"operator": {ActionPause, ActionResume, ActionCancel},
			},
			HasImageRegistryAllowlist: true,
			AllowedImageRegistries:    []string{"registry.example.com"},
		},
	}
	namespace := Policy{
		Ref: PolicyRef{ID: "namespace", Version: 7, Scope: Scope{Kind: ScopeNamespace, Project: "project-a", Namespace: "builds"}},
		Rules: Rules{
			Quotas: QuotaLimits{
				Namespace: ScopedLimits{
					MaxConcurrent: uint64Pointer(10),
					MaxQueued:     uint64Pointer(30),
					MaxRetained:   uint64Pointer(80),
				},
			},
			Priority:             &PriorityRange{Min: 0, Max: 20, Default: 10},
			MaxDelayedStart:      &halfHour,
			MaxExecutionDuration: &fifteenMinutes,
			RoleLifecycleActions: map[string][]LifecycleAction{
				"operator": {ActionCancel},
			},
		},
	}

	effective, err := Compose(installation, project, namespace)
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}

	assertLimit(t, "global concurrency", effective.Rules.Quotas.Global.MaxConcurrent, 100)
	assertLimit(t, "project concurrency", effective.Rules.Quotas.Project.MaxConcurrent, 40)
	assertLimit(t, "namespace concurrency", effective.Rules.Quotas.Namespace.MaxConcurrent, 10)
	assertLimit(t, "namespace queued", effective.Rules.Quotas.Namespace.MaxQueued, 30)
	assertLimit(t, "namespace retained", effective.Rules.Quotas.Namespace.MaxRetained, 80)
	if got := *effective.Rules.Priority; got != (PriorityRange{Min: 0, Max: 20, Default: 10}) {
		t.Errorf("priority = %+v", got)
	}
	if got := *effective.Rules.MaxExecutionDuration; got != fifteenMinutes {
		t.Errorf("maximum execution duration = %s", got)
	}
	if got := effective.Rules.RoleLifecycleActions["operator"]; !reflect.DeepEqual(got, []LifecycleAction{ActionCancel}) {
		t.Errorf("operator actions = %v", got)
	}
	if got := effective.Rules.AllowedImageRegistries; !reflect.DeepEqual(got, []string{"registry.example.com"}) {
		t.Errorf("registries = %v", got)
	}
	if len(effective.Applied) != 3 || effective.Applied[2].Version != 7 {
		t.Errorf("applied policy versions = %+v", effective.Applied)
	}
}

func TestComposeRejectsExpansion(t *testing.T) {
	parent := completePolicy("installation", 1, Scope{Kind: ScopeInstallation}, 100)
	parent.Rules.RoleLifecycleActions = map[string][]LifecycleAction{
		"operator": {ActionCancel},
	}
	parent.Rules.HasImageRegistryAllowlist = true
	parent.Rules.AllowedImageRegistries = []string{"registry.example.com"}

	tests := []struct {
		name  string
		rules Rules
	}{
		{
			name: "quota",
			rules: Rules{Quotas: QuotaLimits{
				Project: ScopedLimits{MaxConcurrent: uint64Pointer(101)},
			}},
		},
		{
			name:  "priority",
			rules: Rules{Priority: &PriorityRange{Min: -101, Max: 100, Default: 0}},
		},
		{
			name: "lifecycle action",
			rules: Rules{RoleLifecycleActions: map[string][]LifecycleAction{
				"operator": {ActionCancel, ActionRetry},
			}},
		},
		{
			name: "image registry",
			rules: Rules{
				HasImageRegistryAllowlist: true,
				AllowedImageRegistries:    []string{"registry.example.com", "other.example.com"},
			},
		},
		{
			name:  "execution duration",
			rules: Rules{MaxExecutionDuration: durationPointer(25 * time.Hour)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			child := Policy{
				Ref:   PolicyRef{ID: "project", Version: 2, Scope: Scope{Kind: ScopeProject, Project: "project-a"}},
				Rules: test.rules,
			}
			_, err := Compose(parent, child)
			if !errors.Is(err, ErrPolicyExpansion) {
				t.Fatalf("Compose() error = %v, want ErrPolicyExpansion", err)
			}
		})
	}
}

func TestPolicyBoundsAndRegistryNormalization(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Policy)
		wantErr bool
	}{
		{
			name: "priority default below minimum",
			mutate: func(policy *Policy) {
				policy.Rules.Priority = &PriorityRange{Min: 1, Max: 10, Default: 0}
			},
			wantErr: true,
		},
		{
			name: "zero delayed horizon",
			mutate: func(policy *Policy) {
				policy.Rules.MaxDelayedStart = durationPointer(0)
			},
			wantErr: true,
		},
		{
			name: "registry path rejected",
			mutate: func(policy *Policy) {
				policy.Rules.HasImageRegistryAllowlist = true
				policy.Rules.AllowedImageRegistries = []string{"registry.example.com/team"}
			},
			wantErr: true,
		},
		{
			name: "normalized registry accepted",
			mutate: func(policy *Policy) {
				policy.Rules.HasImageRegistryAllowlist = true
				policy.Rules.AllowedImageRegistries = []string{"REGISTRY.EXAMPLE.COM."}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := completePolicy("installation", 1, Scope{Kind: ScopeInstallation}, 10)
			test.mutate(&policy)
			effective, err := Compose(policy)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidPolicy) {
					t.Fatalf("Compose() error = %v, want ErrInvalidPolicy", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Compose() error = %v", err)
			}
			if effective.Rules.HasImageRegistryAllowlist &&
				!reflect.DeepEqual(effective.Rules.AllowedImageRegistries, []string{"registry.example.com"}) {
				t.Errorf("normalized registries = %v", effective.Rules.AllowedImageRegistries)
			}
		})
	}
}

func TestAllowsImage(t *testing.T) {
	policy := completePolicy("installation", 1, Scope{Kind: ScopeInstallation}, 10)
	policy.Rules.HasImageRegistryAllowlist = true
	policy.Rules.AllowedImageRegistries = []string{"docker.io", "registry.example.com:5000"}
	effective, err := Compose(policy)
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}

	tests := []struct {
		image string
		want  bool
	}{
		{image: "alpine:3.20", want: true},
		{image: "library/alpine:3.20", want: true},
		{image: "registry.example.com:5000/team/job:v1", want: true},
		{image: "other.example.com/team/job:v1", want: false},
	}
	for _, test := range tests {
		t.Run(test.image, func(t *testing.T) {
			got, err := effective.AllowsImage(test.image)
			if err != nil {
				t.Fatalf("AllowsImage() error = %v", err)
			}
			if got != test.want {
				t.Errorf("AllowsImage() = %t, want %t", got, test.want)
			}
		})
	}
}

func completePolicy(id string, version uint64, scope Scope, limit uint64) Policy {
	delayed := 24 * time.Hour
	execution := 24 * time.Hour
	return Policy{
		Ref: PolicyRef{ID: id, Version: version, Scope: scope},
		Rules: Rules{
			Quotas: QuotaLimits{
				Global:    completeLimits(limit),
				Project:   completeLimits(limit),
				Namespace: completeLimits(limit),
			},
			Priority:             &PriorityRange{Min: -100, Max: 100, Default: 0},
			MaxDelayedStart:      &delayed,
			MaxExecutionDuration: &execution,
		},
	}
}

func completeLimits(limit uint64) ScopedLimits {
	return ScopedLimits{
		MaxConcurrent: uint64Pointer(limit),
		MaxQueued:     uint64Pointer(limit),
		MaxRetained:   uint64Pointer(limit),
	}
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}

func durationPointer(value time.Duration) *time.Duration {
	return &value
}

func assertLimit(t *testing.T, name string, got *uint64, want uint64) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %d", name, got, want)
	}
}
