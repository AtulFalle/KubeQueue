package httpserver

import (
	"errors"
	"os"
	"strings"
)

func developmentLocalAdminSeedFromEnvironment() (bool, error) {
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("KUBEQUEUE_ENVIRONMENT")))
	if environment == "" {
		environment = "production"
	}
	switch environment {
	case "development", "test", "production":
	default:
		return false, errors.New(
			"KUBEQUEUE_ENVIRONMENT must be development, test, or production",
		)
	}

	configured := strings.ToLower(strings.TrimSpace(os.Getenv("KUBEQUEUE_DEV_SEED_LOCAL_ADMIN")))
	if configured == "" || configured == "false" {
		return false, nil
	}
	if configured != "true" {
		return false, errors.New("KUBEQUEUE_DEV_SEED_LOCAL_ADMIN must be true or false")
	}
	if environment == "production" {
		return false, errors.New(
			"KUBEQUEUE_DEV_SEED_LOCAL_ADMIN is forbidden in production",
		)
	}
	return true, nil
}
