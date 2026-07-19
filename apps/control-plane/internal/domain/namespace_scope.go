package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type WatchMode string

const (
	WatchModeSelected WatchMode = "selected"
	WatchModeAll      WatchMode = "all"
)

var (
	ErrNamespaceOutOfScope  = errors.New("namespace is outside the effective KubeQueue scope")
	ErrNamespaceUnavailable = errors.New("namespace is not ready for KubeQueue submissions")
)

type NamespaceScope struct {
	mode       WatchMode
	namespaces []string
	excluded   []string
	allowed    map[string]struct{}
	blocked    map[string]struct{}
}

func NewNamespaceScope(
	mode WatchMode, namespaces, excludedNamespaces []string,
) (NamespaceScope, error) {
	if mode != WatchModeSelected && mode != WatchModeAll {
		return NamespaceScope{}, fmt.Errorf("unsupported watch mode %q", mode)
	}
	normalizedNamespaces, err := normalizeNamespaces(namespaces)
	if err != nil {
		return NamespaceScope{}, err
	}
	normalizedExcluded, err := normalizeNamespaces(excludedNamespaces)
	if err != nil {
		return NamespaceScope{}, err
	}
	if mode == WatchModeSelected && len(normalizedNamespaces) == 0 {
		return NamespaceScope{}, errors.New("selected watch mode requires at least one namespace")
	}

	scope := NamespaceScope{
		mode:       mode,
		namespaces: normalizedNamespaces,
		excluded:   normalizedExcluded,
		allowed:    make(map[string]struct{}, len(normalizedNamespaces)),
		blocked:    make(map[string]struct{}, len(normalizedExcluded)),
	}
	for _, namespace := range normalizedNamespaces {
		scope.allowed[namespace] = struct{}{}
	}
	for _, namespace := range normalizedExcluded {
		scope.blocked[namespace] = struct{}{}
	}
	return scope, nil
}

func (s NamespaceScope) Mode() WatchMode {
	return s.mode
}

func (s NamespaceScope) Namespaces() []string {
	return append([]string(nil), s.namespaces...)
}

func (s NamespaceScope) ExcludedNamespaces() []string {
	return append([]string(nil), s.excluded...)
}

func (s NamespaceScope) Allows(namespace string) bool {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return false
	}
	if s.mode == WatchModeSelected {
		_, allowed := s.allowed[namespace]
		return allowed
	}
	_, blocked := s.blocked[namespace]
	return !blocked
}

func normalizeNamespaces(values []string) ([]string, error) {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		namespace := strings.TrimSpace(value)
		if namespace == "" {
			continue
		}
		if len(namespace) > 63 || !dnsLabel.MatchString(namespace) {
			return nil, fmt.Errorf("namespace %q must be a valid Kubernetes DNS label", namespace)
		}
		unique[namespace] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for namespace := range unique {
		result = append(result, namespace)
	}
	sort.Strings(result)
	return result, nil
}
