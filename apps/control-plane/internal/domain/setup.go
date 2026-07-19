package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrSetupUnavailable   = errors.New("first-time setup is unavailable")
	ErrSetupClaimConflict = errors.New("setup has already been claimed")
)

type SetupReadiness struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

type SetupStatus struct {
	Available           bool           `json:"available"`
	State               string         `json:"state"`
	API                 SetupReadiness `json:"api"`
	Database            SetupReadiness `json:"database"`
	Schema              SetupReadiness `json:"schema"`
	Worker              SetupReadiness `json:"worker"`
	KubernetesAuthority SetupReadiness `json:"kubernetesAuthority"`
	Release             SetupReadiness `json:"release"`
	PublicURL           SetupReadiness `json:"publicUrl"`
}

type SetupPolicy struct {
	GlobalConcurrency    int `json:"globalConcurrency"`
	NamespaceConcurrency int `json:"namespaceConcurrency"`
	QueueCapacity        int `json:"queueCapacity"`
	MinimumPriority      int `json:"minimumPriority"`
	MaximumPriority      int `json:"maximumPriority"`
	MaximumDelaySeconds  int `json:"maximumDelaySeconds"`
	MaximumRunningJobs   int `json:"maximumRunningJobs"`
	MaximumQueuedJobs    int `json:"maximumQueuedJobs"`
}

type SetupClaimInput struct {
	InstallationName string          `json:"installationName"`
	LocalAdmin       SetupLocalAdmin `json:"localAdmin"`
	ProjectName      string          `json:"projectName"`
	Namespaces       []string        `json:"namespaces"`
	Policy           SetupPolicy     `json:"policy"`
}

type SetupLocalAdmin struct {
	Username     string      `json:"username"`
	Password     string      `json:"-"`
	PasswordHash string      `json:"-"`
	PrincipalID  PrincipalID `json:"-"`
}

type SetupClaim struct {
	InstallationID     InstallationID `json:"installationId"`
	OwnerPrincipalID   PrincipalID    `json:"ownerPrincipalId"`
	Username           string         `json:"username"`
	Status             string         `json:"status"`
	CreatedAt          time.Time      `json:"createdAt"`
	ID                 string         `json:"-"`
	IdentityProviderID string         `json:"-"`
}

type SetupRecovery struct {
	Completed bool     `json:"completed"`
	Checklist []string `json:"checklist"`
}

func (input SetupClaimInput) Validate() error {
	if strings.TrimSpace(input.InstallationName) == "" || len(input.InstallationName) > 128 {
		return errors.New("installation name is required and must not exceed 128 characters")
	}
	if err := ValidateLocalUsername(input.LocalAdmin.Username); err != nil {
		return err
	}
	if err := ValidateNewLocalPassword(input.LocalAdmin.Password); err != nil {
		return err
	}
	if strings.TrimSpace(input.ProjectName) == "" || len(input.ProjectName) > 128 {
		return errors.New("project name is required and must not exceed 128 characters")
	}
	if len(input.Namespaces) == 0 || len(input.Namespaces) > 100 {
		return errors.New("between 1 and 100 namespaces is required")
	}
	seen := make(map[string]struct{}, len(input.Namespaces))
	for _, namespace := range input.Namespaces {
		binding, err := NewNamespaceBinding("default", namespace)
		if err != nil {
			return err
		}
		if _, duplicate := seen[binding.Namespace]; duplicate {
			return fmt.Errorf("namespace %q is duplicated", binding.Namespace)
		}
		seen[binding.Namespace] = struct{}{}
	}
	p := input.Policy
	if p.GlobalConcurrency < 1 || p.GlobalConcurrency > 10000 ||
		p.NamespaceConcurrency < 1 || p.NamespaceConcurrency > p.GlobalConcurrency ||
		p.QueueCapacity < 1 || p.QueueCapacity > 1000000 ||
		p.MinimumPriority < -1000 || p.MaximumPriority > 1000 ||
		p.MinimumPriority > p.MaximumPriority ||
		p.MaximumDelaySeconds < 0 || p.MaximumDelaySeconds > 31536000 ||
		p.MaximumRunningJobs < 1 || p.MaximumRunningJobs > 10000 ||
		p.MaximumQueuedJobs < 1 || p.MaximumQueuedJobs > 1000000 {
		return errors.New("setup policy is outside supported bounds")
	}
	return nil
}

var localUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func NormalizeLocalUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func ValidateLocalUsername(value string) error {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 128 || !localUsernamePattern.MatchString(value) {
		return errors.New("local username is invalid")
	}
	return nil
}

func ValidateNewLocalPassword(value string) error {
	if len(value) < 12 || len(value) > 128 {
		return errors.New("local password must contain between 12 and 128 characters")
	}
	return nil
}
