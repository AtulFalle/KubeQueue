package persistence

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/serviceaccountcredential"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func TestNativeServiceAccountCredentialPersistenceLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, err := Open(ctx, "file:test-native-service-accounts?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	formattedNow := now.Format(time.RFC3339Nano)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{
			query: `INSERT INTO installations(id,name,created_at) VALUES(?,?,?)`,
			args:  []any{"default", "Default", formattedNow},
		},
		{
			query: `INSERT INTO projects(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
			args:  []any{"platform", "default", "Platform", formattedNow},
		},
		{
			query: `INSERT INTO principals(id,installation_id,kind,display_name,created_at)
			 VALUES(?,?,?,?,?)`,
			args: []any{"creator", "default", "HUMAN", "Creator", formattedNow},
		},
	} {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	account := serviceaccountcredential.ServiceAccount{
		PrincipalID: "build_bot", InstallationID: "default", ProjectID: "platform",
		DisplayName: "Build Bot", CreatedBy: "creator", CreatedAt: now,
	}
	created, err := store.CreateServiceAccount(ctx, account)
	if err != nil {
		t.Fatalf("CreateServiceAccount() error = %v", err)
	}
	retried := account
	retried.CreatedAt = now.Add(time.Minute)
	again, err := store.CreateServiceAccount(ctx, retried)
	if err != nil {
		t.Fatalf("idempotent CreateServiceAccount() error = %v", err)
	}
	if !again.CreatedAt.Equal(created.CreatedAt) {
		t.Fatal("idempotent service-account creation changed creation time")
	}

	entropy := append(
		bytes.Repeat([]byte{0x21}, 44),
		bytes.Repeat([]byte{0x22}, 44)...,
	)
	lifecycle, err := serviceaccountcredential.NewLifecycleWithRandom(
		bytes.Repeat([]byte{0x33}, serviceaccountcredential.MinimumDigestKeyBytes),
		bytes.NewReader(entropy),
		serviceaccountcredential.DefaultPolicy(),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := serviceaccountcredential.IssueRequest{
		CreatedBy:            "creator",
		RequestedPermissions: []domain.Permission{domain.PermissionJobsRead},
		CreatorPermissions:   []domain.Permission{domain.PermissionJobsRead},
		DelegablePermissions: []domain.Permission{domain.PermissionJobsRead},
		ExpiresAt:            now.Add(time.Hour),
	}
	issued, err := lifecycle.Issue(request, now)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := issued.Plaintext.Reveal()
	if err != nil {
		t.Fatal(err)
	}
	credential := serviceaccountcredential.Credential{
		ID: "credential-one", ServiceAccountPrincipalID: account.PrincipalID,
		Stored: issued.Stored,
	}
	if err := store.CreateNativeCredential(ctx, credential); err != nil {
		t.Fatalf("CreateNativeCredential() error = %v", err)
	}
	var digest string
	if err := store.db.QueryRowContext(ctx,
		`SELECT keyed_digest FROM native_credential_metadata WHERE id=?`,
		credential.ID,
	).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest == plaintext || bytes.Contains([]byte(digest), []byte(plaintext)) {
		t.Fatal("native credential plaintext was persisted")
	}
	stored, storedAccount, err := store.NativeCredentialByPrefix(ctx, credential.Stored.Prefix)
	if err != nil {
		t.Fatalf("NativeCredentialByPrefix() error = %v", err)
	}
	if storedAccount.PrincipalID != account.PrincipalID ||
		len(stored.Stored.Permissions) != 1 ||
		stored.Stored.Permissions[0] != domain.PermissionJobsRead {
		t.Fatalf("stored credential/account = %#v / %#v", stored, storedAccount)
	}
	if err := lifecycle.Verify(stored.Stored, plaintext, now); err != nil {
		t.Fatalf("Verify() persisted credential error = %v", err)
	}
	usedAt := now.Add(time.Minute)
	if err := store.TouchNativeCredential(ctx, credential.ID, usedAt); err != nil {
		t.Fatalf("TouchNativeCredential() error = %v", err)
	}
	stored, _, err = store.NativeCredentialByID(ctx, credential.ID)
	if err != nil || !stored.Stored.LastUsedAt.Equal(usedAt) {
		t.Fatalf("last-used credential = %#v, %v", stored, err)
	}

	rotationRequest := request
	rotationRequest.ExpiresAt = now.Add(2 * time.Hour)
	rotation, err := lifecycle.Rotate(stored.Stored, rotationRequest, 2*time.Minute, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	previous := stored
	previous.Stored = rotation.Previous
	replacement := serviceaccountcredential.Credential{
		ID: "credential-two", ServiceAccountPrincipalID: account.PrincipalID,
		Stored: rotation.Replacement.Stored,
	}
	if err := store.RotateNativeCredential(ctx, previous, replacement); err != nil {
		t.Fatalf("RotateNativeCredential() error = %v", err)
	}
	previous, _, err = store.NativeCredentialByID(ctx, previous.ID)
	if err != nil || previous.Stored.RotatedAt == nil ||
		previous.Stored.OverlapExpiresAt == nil {
		t.Fatalf("persisted rotation = %#v, %v", previous, err)
	}
	if err := lifecycle.Verify(
		previous.Stored, plaintext, *previous.Stored.OverlapExpiresAt,
	); !errors.Is(err, serviceaccountcredential.ErrCredentialRotated) {
		t.Fatalf("rotation overlap boundary error = %v", err)
	}
	revokedAt := usedAt.Add(time.Second)
	if err := store.RevokeNativeCredential(ctx, replacement.ID, revokedAt); err != nil {
		t.Fatalf("RevokeNativeCredential() error = %v", err)
	}
	if err := store.RevokeNativeCredential(ctx, replacement.ID, revokedAt.Add(time.Hour)); err != nil {
		t.Fatalf("idempotent RevokeNativeCredential() error = %v", err)
	}
	replacement, _, err = store.NativeCredentialByID(ctx, replacement.ID)
	if err != nil || replacement.Stored.RevokedAt == nil ||
		!replacement.Stored.RevokedAt.Equal(revokedAt) {
		t.Fatalf("persisted revocation = %#v, %v", replacement, err)
	}
	accounts, err := store.ListServiceAccounts(
		ctx, "default", domain.AccessPage{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].PrincipalID != account.PrincipalID {
		t.Fatalf("service-account page = %#v", accounts)
	}
	firstCredentials, err := store.ListNativeCredentialMetadata(
		ctx, "default", account.PrincipalID, domain.AccessPage{Limit: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondCredentials, err := store.ListNativeCredentialMetadata(
		ctx, "default", account.PrincipalID,
		domain.AccessPage{Limit: 1, After: firstCredentials[0].ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstCredentials) != 1 || firstCredentials[0].ID != "credential-one" ||
		len(secondCredentials) != 1 || secondCredentials[0].ID != "credential-two" ||
		firstCredentials[0].Prefix == "" {
		t.Fatalf("credential metadata pages = %#v then %#v",
			firstCredentials, secondCredentials)
	}

	if _, err := store.db.ExecContext(
		ctx, `UPDATE principals SET disabled_at=? WHERE id=?`, formattedNow, account.PrincipalID,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.NativeCredentialByID(
		ctx, replacement.ID,
	); !errors.Is(err, ports.ErrCredentialNotFound) {
		t.Fatalf("disabled service-account credential lookup error = %v", err)
	}
}
