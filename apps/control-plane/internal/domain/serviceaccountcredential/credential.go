// Package serviceaccountcredential owns the pure lifecycle rules for native
// service-account credentials. It has no persistence or transport concerns.
package serviceaccountcredential

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const (
	credentialMarker = "kqsa"
	prefixBytes      = 12
	secretBytes      = 32

	MinimumDigestKeyBytes       = 32
	MinimumLifetime             = time.Minute
	AbsoluteMaximumLifetime     = 365 * 24 * time.Hour
	AbsoluteMaximumOverlap      = 30 * time.Minute
	AbsoluteMaximumLastUsedWait = 24 * time.Hour
)

var (
	ErrInvalidPolicy        = errors.New("service-account credential policy invalid")
	ErrInvalidRequest       = errors.New("service-account credential request invalid")
	ErrInvalidCredential    = errors.New("service-account credential invalid")
	ErrCredentialExpired    = errors.New("service-account credential expired")
	ErrCredentialRevoked    = errors.New("service-account credential revoked")
	ErrCredentialRotated    = errors.New("service-account credential rotation overlap ended")
	ErrDelegationDenied     = errors.New("service-account credential delegation denied")
	ErrPlaintextUnavailable = errors.New("service-account credential plaintext unavailable")
)

// ServiceAccount is the persistence-neutral identity owned by this lifecycle.
// An empty ProjectID denotes installation ownership.
type ServiceAccount struct {
	PrincipalID    domain.PrincipalID
	InstallationID domain.InstallationID
	ProjectID      domain.ProjectID
	DisplayName    string
	OIDCIdentity   *OIDCIdentity
	CreatedBy      domain.PrincipalID
	CreatedAt      time.Time
}

// OIDCIdentity selects an existing service account after an access token has
// already passed provider, signature, audience, and time validation.
type OIDCIdentity struct {
	Issuer  string
	Subject string
}

// Credential associates lifecycle metadata with its durable identity and
// service-account principal.
type Credential struct {
	ID                        string
	ServiceAccountPrincipalID domain.PrincipalID
	Stored                    StoredCredential
}

// CredentialMetadata is safe for bounded administrative reads. It deliberately
// excludes both plaintext and the keyed digest.
type CredentialMetadata struct {
	ID                        string
	ServiceAccountPrincipalID domain.PrincipalID
	Prefix                    string
	Permissions               []domain.Permission
	CreatedBy                 domain.PrincipalID
	CreatedAt                 time.Time
	ExpiresAt                 time.Time
	LastUsedAt                time.Time
	RotatedAt                 *time.Time
	OverlapExpiresAt          *time.Time
	RevokedAt                 *time.Time
}

func (c Credential) Metadata() CredentialMetadata {
	return CredentialMetadata{
		ID: c.ID, ServiceAccountPrincipalID: c.ServiceAccountPrincipalID,
		Prefix: c.Stored.Prefix, Permissions: slices.Clone(c.Stored.Permissions),
		CreatedBy: c.Stored.CreatedBy, CreatedAt: c.Stored.CreatedAt,
		ExpiresAt: c.Stored.ExpiresAt, LastUsedAt: c.Stored.LastUsedAt,
		RotatedAt: c.Stored.RotatedAt, OverlapExpiresAt: c.Stored.OverlapExpiresAt,
		RevokedAt: c.Stored.RevokedAt,
	}
}

// Policy bounds every caller-selected lifetime and rotation overlap. ExpiresAt
// remains mandatory on each IssueRequest; the policy never creates an
// unbounded credential implicitly.
type Policy struct {
	MaxLifetime           time.Duration
	MaxRotationOverlap    time.Duration
	LastUsedWriteInterval time.Duration
}

func DefaultPolicy() Policy {
	return Policy{
		MaxLifetime:           90 * 24 * time.Hour,
		MaxRotationOverlap:    5 * time.Minute,
		LastUsedWriteInterval: 5 * time.Minute,
	}
}

func (p Policy) Validate() error {
	if p.MaxLifetime < MinimumLifetime || p.MaxLifetime > AbsoluteMaximumLifetime {
		return ErrInvalidPolicy
	}
	if p.MaxRotationOverlap <= 0 || p.MaxRotationOverlap > AbsoluteMaximumOverlap {
		return ErrInvalidPolicy
	}
	if p.LastUsedWriteInterval <= 0 ||
		p.LastUsedWriteInterval > AbsoluteMaximumLastUsedWait {
		return ErrInvalidPolicy
	}
	return nil
}

// Digest is the keyed, non-reversible storage value. Its formatting methods
// deliberately redact it to reduce accidental disclosure in logs and errors.
type Digest [sha256.Size]byte

func (Digest) String() string   { return "[REDACTED]" }
func (Digest) GoString() string { return "serviceaccountcredential.Digest([REDACTED])" }

func (d Digest) Equal(other Digest) bool {
	return subtle.ConstantTimeCompare(d[:], other[:]) == 1
}

// Bytes returns a copy suitable for a persistence adapter.
func (d Digest) Bytes() []byte {
	result := make([]byte, len(d))
	copy(result, d[:])
	return result
}

// StoredCredential is the complete persistence-neutral storage contract. It
// intentionally cannot contain the plaintext credential.
type StoredCredential struct {
	Prefix           string
	Digest           Digest
	Permissions      []domain.Permission
	CreatedBy        domain.PrincipalID
	CreatedAt        time.Time
	ExpiresAt        time.Time
	LastUsedAt       time.Time
	RotatedAt        *time.Time
	OverlapExpiresAt *time.Time
	RevokedAt        *time.Time
}

func (c StoredCredential) String() string {
	return fmt.Sprintf(
		"service-account credential {prefix=%q created_by=%q expires_at=%s}",
		c.Prefix,
		c.CreatedBy,
		c.ExpiresAt.UTC().Format(time.RFC3339),
	)
}

func (c StoredCredential) GoString() string { return c.String() }

// OneTimePlaintext owns the only plaintext returned by Issue or Rotate. Reveal
// consumes and wipes its internal copy, so subsequent calls cannot recover it.
type OneTimePlaintext struct {
	mu    sync.Mutex
	value []byte
}

func (p *OneTimePlaintext) Reveal() (string, error) {
	if p == nil {
		return "", ErrPlaintextUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.value) == 0 {
		return "", ErrPlaintextUnavailable
	}
	value := string(p.value)
	clear(p.value)
	p.value = nil
	return value, nil
}

func (*OneTimePlaintext) String() string { return "[REDACTED]" }
func (*OneTimePlaintext) GoString() string {
	return "serviceaccountcredential.OneTimePlaintext([REDACTED])"
}

type IssueRequest struct {
	CreatedBy            domain.PrincipalID
	RequestedPermissions []domain.Permission
	CreatorPermissions   []domain.Permission
	DelegablePermissions []domain.Permission
	ExpiresAt            time.Time
}

type IssuedCredential struct {
	Stored    StoredCredential
	Plaintext *OneTimePlaintext
}

func (IssuedCredential) String() string { return "issued service-account credential [REDACTED]" }
func (IssuedCredential) GoString() string {
	return "serviceaccountcredential.IssuedCredential([REDACTED])"
}

type Rotation struct {
	Previous    StoredCredential
	Replacement IssuedCredential
}

// Lifecycle applies credential policy using an injected entropy source and a
// keyed HMAC digest. It is safe for concurrent use when Random is safe for
// concurrent use.
type Lifecycle struct {
	digestKey []byte
	random    io.Reader
	policy    Policy
}

// NewLifecycle uses the operating system cryptographic random source.
func NewLifecycle(digestKey []byte, policy Policy) (*Lifecycle, error) {
	return NewLifecycleWithRandom(digestKey, rand.Reader, policy)
}

// NewLifecycleWithRandom permits deterministic entropy injection for tests.
// Production composition should normally use NewLifecycle.
func NewLifecycleWithRandom(
	digestKey []byte,
	random io.Reader,
	policy Policy,
) (*Lifecycle, error) {
	if len(digestKey) < MinimumDigestKeyBytes || random == nil {
		return nil, ErrInvalidPolicy
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	keyCopy := slices.Clone(digestKey)
	return &Lifecycle{digestKey: keyCopy, random: random, policy: policy}, nil
}

func (*Lifecycle) String() string   { return "service-account credential lifecycle [REDACTED]" }
func (*Lifecycle) GoString() string { return "serviceaccountcredential.Lifecycle([REDACTED])" }

func (l *Lifecycle) Issue(request IssueRequest, now time.Time) (IssuedCredential, error) {
	permissions, err := validateIssueRequest(request, now, l.policy)
	if err != nil {
		return IssuedCredential{}, err
	}

	randomBytes := make([]byte, prefixBytes+secretBytes)
	if _, err := io.ReadFull(l.random, randomBytes); err != nil {
		clear(randomBytes)
		return IssuedCredential{}, fmt.Errorf("generate service-account credential: %w", err)
	}

	prefixLength := base64.RawURLEncoding.EncodedLen(prefixBytes)
	secretLength := base64.RawURLEncoding.EncodedLen(secretBytes)
	plaintextBytes := make([]byte, len(credentialMarker)+2+prefixLength+secretLength)
	copy(plaintextBytes, credentialMarker)
	plaintextBytes[len(credentialMarker)] = '.'
	prefixStart := len(credentialMarker) + 1
	prefixEnd := prefixStart + prefixLength
	base64.RawURLEncoding.Encode(plaintextBytes[prefixStart:prefixEnd], randomBytes[:prefixBytes])
	plaintextBytes[prefixEnd] = '.'
	base64.RawURLEncoding.Encode(plaintextBytes[prefixEnd+1:], randomBytes[prefixBytes:])
	clear(randomBytes)

	prefix := string(plaintextBytes[prefixStart:prefixEnd])
	digest := l.digest(plaintextBytes)
	return IssuedCredential{
		Stored: StoredCredential{
			Prefix:      prefix,
			Digest:      digest,
			Permissions: permissions,
			CreatedBy:   request.CreatedBy,
			CreatedAt:   now,
			ExpiresAt:   request.ExpiresAt,
		},
		Plaintext: &OneTimePlaintext{value: plaintextBytes},
	}, nil
}

// Verify compares the keyed digest in constant time before evaluating
// lifecycle state. Callers should avoid exposing the returned distinction at
// an unauthenticated transport boundary.
func (l *Lifecycle) Verify(stored StoredCredential, candidate string, now time.Time) error {
	candidateDigest := l.digest([]byte(candidate))
	digestMatches := subtle.ConstantTimeCompare(candidateDigest[:], stored.Digest[:])
	prefix := credentialPrefix(candidate)
	prefixMatches := subtle.ConstantTimeCompare([]byte(prefix), []byte(stored.Prefix))
	if digestMatches != 1 || prefixMatches != 1 {
		return ErrInvalidCredential
	}
	return stored.ValidateAt(now)
}

// Prefix returns the non-secret lookup prefix from a structurally valid native
// credential. It never returns the secret portion.
func Prefix(candidate string) (string, error) {
	prefix := credentialPrefix(candidate)
	if prefix == "" {
		return "", ErrInvalidCredential
	}
	return prefix, nil
}

// IsNative reports whether the credential claims the reserved native marker.
// It intentionally does not validate or expose either opaque token component.
func IsNative(candidate string) bool {
	return strings.HasPrefix(candidate, credentialMarker+".")
}

func (l *Lifecycle) Rotate(
	previous StoredCredential,
	request IssueRequest,
	overlap time.Duration,
	now time.Time,
) (Rotation, error) {
	if err := previous.ValidateAt(now); err != nil {
		return Rotation{}, err
	}
	if overlap <= 0 || overlap > l.policy.MaxRotationOverlap {
		return Rotation{}, ErrInvalidRequest
	}
	replacement, err := l.Issue(request, now)
	if err != nil {
		return Rotation{}, err
	}

	overlapExpiresAt := now.Add(overlap)
	if previous.ExpiresAt.Before(overlapExpiresAt) {
		overlapExpiresAt = previous.ExpiresAt
	}
	rotatedAt := now
	previous.RotatedAt = &rotatedAt
	previous.OverlapExpiresAt = &overlapExpiresAt
	return Rotation{Previous: previous, Replacement: replacement}, nil
}

// Revoke is idempotent and preserves the first revocation time.
func Revoke(stored StoredCredential, now time.Time) StoredCredential {
	if stored.RevokedAt == nil {
		revokedAt := now
		stored.RevokedAt = &revokedAt
	}
	return stored
}

func (c StoredCredential) ValidateAt(now time.Time) error {
	if c.RevokedAt != nil {
		return ErrCredentialRevoked
	}
	if c.ExpiresAt.IsZero() || !now.Before(c.ExpiresAt) {
		return ErrCredentialExpired
	}
	if c.RotatedAt != nil {
		if c.OverlapExpiresAt == nil || !now.Before(*c.OverlapExpiresAt) {
			return ErrCredentialRotated
		}
	}
	return nil
}

// ShouldRecordLastUsed makes the write-throttling decision without mutating
// the record. A zero timestamp is always due; clocks moving backwards are not.
func (l *Lifecycle) ShouldRecordLastUsed(lastUsedAt, now time.Time) bool {
	if lastUsedAt.IsZero() {
		return true
	}
	return !now.Before(lastUsedAt.Add(l.policy.LastUsedWriteInterval))
}

func (l *Lifecycle) digest(plaintext []byte) Digest {
	mac := hmac.New(sha256.New, l.digestKey)
	_, _ = mac.Write(plaintext)
	var result Digest
	copy(result[:], mac.Sum(nil))
	return result
}

func validateIssueRequest(
	request IssueRequest,
	now time.Time,
	policy Policy,
) ([]domain.Permission, error) {
	if strings.TrimSpace(string(request.CreatedBy)) == "" ||
		request.ExpiresAt.IsZero() ||
		request.ExpiresAt.Before(now.Add(MinimumLifetime)) ||
		request.ExpiresAt.After(now.Add(policy.MaxLifetime)) ||
		len(request.RequestedPermissions) == 0 {
		return nil, ErrInvalidRequest
	}

	creator, creatorHasInternalAll, err := permissionSet(request.CreatorPermissions, true)
	if err != nil {
		return nil, err
	}
	delegable, _, err := permissionSet(request.DelegablePermissions, false)
	if err != nil {
		return nil, err
	}

	requested := make(map[domain.Permission]struct{}, len(request.RequestedPermissions))
	for _, permission := range request.RequestedPermissions {
		if !permission.Valid() {
			return nil, ErrInvalidRequest
		}
		if permission == domain.PermissionInternalAll {
			return nil, ErrDelegationDenied
		}
		if _, ok := delegable[permission]; !ok {
			return nil, ErrDelegationDenied
		}
		if _, ok := creator[permission]; !ok && !creatorHasInternalAll {
			return nil, ErrDelegationDenied
		}
		requested[permission] = struct{}{}
	}

	result := make([]domain.Permission, 0, len(requested))
	for permission := range requested {
		result = append(result, permission)
	}
	slices.Sort(result)
	return result, nil
}

func permissionSet(
	permissions []domain.Permission,
	allowInternalAll bool,
) (map[domain.Permission]struct{}, bool, error) {
	result := make(map[domain.Permission]struct{}, len(permissions))
	hasInternalAll := false
	for _, permission := range permissions {
		if !permission.Valid() {
			return nil, false, ErrInvalidRequest
		}
		if permission == domain.PermissionInternalAll {
			if !allowInternalAll {
				return nil, false, ErrDelegationDenied
			}
			hasInternalAll = true
			continue
		}
		result[permission] = struct{}{}
	}
	return result, hasInternalAll, nil
}

func credentialPrefix(candidate string) string {
	parts := strings.Split(candidate, ".")
	if len(parts) != 3 || parts[0] != credentialMarker {
		return ""
	}
	return parts[1]
}
