package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

const maxSessionTokenResponseBytes = 64 << 10

type sessionTokenResponse struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	TokenType    string      `json:"token_type"`
	ExpiresIn    json.Number `json:"expires_in"`
	Error        string      `json:"error"`
}

func readSessionTokenResponse(body io.Reader) (sessionTokenResponse, error) {
	var payload sessionTokenResponse
	limited := io.LimitReader(body, maxSessionTokenResponseBytes+1)
	encoded, err := io.ReadAll(limited)
	if err != nil || len(encoded) > maxSessionTokenResponseBytes {
		return payload, errors.New("OIDC token response is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return payload, fmt.Errorf("decode OIDC token response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return payload, errors.New("OIDC token response contains trailing data")
	}
	return payload, nil
}

func sessionLoopbackHost(host string) bool {
	address := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (address != nil && address.IsLoopback())
}
