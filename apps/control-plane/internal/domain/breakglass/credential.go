// Package breakglass owns the bounded token and lifetime rules for the
// explicitly configured on-premises emergency credential.
package breakglass

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	Marker              = "kqbg"
	MinimumDigestKey    = 32
	MaximumLifetime     = 24 * time.Hour
	MaximumOverlap      = 10 * time.Minute
	FailureWindow       = time.Minute
	FailureLimit        = 5
	DurableFailureLimit = 10
	BlockDuration       = 5 * time.Minute
)

var (
	ErrInvalidCredential = errors.New("break-glass credential invalid")
	ErrExpired           = errors.New("break-glass credential expired")
	ErrRevoked           = errors.New("break-glass credential revoked")
	ErrRateLimited       = errors.New("break-glass authentication rate limited")
	tokenPattern         = regexp.MustCompile(`^kqbg\.([A-Za-z0-9_-]{16})\.([A-Za-z0-9_-]{43})$`)
)

type Digest [sha256.Size]byte

func (d Digest) Bytes() []byte {
	return append([]byte(nil), d[:]...)
}

func (d Digest) Equal(other Digest) bool {
	return subtle.ConstantTimeCompare(d[:], other[:]) == 1
}

type Credential struct {
	Slot           string
	Prefix         string
	Digest         Digest
	ExpiresAt      time.Time
	OverlapExpires *time.Time
	RevokedAt      *time.Time
	LastUsedAt     time.Time
}

func IsReserved(candidate string) bool {
	return strings.HasPrefix(candidate, Marker+".")
}

func Parse(candidate string) (string, error) {
	matches := tokenPattern.FindStringSubmatch(candidate)
	if len(matches) != 3 {
		return "", ErrInvalidCredential
	}
	if prefix, err := base64.RawURLEncoding.DecodeString(matches[1]); err != nil || len(prefix) != 12 {
		return "", ErrInvalidCredential
	}
	if secret, err := base64.RawURLEncoding.DecodeString(matches[2]); err != nil || len(secret) != 32 {
		return "", ErrInvalidCredential
	}
	return matches[1], nil
}

func KeyedDigest(key []byte, candidate string) (Digest, error) {
	if len(key) < MinimumDigestKey {
		return Digest{}, ErrInvalidCredential
	}
	if _, err := Parse(candidate); err != nil {
		return Digest{}, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(candidate))
	var digest Digest
	copy(digest[:], mac.Sum(nil))
	return digest, nil
}

func (c Credential) Validate(candidate string, key []byte, now time.Time) error {
	digest, err := KeyedDigest(key, candidate)
	if err != nil || !c.Digest.Equal(digest) {
		return ErrInvalidCredential
	}
	if c.RevokedAt != nil {
		return ErrRevoked
	}
	if c.ExpiresAt.IsZero() || !now.Before(c.ExpiresAt) {
		return ErrExpired
	}
	if c.Slot == "previous" &&
		(c.OverlapExpires == nil || !now.Before(*c.OverlapExpires)) {
		return ErrExpired
	}
	return nil
}
