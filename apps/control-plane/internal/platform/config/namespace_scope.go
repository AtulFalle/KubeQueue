package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

var defaultExcludedNamespaces = []string{
	"kube-system",
	"kube-public",
	"kube-node-lease",
}

func NamespaceScopeFromEnvironment() (domain.NamespaceScope, error) {
	releaseNamespace := strings.TrimSpace(os.Getenv("KUBEQUEUE_RELEASE_NAMESPACE"))
	if releaseNamespace == "" {
		releaseNamespace = "default"
	}
	mode := domain.WatchMode(strings.TrimSpace(os.Getenv("KUBEQUEUE_WATCH_MODE")))
	if mode == "" {
		mode = domain.WatchModeSelected
	}
	namespaces := splitNamespaces(os.Getenv("KUBEQUEUE_WATCH_NAMESPACES"))
	excluded := splitNamespaces(os.Getenv("KUBEQUEUE_EXCLUDED_NAMESPACES"))
	if mode == domain.WatchModeSelected && len(namespaces) == 0 {
		namespaces = []string{releaseNamespace}
	}
	if mode == domain.WatchModeAll {
		excluded = append(excluded, defaultExcludedNamespaces...)
		excluded = append(excluded, releaseNamespace)
	}
	scope, err := domain.NewNamespaceScope(mode, namespaces, excluded)
	if err != nil {
		return domain.NamespaceScope{}, fmt.Errorf("configure namespace scope: %w", err)
	}
	return scope, nil
}

func splitNamespaces(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}
