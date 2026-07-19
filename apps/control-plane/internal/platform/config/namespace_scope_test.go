package config

import (
	"reflect"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestNamespaceScopeFromEnvironmentDefaultsSelectedModeToReleaseNamespace(t *testing.T) {
	t.Setenv("KUBEQUEUE_RELEASE_NAMESPACE", "kubequeue")
	t.Setenv("KUBEQUEUE_WATCH_MODE", "")
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "")
	t.Setenv("KUBEQUEUE_EXCLUDED_NAMESPACES", "")

	scope, err := NamespaceScopeFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if scope.Mode() != domain.WatchModeSelected ||
		!reflect.DeepEqual(scope.Namespaces(), []string{"kubequeue"}) {
		t.Fatalf("scope = mode %q namespaces %#v", scope.Mode(), scope.Namespaces())
	}
}

func TestNamespaceScopeFromEnvironmentDefendsAllModeSystemNamespaces(t *testing.T) {
	t.Setenv("KUBEQUEUE_RELEASE_NAMESPACE", "kubequeue")
	t.Setenv("KUBEQUEUE_WATCH_MODE", "all")
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "")
	t.Setenv("KUBEQUEUE_EXCLUDED_NAMESPACES", "custom, kube-system")

	scope, err := NamespaceScopeFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if scope.Mode() != domain.WatchModeAll || !scope.Allows("batch") {
		t.Fatalf("scope = mode %q", scope.Mode())
	}
	for _, excluded := range []string{
		"custom", "kube-system", "kube-public", "kube-node-lease", "kubequeue",
	} {
		if scope.Allows(excluded) {
			t.Fatalf("excluded namespace %q is allowed", excluded)
		}
	}
}
