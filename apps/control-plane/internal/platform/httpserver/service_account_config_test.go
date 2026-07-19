package httpserver

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
)

func TestServiceAccountConfigurationIsOptionalAndFailsClosed(t *testing.T) {
	t.Run("absent key disables native authentication", func(t *testing.T) {
		t.Setenv("KUBEQUEUE_SERVICE_ACCOUNT_DIGEST_KEY", "")
		service, err := serviceAccountsFromEnvironment(nil)
		if err != nil || service != nil {
			t.Fatalf("absent service-account configuration = %#v, %v", service, err)
		}
	})
	for _, encoded := range []string{
		"not-base64",
		base64.StdEncoding.EncodeToString(make([]byte, 31)),
	} {
		t.Run("invalid key fails closed", func(t *testing.T) {
			t.Setenv("KUBEQUEUE_SERVICE_ACCOUNT_DIGEST_KEY", encoded)
			service, err := serviceAccountsFromEnvironment(nil)
			if service != nil || err == nil ||
				!strings.Contains(err.Error(), "at least 256 bits") {
				t.Fatalf("invalid service-account configuration = %#v, %v", service, err)
			}
		})
	}
}

func TestServiceAccountConfigurationComposesApplicationService(t *testing.T) {
	store, err := persistence.Open(
		t.Context(), "file:test-service-account-composition?mode=memory&cache=shared",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv(
		"KUBEQUEUE_SERVICE_ACCOUNT_DIGEST_KEY",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	service, err := serviceAccountsFromEnvironment(store)
	if err != nil {
		t.Fatalf("serviceAccountsFromEnvironment() error = %v", err)
	}
	if service == nil {
		t.Fatal("configured service-account application was not composed")
	}
}
