package httpserver

import (
	"strings"
	"testing"
)

func TestDevelopmentLocalAdminSeedRequiresNonProductionMode(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		enabled     string
		want        bool
		errorText   string
	}{
		{name: "disabled by default", environment: "production"},
		{
			name: "production rejects seed", environment: "production", enabled: "true",
			errorText: "forbidden in production",
		},
		{name: "development permits seed", environment: "development", enabled: "true", want: true},
		{name: "test permits seed", environment: "test", enabled: "true", want: true},
		{name: "invalid flag fails closed", environment: "development", enabled: "yes", errorText: "true or false"},
		{name: "invalid environment fails closed", environment: "staging", errorText: "development, test, or production"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KUBEQUEUE_ENVIRONMENT", tt.environment)
			t.Setenv("KUBEQUEUE_DEV_SEED_LOCAL_ADMIN", tt.enabled)
			got, err := developmentLocalAdminSeedFromEnvironment()
			if tt.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errorText) {
					t.Fatalf("error = %v, want text %q", err, tt.errorText)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("enabled = %v, error = %v, want %v", got, err, tt.want)
			}
		})
	}
}
