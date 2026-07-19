package domain

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type IdentityProviderState string
type IdentityProviderTestStatus string

const (
	IdentityProviderDisabled IdentityProviderState = "DISABLED"
	IdentityProviderEnabled  IdentityProviderState = "ENABLED"

	IdentityProviderNotTested  IdentityProviderTestStatus = "NOT_TESTED"
	IdentityProviderTestPassed IdentityProviderTestStatus = "PASSED"
	IdentityProviderTestFailed IdentityProviderTestStatus = "FAILED"
)

var (
	ErrIdentityProviderNotFound     = errors.New("identity provider not found")
	ErrIdentityProviderConflict     = errors.New("identity provider version conflict")
	ErrIdentityProviderUnsafeChange = errors.New("identity provider change would remove the final login path")
	ErrIdentityProviderTestRequired = errors.New("identity provider must pass a current-version test")
	identityProviderIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

type IdentityProviderConfiguration struct {
	DisplayName       string
	Issuer            string
	Audience          string
	ClientID          string
	ClientSecret      string
	ClientSecretRef   string
	RedirectURI       string
	AuthorizedParty   string
	AllowedAlgorithms []string
	MappingType       string
	MappingValue      string
	GroupsClaim       string
	EmailClaim        string
	NameClaim         string
	CacheTTL          time.Duration
}

type ManagedIdentityProvider struct {
	ID                     string
	InstallationID         InstallationID
	Configuration          IdentityProviderConfiguration
	ClientSecretCiphertext string
	ClientSecretConfigured bool
	State                  IdentityProviderState
	TestStatus             IdentityProviderTestStatus
	TestedAt               *time.Time
	TestMessage            string
	TestedVersion          uint64
	Version                uint64
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func (c IdentityProviderConfiguration) Validate() error {
	if strings.TrimSpace(c.DisplayName) == "" || len(c.DisplayName) > 128 ||
		strings.TrimSpace(c.ClientID) == "" || len(c.ClientID) > 256 ||
		len(c.ClientSecret) > 4096 || len(c.ClientSecretRef) > 512 ||
		(c.ClientSecret != "" && c.ClientSecretRef != "") {
		return errors.New("invalid OIDC client configuration")
	}
	redirect, err := url.Parse(c.RedirectURI)
	if err != nil || redirect.Scheme == "" || redirect.Host == "" ||
		redirect.User != nil || redirect.RawQuery != "" || redirect.Fragment != "" ||
		(redirect.Scheme != "https" &&
			(redirect.Scheme != "http" || !isLoopbackHost(redirect.Hostname()))) {
		return errors.New("OIDC redirect URI must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	if (c.MappingType == "") != (c.MappingValue == "") ||
		(c.MappingType != "" && c.MappingType != OIDCMappingGroup && c.MappingType != OIDCMappingDomain) ||
		len(c.MappingValue) > 256 {
		return errors.New("invalid OIDC provisioning mapping")
	}
	return c.RuntimeProvider("", "").Validate()
}

func (c IdentityProviderConfiguration) RuntimeProvider(
	id string, installationID InstallationID,
) OIDCProvider {
	if id == "" {
		id = "provider"
	}
	if installationID == "" {
		installationID = "installation"
	}
	return OIDCProvider{
		ID: id, InstallationID: installationID, Issuer: c.Issuer, Audience: c.Audience,
		AuthorizedParty: c.AuthorizedParty, AllowedAlgorithms: c.AllowedAlgorithms,
		GroupsClaim: c.GroupsClaim, EmailClaim: c.EmailClaim, NameClaim: c.NameClaim,
		CacheTTL: c.CacheTTL,
	}
}

func (p ManagedIdentityProvider) Validate() error {
	if !identityProviderIDPattern.MatchString(p.ID) || p.InstallationID == "" ||
		p.Version == 0 || p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		return errors.New("invalid identity provider")
	}
	if err := p.Configuration.Validate(); err != nil {
		return err
	}
	if p.State != IdentityProviderDisabled && p.State != IdentityProviderEnabled {
		return errors.New("invalid identity provider state")
	}
	switch p.TestStatus {
	case IdentityProviderNotTested, IdentityProviderTestPassed, IdentityProviderTestFailed:
	default:
		return errors.New("invalid identity provider test status")
	}
	return nil
}

func (p ManagedIdentityProvider) RuntimeProvider() OIDCProvider {
	return p.Configuration.RuntimeProvider(p.ID, p.InstallationID)
}

func (p ManagedIdentityProvider) CanEnable() bool {
	return p.TestStatus == IdentityProviderTestPassed && p.TestedVersion == p.Version
}
