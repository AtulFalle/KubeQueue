package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/breakglass"
)

type breakGlassSecret struct {
	DigestKey        string                    `json:"digestKey"`
	Current          breakGlassCredentialJSON  `json:"current"`
	Previous         *breakGlassCredentialJSON `json:"previous,omitempty"`
	OverlapExpiresAt string                    `json:"overlapExpiresAt,omitempty"`
}

type breakGlassCredentialJSON struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

func breakGlassFromEnvironment(
	ctx context.Context,
	store interface {
		application.BreakGlassRepository
		ConfigureBreakGlass(context.Context, []breakglass.Credential, time.Time) error
	},
) (*application.BreakGlass, error) {
	inline := strings.TrimSpace(os.Getenv("KUBEQUEUE_BREAK_GLASS_CONFIG"))
	path := strings.TrimSpace(os.Getenv("KUBEQUEUE_BREAK_GLASS_CONFIG_FILE"))
	if inline != "" && path != "" {
		return nil, errors.New("configure only one of KUBEQUEUE_BREAK_GLASS_CONFIG or KUBEQUEUE_BREAK_GLASS_CONFIG_FILE")
	}
	now := time.Now().UTC()
	if inline == "" && path == "" {
		if err := store.ConfigureBreakGlass(ctx, nil, now); err != nil {
			return nil, fmt.Errorf("disable break-glass credentials: %w", err)
		}
		return nil, nil
	}
	raw := []byte(inline)
	if path != "" {
		var err error
		raw, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read break-glass configuration file: %w", err)
		}
	}
	var secret breakGlassSecret
	if err := json.Unmarshal(raw, &secret); err != nil {
		return nil, errors.New("break-glass configuration must be valid JSON")
	}
	clear(raw)
	key, err := base64.StdEncoding.DecodeString(secret.DigestKey)
	if err != nil || len(key) < breakglass.MinimumDigestKey {
		return nil, errors.New("break-glass digestKey must be base64-encoded and at least 256 bits")
	}
	credentials, err := configuredBreakGlassCredentials(secret, key, now)
	if err != nil {
		clear(key)
		return nil, err
	}
	if err := store.ConfigureBreakGlass(ctx, credentials, now); err != nil {
		clear(key)
		return nil, fmt.Errorf("synchronize break-glass configuration: %w", err)
	}
	service, err := application.NewBreakGlass(store, key)
	clear(key)
	return service, err
}

func configuredBreakGlassCredentials(
	secret breakGlassSecret,
	key []byte,
	now time.Time,
) ([]breakglass.Credential, error) {
	current, err := configuredBreakGlassCredential("current", secret.Current, nil, key, now)
	if err != nil {
		return nil, err
	}
	result := []breakglass.Credential{current}
	if secret.Previous == nil {
		if secret.OverlapExpiresAt != "" {
			return nil, errors.New("break-glass overlap requires one previous credential")
		}
		return result, nil
	}
	overlap, err := time.Parse(time.RFC3339, secret.OverlapExpiresAt)
	if err != nil || !now.Before(overlap) || overlap.After(now.Add(breakglass.MaximumOverlap)) {
		return nil, errors.New("break-glass overlapExpiresAt must be within the next 10 minutes")
	}
	previous, err := configuredBreakGlassCredential("previous", *secret.Previous, &overlap, key, now)
	if err != nil {
		return nil, err
	}
	if previous.Prefix == current.Prefix || overlap.After(previous.ExpiresAt) {
		return nil, errors.New("break-glass previous credential and overlap are invalid")
	}
	return append(result, previous), nil
}

func configuredBreakGlassCredential(
	slot string,
	config breakGlassCredentialJSON,
	overlap *time.Time,
	key []byte,
	now time.Time,
) (breakglass.Credential, error) {
	prefix, err := breakglass.Parse(config.Token)
	if err != nil {
		return breakglass.Credential{}, errors.New("break-glass token must have the exact kqbg.<16>.<43> shape")
	}
	expiresAt, err := time.Parse(time.RFC3339, config.ExpiresAt)
	if err != nil || !now.Before(expiresAt) || expiresAt.After(now.Add(breakglass.MaximumLifetime)) {
		return breakglass.Credential{}, errors.New("break-glass expiry must be within the next 24 hours")
	}
	digest, err := breakglass.KeyedDigest(key, config.Token)
	if err != nil {
		return breakglass.Credential{}, err
	}
	return breakglass.Credential{
		Slot: slot, Prefix: prefix, Digest: digest,
		ExpiresAt: expiresAt.UTC(), OverlapExpires: overlap,
	}, nil
}
