package domain

import "errors"

var ErrAccessDenied = errors.New("access denied")

const (
	AuthenticationMethodNativeServiceAccount  = "NATIVE_SERVICE_ACCOUNT"
	AuthenticationMethodOIDCClientCredentials = "OIDC_CLIENT_CREDENTIALS"
	AuthenticationMethodBreakGlass            = "BREAK_GLASS"
)

type Actor struct {
	PrincipalID           PrincipalID
	InstallationID        InstallationID
	IdentityProviderID    string
	AuthenticationMethod  string
	CredentialID          string
	CredentialPermissions []Permission
	CredentialScope       AccessScope
}

type AuthorizationScope struct {
	InstallationID InstallationID
	ProjectID      ProjectID
}

type AccessScope struct {
	InstallationID   InstallationID
	InstallationWide bool
	ProjectIDs       []ProjectID
}
