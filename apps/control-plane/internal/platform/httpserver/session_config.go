package httpserver

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
)

type sessionStore interface {
	application.SessionRepository
}

func sessionsFromEnvironment(store sessionStore) (*application.Sessions, string, error) {
	digestEncoded := strings.TrimSpace(os.Getenv("KUBEQUEUE_SESSION_DIGEST_KEY"))
	encryptionEncoded := strings.TrimSpace(os.Getenv("KUBEQUEUE_SESSION_ENCRYPTION_KEY"))
	origin := strings.TrimSuffix(strings.TrimSpace(os.Getenv("KUBEQUEUE_BROWSER_ORIGIN")), "/")
	bffKey := strings.TrimSpace(os.Getenv("KUBEQUEUE_BFF_INTERNAL_KEY"))
	configured := digestEncoded != "" || encryptionEncoded != "" || origin != "" || bffKey != ""
	if !configured {
		return nil, "", nil
	}
	if digestEncoded == "" || encryptionEncoded == "" || origin == "" || len(bffKey) < 32 {
		return nil, "", errors.New(
			"KUBEQUEUE_SESSION_DIGEST_KEY, KUBEQUEUE_SESSION_ENCRYPTION_KEY, KUBEQUEUE_BROWSER_ORIGIN, and a 32-character KUBEQUEUE_BFF_INTERNAL_KEY must be configured together",
		)
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Scheme == "" || parsedOrigin.Host == "" ||
		parsedOrigin.User != nil || (parsedOrigin.Path != "" && parsedOrigin.Path != "/") ||
		parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
		return nil, "", errors.New("KUBEQUEUE_BROWSER_ORIGIN must be an absolute origin without credentials or a path")
	}
	if parsedOrigin.Scheme != "https" &&
		(parsedOrigin.Scheme != "http" || !sessionLoopbackHost(parsedOrigin.Hostname())) {
		return nil, "", errors.New("KUBEQUEUE_BROWSER_ORIGIN must use HTTPS except on loopback")
	}
	origin = parsedOrigin.Scheme + "://" + parsedOrigin.Host
	digestKey, err := base64.StdEncoding.DecodeString(digestEncoded)
	if err != nil || len(digestKey) < 32 {
		return nil, "", errors.New("KUBEQUEUE_SESSION_DIGEST_KEY must be base64-encoded and at least 256 bits")
	}
	encryptionKey, err := base64.StdEncoding.DecodeString(encryptionEncoded)
	if err != nil || len(encryptionKey) != 32 {
		return nil, "", errors.New("KUBEQUEUE_SESSION_ENCRYPTION_KEY must be a base64-encoded 256-bit key")
	}
	idle, err := durationEnvironment("KUBEQUEUE_SESSION_IDLE_LIFETIME", 30*time.Minute)
	if err != nil {
		return nil, "", err
	}
	absolute, err := durationEnvironment("KUBEQUEUE_SESSION_ABSOLUTE_LIFETIME", 12*time.Hour)
	if err != nil {
		return nil, "", err
	}
	lastUsed, err := durationEnvironment("KUBEQUEUE_SESSION_LAST_USED_INTERVAL", time.Minute)
	if err != nil {
		return nil, "", err
	}
	sessions, err := application.NewSessions(store, application.SessionConfig{
		DigestKey: digestKey, EncryptionKey: encryptionKey, IdleLifetime: idle,
		AbsoluteLifetime: absolute, LastUsedInterval: lastUsed,
	})
	if err != nil {
		return nil, "", fmt.Errorf("configure browser sessions: %w", err)
	}
	return sessions, origin, nil
}

func sessionEncryptionKeyFromEnvironment() ([]byte, error) {
	encoded := strings.TrimSpace(os.Getenv("KUBEQUEUE_SESSION_ENCRYPTION_KEY"))
	if encoded == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("KUBEQUEUE_SESSION_ENCRYPTION_KEY must be a base64-encoded 256-bit key")
	}
	return key, nil
}

func durationEnvironment(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return parsed, nil
}
