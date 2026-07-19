package domain

import (
	"errors"
	"time"
)

var (
	ErrLocalAuthenticationFailed  = errors.New("local authentication failed")
	ErrLocalAuthenticationLimited = errors.New("local authentication temporarily unavailable")
	ErrLocalAccountNotFound       = errors.New("local account not found")
	ErrLocalPasswordConflict      = errors.New("local password changed concurrently")
)

const AuthenticationMethodLocal = "LOCAL"

type LocalAccount struct {
	PrincipalID    PrincipalID
	InstallationID InstallationID
	Username       string
	PasswordHash   string
	Disabled       bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (a LocalAccount) Actor() Actor {
	return Actor{
		PrincipalID:          a.PrincipalID,
		InstallationID:       a.InstallationID,
		AuthenticationMethod: AuthenticationMethodLocal,
		CredentialID:         "local:" + string(a.PrincipalID),
	}
}
