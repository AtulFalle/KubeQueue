package sensitivedata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestInspectManifestJSONRedactsStructuralCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          string
		wantJSON       string
		wantFindings   []Finding
		forbidden      []string
		preserved      []string
		wantNoFindings bool
	}{
		{
			name: "secret payload and sensitive annotation",
			input: `{
				"kind":"Secret",
				"metadata":{"annotations":{
					"example.com/password":"annotation-secret",
					"example.com/description":"public"
				}},
				"data":{"username":"dXNlcg==","password":"c2VjcmV0"},
				"stringData":{"token":"plain-secret"}
			}`,
			wantJSON: `{
				"data":{"password":"[REDACTED]","username":"[REDACTED]"},
				"kind":"Secret",
				"metadata":{"annotations":{
					"example.com/description":"public",
					"example.com/password":"[REDACTED]"
				}},
				"stringData":{"token":"[REDACTED]"}
			}`,
			wantFindings: []Finding{
				{Reason: ReasonSecretData, Path: "/data/password"},
				{Reason: ReasonSecretData, Path: "/data/username"},
				{Reason: ReasonAnnotation, Path: "/metadata/annotations/example.com~1password"},
				{Reason: ReasonSecretStringData, Path: "/stringData/token"},
			},
			forbidden: []string{"annotation-secret", "dXNlcg==", "c2VjcmV0", "plain-secret"},
			preserved: []string{"public"},
		},
		{
			name: "literal environment credential with references preserved",
			input: `{
				"kind":"Pod",
				"spec":{"containers":[{
					"name":"worker",
					"env":[
						{"name":"DB_PASSWORD","value":"inline-password"},
						{"name":"API_TOKEN","value":"inline-token","valueFrom":{"secretKeyRef":{"name":"api","key":"token"}}},
						{"name":"CONFIG","valueFrom":{"configMapKeyRef":{"name":"settings","key":"mode"}}}
					]
				}]}
			}`,
			wantJSON: `{
				"kind":"Pod",
				"spec":{"containers":[{
					"env":[
						{"name":"DB_PASSWORD","value":"[REDACTED]"},
						{"name":"API_TOKEN","value":"[REDACTED]","valueFrom":{"secretKeyRef":{"key":"token","name":"api"}}},
						{"name":"CONFIG","valueFrom":{"configMapKeyRef":{"key":"mode","name":"settings"}}}
					],
					"name":"worker"
				}]}
			}`,
			wantFindings: []Finding{
				{Reason: ReasonEnvLiteral, Path: "/spec/containers/0/env/0/value"},
				{Reason: ReasonEnvLiteral, Path: "/spec/containers/0/env/1/value"},
			},
			forbidden: []string{"inline-password", "inline-token"},
			preserved: []string{`"secretKeyRef"`, `"configMapKeyRef"`, `"key":"token"`, `"name":"api"`},
		},
		{
			name: "credential-bearing arguments and known fields",
			input: `{
				"token":"object-token",
				"description":"Bearer is an authentication scheme",
				"spec":{"containers":[{
					"command":["runner","--password=command-password","--client-secret","next-secret"],
					"args":[
						"Authorization: Bearer header-secret",
						"DATABASE_URL=postgres://user:assignment-secret@db/app",
						"postgres://user:url-secret@db/app",
						"--token-expiry=30"
					]
				}]}
			}`,
			wantJSON: `{
				"description":"Bearer is an authentication scheme",
				"spec":{"containers":[{
					"args":[
						"Authorization: [REDACTED]",
						"DATABASE_URL=[REDACTED]",
						"[REDACTED]",
						"--token-expiry=30"
					],
					"command":["runner","--password=[REDACTED]","--client-secret","[REDACTED]"]
				}]},
				"token":"[REDACTED]"
			}`,
			wantFindings: []Finding{
				{Reason: ReasonCredentialArg, Path: "/spec/containers/0/args/0"},
				{Reason: ReasonCredentialArg, Path: "/spec/containers/0/args/1"},
				{Reason: ReasonCredentialArg, Path: "/spec/containers/0/args/2"},
				{Reason: ReasonCredentialArg, Path: "/spec/containers/0/command/1"},
				{Reason: ReasonCredentialArg, Path: "/spec/containers/0/command/3"},
				{Reason: ReasonSensitiveField, Path: "/token"},
			},
			forbidden: []string{
				"object-token",
				"command-password",
				"next-secret",
				"header-secret",
				"assignment-secret",
				"url-secret",
			},
			preserved: []string{"Bearer is an authentication scheme", "--token-expiry=30", "--client-secret"},
		},
		{
			name: "false-positive resistance",
			input: `{
				"passwordPolicy":"strict",
				"tokenExpiration":30,
				"authorizationMode":"RBAC",
				"spec":{"containers":[{
					"args":["--token-expiry=30","tokenizer","--password-file=/var/run/password"],
					"env":[
						{"name":"TOKEN_EXPIRY","value":"15m"},
						{"name":"PASSWORD_FILE","value":"/var/run/password"},
						{"name":"SECRET_NAME","value":"referenced-secret"},
						{"name":"NORMAL","value":"visible"}
					]
				}]}
			}`,
			wantJSON: `{
				"authorizationMode":"RBAC",
				"passwordPolicy":"strict",
				"spec":{"containers":[{
					"args":["--token-expiry=30","tokenizer","--password-file=/var/run/password"],
					"env":[
						{"name":"TOKEN_EXPIRY","value":"15m"},
						{"name":"PASSWORD_FILE","value":"/var/run/password"},
						{"name":"SECRET_NAME","value":"referenced-secret"},
						{"name":"NORMAL","value":"visible"}
					]
				}]},
				"tokenExpiration":30
			}`,
			preserved:      []string{"strict", "RBAC", "15m", "/var/run/password", "referenced-secret", "visible"},
			wantNoFindings: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			first, err := InspectManifestJSON([]byte(test.input), Limits{})
			if err != nil {
				t.Fatalf("InspectManifestJSON() error = %v", err)
			}
			second, err := InspectManifestJSON([]byte(test.input), Limits{})
			if err != nil {
				t.Fatalf("second InspectManifestJSON() error = %v", err)
			}

			assertJSONEqual(t, first.Redacted, []byte(test.wantJSON))
			if !bytes.Equal(first.Redacted, second.Redacted) {
				t.Fatalf("redacted output is not deterministic:\nfirst:  %s\nsecond: %s", first.Redacted, second.Redacted)
			}
			if !reflect.DeepEqual(first.Findings, second.Findings) {
				t.Fatalf("findings are not deterministic:\nfirst:  %#v\nsecond: %#v", first.Findings, second.Findings)
			}
			if test.wantNoFindings {
				if len(first.Findings) != 0 || first.FindingsTruncated {
					t.Fatalf("findings = %#v, truncated = %t; want none", first.Findings, first.FindingsTruncated)
				}
			} else if !reflect.DeepEqual(first.Findings, test.wantFindings) {
				t.Fatalf("findings = %#v, want %#v", first.Findings, test.wantFindings)
			}

			output := string(first.Redacted)
			for _, forbidden := range test.forbidden {
				if strings.Contains(output, forbidden) {
					t.Fatalf("redacted output leaked %q: %s", forbidden, output)
				}
				for _, finding := range first.Findings {
					if strings.Contains(finding.Path, forbidden) ||
						strings.Contains(string(finding.Reason), forbidden) {
						t.Fatalf("finding leaked %q: %#v", forbidden, finding)
					}
				}
			}
			for _, preserved := range test.preserved {
				if !strings.Contains(output, preserved) {
					t.Fatalf("redacted output did not preserve %q: %s", preserved, output)
				}
			}
		})
	}
}

func TestInspectManifestJSONRejectsMalformedInputWithoutEchoingIt(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"{",
		`{"password":"do-not-echo"`,
		`{} {"token":"do-not-echo"}`,
		`["value",]`,
	}
	for _, input := range tests {
		input := input
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			t.Parallel()

			result, err := InspectManifestJSON([]byte(input), Limits{})
			if err == nil {
				t.Fatal("InspectManifestJSON() error = nil")
			}
			if len(result.Redacted) != 0 || len(result.Findings) != 0 {
				t.Fatalf("result = %#v, want zero result", result)
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != ErrorMalformedJSON {
				t.Fatalf("error = %#v, want Error with code %q", err, ErrorMalformedJSON)
			}
			if strings.Contains(err.Error(), "do-not-echo") {
				t.Fatalf("error leaked input: %v", err)
			}
		})
	}
}

func TestInspectManifestJSONAppliesConfiguredLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		limits     Limits
		wantCode   ErrorCode
		wantLimit  int
		wantActual int
	}{
		{
			name:       "input too large",
			input:      `{"value":"visible"}`,
			limits:     Limits{MaxInputBytes: 5},
			wantCode:   ErrorInputTooLarge,
			wantLimit:  5,
			wantActual: len(`{"value":"visible"}`),
		},
		{
			name:     "negative input limit",
			input:    `{}`,
			limits:   Limits{MaxInputBytes: -1},
			wantCode: ErrorInvalidLimits,
		},
		{
			name:     "negative finding limit",
			input:    `{}`,
			limits:   Limits{MaxFindings: -1},
			wantCode: ErrorInvalidLimits,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := InspectManifestJSON([]byte(test.input), test.limits)
			var typed *Error
			if !errors.As(err, &typed) {
				t.Fatalf("error = %#v, want *Error", err)
			}
			if typed.Code != test.wantCode || typed.Limit != test.wantLimit || typed.Actual != test.wantActual {
				t.Fatalf(
					"error = %#v, want code %q, limit %d, actual %d",
					typed,
					test.wantCode,
					test.wantLimit,
					test.wantActual,
				)
			}
		})
	}
}

func TestInspectManifestJSONBoundsFindingsWithoutSkippingRedaction(t *testing.T) {
	t.Parallel()

	input := []byte(`{
		"password":"first-secret",
		"token":"second-secret",
		"authorization":"third-secret"
	}`)
	result, err := InspectManifestJSON(input, Limits{MaxFindings: 2})
	if err != nil {
		t.Fatalf("InspectManifestJSON() error = %v", err)
	}

	wantFindings := []Finding{
		{Reason: ReasonSensitiveField, Path: "/authorization"},
		{Reason: ReasonSensitiveField, Path: "/password"},
	}
	if !reflect.DeepEqual(result.Findings, wantFindings) {
		t.Fatalf("findings = %#v, want %#v", result.Findings, wantFindings)
	}
	if !result.FindingsTruncated {
		t.Fatal("FindingsTruncated = false, want true")
	}
	for _, secret := range []string{"first-secret", "second-secret", "third-secret"} {
		if bytes.Contains(result.Redacted, []byte(secret)) {
			t.Fatalf("bounded result leaked %q: %s", secret, result.Redacted)
		}
	}
}

func TestInspectManifestJSONUsesDefaultFindingBound(t *testing.T) {
	t.Parallel()

	data := make(map[string]string, DefaultMaxFindings+1)
	for index := 0; index <= DefaultMaxFindings; index++ {
		data[fmt.Sprintf("key-%03d", index)] = fmt.Sprintf("secret-%03d", index)
	}
	input, err := json.Marshal(map[string]any{"kind": "Secret", "data": data})
	if err != nil {
		t.Fatal(err)
	}

	result, err := InspectManifestJSON(input, Limits{})
	if err != nil {
		t.Fatalf("InspectManifestJSON() error = %v", err)
	}
	if len(result.Findings) != DefaultMaxFindings || !result.FindingsTruncated {
		t.Fatalf(
			"finding count = %d, truncated = %t; want %d, true",
			len(result.Findings),
			result.FindingsTruncated,
			DefaultMaxFindings,
		)
	}
	for index := 0; index <= DefaultMaxFindings; index++ {
		if bytes.Contains(result.Redacted, []byte(fmt.Sprintf("secret-%03d", index))) {
			t.Fatalf("default-bounded result leaked secret %d", index)
		}
	}
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("unmarshal got JSON: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("unmarshal want JSON: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}
