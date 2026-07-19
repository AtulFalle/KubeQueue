package domain

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	OIDCMappingGroup  = "GROUP"
	OIDCMappingDomain = "DOMAIN"
)

var (
	ErrIdentityDisabled      = errors.New("principal is disabled")
	ErrJITProvisioningDenied = errors.New("no OIDC provisioning mapping grants access")
)

type OIDCProvider struct {
	ID                string
	InstallationID    InstallationID
	Issuer            string
	Audience          string
	AuthorizedParty   string
	AllowedAlgorithms []string
	GroupsClaim       string
	EmailClaim        string
	NameClaim         string
	CacheTTL          time.Duration
}

type OIDCIdentityClaims struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
	Groups        []string
}

func (p OIDCProvider) Validate() error {
	if err := validateIdentityID("identity provider", p.ID); err != nil {
		return err
	}
	if err := validateIdentityID("installation", string(p.InstallationID)); err != nil {
		return err
	}
	issuer, err := url.Parse(p.Issuer)
	if err != nil || issuer.Scheme == "" || issuer.Host == "" ||
		(issuer.Scheme != "https" && issuer.Scheme != "http") ||
		issuer.RawQuery != "" || issuer.Fragment != "" {
		return errors.New("OIDC issuer must be an absolute HTTP(S) URL without query or fragment")
	}
	if issuer.Scheme != "https" && !isLoopbackHost(issuer.Hostname()) {
		return errors.New("OIDC issuer must use HTTPS except on loopback")
	}
	if strings.TrimSpace(p.Issuer) != p.Issuer || strings.HasSuffix(p.Issuer, "/") {
		return errors.New("OIDC issuer must be canonical and must not have a trailing slash")
	}
	if strings.TrimSpace(p.Audience) == "" {
		return errors.New("OIDC audience is required")
	}
	if len(p.AllowedAlgorithms) == 0 {
		return errors.New("at least one OIDC signing algorithm is required")
	}
	seen := make(map[string]struct{}, len(p.AllowedAlgorithms))
	for _, algorithm := range p.AllowedAlgorithms {
		if !slices.Contains([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}, algorithm) {
			return fmt.Errorf("OIDC signing algorithm %q is not an allowlisted asymmetric algorithm", algorithm)
		}
		if _, duplicate := seen[algorithm]; duplicate {
			return fmt.Errorf("OIDC signing algorithm %q is duplicated", algorithm)
		}
		seen[algorithm] = struct{}{}
	}
	for name, claim := range map[string]string{
		"groups": p.GroupsClaim,
		"email":  p.EmailClaim,
		"name":   p.NameClaim,
	} {
		if strings.TrimSpace(claim) == "" {
			return fmt.Errorf("OIDC %s claim name is required", name)
		}
	}
	if p.CacheTTL <= 0 {
		return errors.New("OIDC cache TTL must be positive")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") ||
		(net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}
