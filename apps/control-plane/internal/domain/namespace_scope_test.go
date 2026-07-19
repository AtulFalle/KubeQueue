package domain

import (
	"reflect"
	"testing"
)

func TestSelectedNamespaceScopeNormalizesAndRestricts(t *testing.T) {
	t.Parallel()
	scope, err := NewNamespaceScope(
		WatchModeSelected,
		[]string{" batch-jobs ", "default", "batch-jobs", ""},
		[]string{"ignored"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := scope.Namespaces(), []string{"batch-jobs", "default"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Namespaces() = %#v, want %#v", got, want)
	}
	if !scope.Allows("default") || scope.Allows("other") {
		t.Fatalf("selected scope allowed unexpected namespaces")
	}
}

func TestAllNamespaceScopeAppliesExclusions(t *testing.T) {
	t.Parallel()
	scope, err := NewNamespaceScope(
		WatchModeAll, nil, []string{"kube-system", "kubequeue"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Allows("batch") || scope.Allows("kube-system") || scope.Allows("kubequeue") {
		t.Fatalf("all scope did not apply exclusions")
	}
}

func TestNamespaceScopeRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mode       WatchMode
		namespaces []string
		excluded   []string
	}{
		{name: "unknown mode", mode: "automatic", namespaces: []string{"default"}},
		{name: "selected empty", mode: WatchModeSelected},
		{name: "invalid selected namespace", mode: WatchModeSelected, namespaces: []string{"UPPER"}},
		{name: "invalid exclusion", mode: WatchModeAll, excluded: []string{"not_valid"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewNamespaceScope(test.mode, test.namespaces, test.excluded); err == nil {
				t.Fatal("NewNamespaceScope() error = nil")
			}
		})
	}
}
