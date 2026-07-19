package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type InstallationID string
type ProjectID string
type PrincipalID string
type NamespaceBindingID string

type Installation struct {
	ID   InstallationID
	Name string
}

type Project struct {
	ID             ProjectID
	InstallationID InstallationID
	Name           string
}

type Principal struct {
	ID             PrincipalID
	InstallationID InstallationID
	DisplayName    string
}

type NamespaceBinding struct {
	ID             NamespaceBindingID
	InstallationID InstallationID
	ProjectID      ProjectID
	Namespace      string
	Desired        bool
	AuthorityState NamespaceAuthorityState
	InformerSynced bool
	Authorized     bool
	Message        string
	ObservedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type NamespaceAuthorityState string

const (
	NamespaceAuthorityReady          NamespaceAuthorityState = "READY"
	NamespaceAuthorityPending        NamespaceAuthorityState = "PENDING"
	NamespaceAuthorityUnauthorized   NamespaceAuthorityState = "UNAUTHORIZED"
	NamespaceAuthorityUnsynchronized NamespaceAuthorityState = "UNSYNCHRONIZED"
	NamespaceAuthorityOutOfScope     NamespaceAuthorityState = "OUT_OF_SCOPE"
)

type SubmissionSource string

const (
	SubmissionSourceAPI                 SubmissionSource = "API"
	SubmissionSourceKubernetesDiscovery SubmissionSource = "KUBERNETES_DISCOVERY"
	SubmissionSourceLegacyCompatibility SubmissionSource = "LEGACY_COMPATIBILITY"
)

var identityID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func NewInstallation(id InstallationID, name string) (Installation, error) {
	if err := validateIdentityID("installation", string(id)); err != nil {
		return Installation{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Installation{}, errors.New("installation name is required")
	}
	return Installation{ID: id, Name: name}, nil
}

func NewProject(id ProjectID, installationID InstallationID, name string) (Project, error) {
	if err := validateIdentityID("project", string(id)); err != nil {
		return Project{}, err
	}
	if err := validateIdentityID("installation", string(installationID)); err != nil {
		return Project{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, errors.New("project name is required")
	}
	return Project{ID: id, InstallationID: installationID, Name: name}, nil
}

func NewPrincipal(
	id PrincipalID, installationID InstallationID, displayName string,
) (Principal, error) {
	if err := validateIdentityID("principal", string(id)); err != nil {
		return Principal{}, err
	}
	if err := validateIdentityID("installation", string(installationID)); err != nil {
		return Principal{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return Principal{}, errors.New("principal display name is required")
	}
	return Principal{ID: id, InstallationID: installationID, DisplayName: displayName}, nil
}

func NewNamespaceBinding(
	projectID ProjectID, namespace string,
) (NamespaceBinding, error) {
	if err := validateIdentityID("project", string(projectID)); err != nil {
		return NamespaceBinding{}, err
	}
	namespace = strings.TrimSpace(namespace)
	if len(namespace) == 0 || len(namespace) > 63 || !dnsLabel.MatchString(namespace) {
		return NamespaceBinding{}, fmt.Errorf(
			"namespace %q must be a valid Kubernetes DNS label", namespace,
		)
	}
	return NamespaceBinding{
		ID:        NamespaceBindingID(string(projectID) + "__" + namespace),
		ProjectID: projectID,
		Namespace: namespace,
		Desired:   true,
	}, nil
}

func (s SubmissionSource) Valid() bool {
	switch s {
	case SubmissionSourceAPI, SubmissionSourceKubernetesDiscovery,
		SubmissionSourceLegacyCompatibility:
		return true
	default:
		return false
	}
}

func validateIdentityID(kind, value string) error {
	if !identityID.MatchString(value) {
		return fmt.Errorf("%s ID %q must be a lowercase stable identifier", kind, value)
	}
	return nil
}
