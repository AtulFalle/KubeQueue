package serviceaccountcredential

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

var (
	testNow = time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	testKey = bytes.Repeat([]byte{0x9d}, MinimumDigestKeyBytes)
)

func TestIssueGeneration(t *testing.T) {
	tests := []struct {
		name       string
		random     []byte
		wantErr    error
		wantPrefix string
	}{
		{
			name:       "injectable entropy produces opaque credential",
			random:     bytes.Repeat([]byte{0x2a}, prefixBytes+secretBytes),
			wantPrefix: "KioqKioqKioqKioq",
		},
		{
			name:    "entropy failure is reported without partial credential",
			random:  []byte{0x01},
			wantErr: io.ErrUnexpectedEOF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := mustLifecycle(t, bytes.NewReader(tt.random))
			issued, err := lifecycle.Issue(validRequest(), testNow)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Issue() error = %v, want %v", err, tt.wantErr)
				}
				if issued.Plaintext != nil {
					t.Fatal("Issue() returned plaintext after entropy failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("Issue() error = %v", err)
			}
			if issued.Stored.Prefix != tt.wantPrefix {
				t.Fatalf("stored prefix = %q, want %q", issued.Stored.Prefix, tt.wantPrefix)
			}
			plaintext, err := issued.Plaintext.Reveal()
			if err != nil {
				t.Fatalf("Reveal() error = %v", err)
			}
			if !strings.HasPrefix(plaintext, "kqsa."+tt.wantPrefix+".") {
				t.Fatal("plaintext does not have the safe credential marker and lookup prefix")
			}
			if _, err := issued.Plaintext.Reveal(); !errors.Is(err, ErrPlaintextUnavailable) {
				t.Fatalf("second Reveal() error = %v, want %v", err, ErrPlaintextUnavailable)
			}

			mac := hmac.New(sha256.New, testKey)
			_, _ = mac.Write([]byte(plaintext))
			if !hmac.Equal(issued.Stored.Digest.Bytes(), mac.Sum(nil)) {
				t.Fatal("stored digest does not match keyed HMAC-SHA-256")
			}
		})
	}
}

func TestDigestVerification(t *testing.T) {
	lifecycle := mustLifecycle(t, bytes.NewReader(append(
		bytes.Repeat([]byte{0x43}, prefixBytes+secretBytes),
		bytes.Repeat([]byte{0x44}, prefixBytes+secretBytes)...,
	)))
	issued, err := lifecycle.Issue(validRequest(), testNow)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	plaintext, err := issued.Plaintext.Reveal()
	if err != nil {
		t.Fatalf("Reveal() error = %v", err)
	}
	other, err := lifecycle.Issue(validRequest(), testNow)
	if err != nil {
		t.Fatalf("second Issue() error = %v", err)
	}
	otherPlaintext, err := other.Plaintext.Reveal()
	if err != nil {
		t.Fatalf("second Reveal() error = %v", err)
	}

	tests := []struct {
		name      string
		candidate string
		wantErr   error
	}{
		{name: "matching digest", candidate: plaintext},
		{name: "different secret", candidate: otherPlaintext, wantErr: ErrInvalidCredential},
		{name: "malformed credential", candidate: "not-a-credential", wantErr: ErrInvalidCredential},
		{name: "empty credential", candidate: "", wantErr: ErrInvalidCredential},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := lifecycle.Verify(issued.Stored, tt.candidate, testNow)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Verify() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestExpiryPolicyAndValidation(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		verifyAt  time.Time
		issueErr  error
		verifyErr error
	}{
		{
			name:      "expiry is required",
			expiresAt: time.Time{},
			issueErr:  ErrInvalidRequest,
		},
		{
			name:      "minimum lifetime is accepted",
			expiresAt: testNow.Add(MinimumLifetime),
			verifyAt:  testNow.Add(MinimumLifetime - time.Nanosecond),
		},
		{
			name:      "lifetime beyond policy is rejected",
			expiresAt: testNow.Add(DefaultPolicy().MaxLifetime + time.Nanosecond),
			issueErr:  ErrInvalidRequest,
		},
		{
			name:      "credential expires at exact boundary",
			expiresAt: testNow.Add(time.Hour),
			verifyAt:  testNow.Add(time.Hour),
			verifyErr: ErrCredentialExpired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := mustLifecycle(t, bytes.NewReader(bytes.Repeat(
				[]byte{0x31},
				prefixBytes+secretBytes,
			)))
			request := validRequest()
			request.ExpiresAt = tt.expiresAt
			issued, err := lifecycle.Issue(request, testNow)
			if !errors.Is(err, tt.issueErr) {
				t.Fatalf("Issue() error = %v, want %v", err, tt.issueErr)
			}
			if tt.issueErr != nil {
				return
			}
			plaintext, err := issued.Plaintext.Reveal()
			if err != nil {
				t.Fatalf("Reveal() error = %v", err)
			}
			if err := lifecycle.Verify(issued.Stored, plaintext, tt.verifyAt); !errors.Is(err, tt.verifyErr) {
				t.Fatalf("Verify() error = %v, want %v", err, tt.verifyErr)
			}
		})
	}
}

func TestRotationOverlap(t *testing.T) {
	tests := []struct {
		name      string
		overlap   time.Duration
		verifyAt  time.Time
		wantErr   error
		rotateErr error
	}{
		{
			name:     "previous credential works during overlap",
			overlap:  2 * time.Minute,
			verifyAt: testNow.Add(2*time.Minute - time.Nanosecond),
		},
		{
			name:     "previous credential stops at overlap boundary",
			overlap:  2 * time.Minute,
			verifyAt: testNow.Add(2 * time.Minute),
			wantErr:  ErrCredentialRotated,
		},
		{
			name:      "overlap must be positive",
			overlap:   0,
			rotateErr: ErrInvalidRequest,
		},
		{
			name:      "overlap cannot exceed policy",
			overlap:   DefaultPolicy().MaxRotationOverlap + time.Nanosecond,
			rotateErr: ErrInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := mustLifecycle(t, bytes.NewReader(bytes.Repeat(
				[]byte{0x52},
				2*(prefixBytes+secretBytes),
			)))
			issued, err := lifecycle.Issue(validRequest(), testNow.Add(-time.Minute))
			if err != nil {
				t.Fatalf("Issue() error = %v", err)
			}
			plaintext, err := issued.Plaintext.Reveal()
			if err != nil {
				t.Fatalf("Reveal() error = %v", err)
			}
			rotationRequest := validRequest()
			rotationRequest.ExpiresAt = testNow.Add(24 * time.Hour)
			rotation, err := lifecycle.Rotate(issued.Stored, rotationRequest, tt.overlap, testNow)
			if !errors.Is(err, tt.rotateErr) {
				t.Fatalf("Rotate() error = %v, want %v", err, tt.rotateErr)
			}
			if tt.rotateErr != nil {
				return
			}
			if err := lifecycle.Verify(rotation.Previous, plaintext, tt.verifyAt); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Verify(previous) error = %v, want %v", err, tt.wantErr)
			}
			replacement, err := rotation.Replacement.Plaintext.Reveal()
			if err != nil {
				t.Fatalf("replacement Reveal() error = %v", err)
			}
			if err := lifecycle.Verify(rotation.Replacement.Stored, replacement, tt.verifyAt); err != nil {
				t.Fatalf("Verify(replacement) error = %v", err)
			}
		})
	}
}

func TestRevocationIsImmediate(t *testing.T) {
	lifecycle := mustLifecycle(t, bytes.NewReader(bytes.Repeat(
		[]byte{0x61},
		prefixBytes+secretBytes,
	)))
	issued, err := lifecycle.Issue(validRequest(), testNow)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	plaintext, err := issued.Plaintext.Reveal()
	if err != nil {
		t.Fatalf("Reveal() error = %v", err)
	}

	revoked := Revoke(issued.Stored, testNow.Add(time.Second))
	if err := lifecycle.Verify(revoked, plaintext, testNow.Add(time.Second)); !errors.Is(err, ErrCredentialRevoked) {
		t.Fatalf("Verify() error = %v, want %v", err, ErrCredentialRevoked)
	}
	again := Revoke(revoked, testNow.Add(time.Hour))
	if !again.RevokedAt.Equal(*revoked.RevokedAt) {
		t.Fatal("idempotent revocation changed the original revocation time")
	}
}

func TestDelegationCeiling(t *testing.T) {
	tests := []struct {
		name      string
		requested []domain.Permission
		creator   []domain.Permission
		delegable []domain.Permission
		wantErr   error
	}{
		{
			name:      "permission inside both ceilings",
			requested: []domain.Permission{domain.PermissionJobsRead},
			creator:   []domain.Permission{domain.PermissionJobsRead},
			delegable: []domain.Permission{domain.PermissionJobsRead},
		},
		{
			name:      "creator cannot delegate authority not held",
			requested: []domain.Permission{domain.PermissionJobsSubmit},
			creator:   []domain.Permission{domain.PermissionJobsRead},
			delegable: []domain.Permission{domain.PermissionJobsSubmit},
			wantErr:   ErrDelegationDenied,
		},
		{
			name:      "non-delegable authority is denied",
			requested: []domain.Permission{domain.PermissionTokensManage},
			creator:   []domain.Permission{domain.PermissionTokensManage},
			delegable: []domain.Permission{domain.PermissionJobsRead},
			wantErr:   ErrDelegationDenied,
		},
		{
			name:      "internal wildcard is never delegated",
			requested: []domain.Permission{domain.PermissionInternalAll},
			creator:   []domain.Permission{domain.PermissionInternalAll},
			delegable: []domain.Permission{domain.PermissionInternalAll},
			wantErr:   ErrDelegationDenied,
		},
		{
			name:      "owner wildcard still requires explicit delegable permission",
			requested: []domain.Permission{domain.PermissionJobsSubmit},
			creator:   []domain.Permission{domain.PermissionInternalAll},
			delegable: []domain.Permission{domain.PermissionJobsSubmit},
		},
		{
			name:      "unknown catalog permission is rejected",
			requested: []domain.Permission{"jobs.unknown"},
			creator:   []domain.Permission{"jobs.unknown"},
			delegable: []domain.Permission{"jobs.unknown"},
			wantErr:   ErrInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := mustLifecycle(t, bytes.NewReader(bytes.Repeat(
				[]byte{0x77},
				prefixBytes+secretBytes,
			)))
			request := validRequest()
			request.RequestedPermissions = tt.requested
			request.CreatorPermissions = tt.creator
			request.DelegablePermissions = tt.delegable
			_, err := lifecycle.Issue(request, testNow)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Issue() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestPlaintextNeverEntersStorageOrFormatting(t *testing.T) {
	lifecycle := mustLifecycle(t, bytes.NewReader(bytes.Repeat(
		[]byte{0x8b},
		prefixBytes+secretBytes,
	)))
	issued, err := lifecycle.Issue(validRequest(), testNow)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	plaintext, err := issued.Plaintext.Reveal()
	if err != nil {
		t.Fatalf("Reveal() error = %v", err)
	}

	tests := []struct {
		name      string
		formatted string
	}{
		{name: "stored value", formatted: fmt.Sprintf("%v", issued.Stored)},
		{name: "stored Go value", formatted: fmt.Sprintf("%#v", issued.Stored)},
		{name: "digest", formatted: fmt.Sprintf("%v", issued.Stored.Digest)},
		{name: "one-time wrapper", formatted: fmt.Sprintf("%v", issued.Plaintext)},
		{name: "issue result", formatted: fmt.Sprintf("%#v", issued)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(tt.formatted, plaintext) {
				t.Fatal("formatted value disclosed plaintext credential")
			}
		})
	}
}

func TestLastUsedWriteThrottling(t *testing.T) {
	lifecycle := mustLifecycle(t, bytes.NewReader(nil))
	interval := DefaultPolicy().LastUsedWriteInterval
	tests := []struct {
		name       string
		lastUsedAt time.Time
		now        time.Time
		want       bool
	}{
		{name: "first use is due", now: testNow, want: true},
		{
			name:       "use inside interval is throttled",
			lastUsedAt: testNow,
			now:        testNow.Add(interval - time.Nanosecond),
		},
		{
			name:       "use at interval is due",
			lastUsedAt: testNow,
			now:        testNow.Add(interval),
			want:       true,
		},
		{
			name:       "backward clock is throttled",
			lastUsedAt: testNow,
			now:        testNow.Add(-time.Second),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lifecycle.ShouldRecordLastUsed(tt.lastUsedAt, tt.now); got != tt.want {
				t.Fatalf("ShouldRecordLastUsed() = %t, want %t", got, tt.want)
			}
		})
	}
}

func validRequest() IssueRequest {
	return IssueRequest{
		CreatedBy:            domain.PrincipalID("creator"),
		RequestedPermissions: []domain.Permission{domain.PermissionJobsRead},
		CreatorPermissions:   []domain.Permission{domain.PermissionJobsRead},
		DelegablePermissions: []domain.Permission{domain.PermissionJobsRead},
		ExpiresAt:            testNow.Add(24 * time.Hour),
	}
}

func mustLifecycle(t *testing.T, random *bytes.Reader) *Lifecycle {
	t.Helper()
	lifecycle, err := NewLifecycleWithRandom(testKey, random, DefaultPolicy())
	if err != nil {
		t.Fatalf("NewLifecycle() error = %v", err)
	}
	return lifecycle
}
