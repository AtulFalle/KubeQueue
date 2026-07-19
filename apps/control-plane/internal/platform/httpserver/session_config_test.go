package httpserver

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
)

func TestSessionConfigurationFailsClosedWhenPartial(t *testing.T) {
	t.Setenv(
		"KUBEQUEUE_SESSION_DIGEST_KEY",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	t.Setenv("KUBEQUEUE_SESSION_ENCRYPTION_KEY", "")
	t.Setenv("KUBEQUEUE_BROWSER_ORIGIN", "")
	t.Setenv("KUBEQUEUE_BFF_INTERNAL_KEY", "")
	_, _, err := sessionsFromEnvironment(nil)
	if err == nil || !strings.Contains(err.Error(), "must be configured together") {
		t.Fatalf("partial session configuration error = %v", err)
	}
}

func TestSessionConfigurationAllowsLocalAuthWithoutRefreshProvider(t *testing.T) {
	t.Setenv(
		"KUBEQUEUE_SESSION_DIGEST_KEY",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	t.Setenv(
		"KUBEQUEUE_SESSION_ENCRYPTION_KEY",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	t.Setenv("KUBEQUEUE_BROWSER_ORIGIN", "https://queue.example.com")
	t.Setenv("KUBEQUEUE_BFF_INTERNAL_KEY", strings.Repeat("a", 32))

	store, err := persistence.Open(
		t.Context(), "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "sessions.db")),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions, _, err := sessionsFromEnvironment(store)
	if err != nil || sessions == nil {
		t.Fatalf("local-only session configuration = %v, error = %v", sessions, err)
	}
}

func TestSessionConfigurationRejectsNonLoopbackHTTPOrigin(t *testing.T) {
	t.Setenv(
		"KUBEQUEUE_SESSION_DIGEST_KEY",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	t.Setenv(
		"KUBEQUEUE_SESSION_ENCRYPTION_KEY",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	t.Setenv("KUBEQUEUE_BROWSER_ORIGIN", "http://queue.example.com")
	t.Setenv("KUBEQUEUE_BFF_INTERNAL_KEY", strings.Repeat("a", 32))

	_, _, err := sessionsFromEnvironment(nil)
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS except on loopback") {
		t.Fatalf("insecure browser origin error = %v", err)
	}
}
