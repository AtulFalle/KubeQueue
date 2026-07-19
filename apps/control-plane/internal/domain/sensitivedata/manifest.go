// Package sensitivedata owns pure structural controls for sensitive workload data.
package sensitivedata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

const (
	DefaultMaxInputBytes = 1 << 20
	DefaultMaxFindings   = 100
	RedactedValue        = "[REDACTED]"
)

type ErrorCode string

const (
	ErrorInvalidLimits ErrorCode = "sensitive_data.invalid_limits"
	ErrorInputTooLarge ErrorCode = "sensitive_data.input_too_large"
	ErrorMalformedJSON ErrorCode = "sensitive_data.malformed_json"
)

// Error reports a bounded-input or decoding failure without including manifest data.
type Error struct {
	Code   ErrorCode
	Limit  int
	Actual int
	cause  error
}

func (e *Error) Error() string {
	switch e.Code {
	case ErrorInvalidLimits:
		return "sensitive data limits must not be negative"
	case ErrorInputTooLarge:
		return fmt.Sprintf("sensitive data input is %d bytes; limit is %d", e.Actual, e.Limit)
	case ErrorMalformedJSON:
		return "sensitive data input is not one JSON value"
	default:
		return "sensitive data processing failed"
	}
}

func (e *Error) Unwrap() error {
	return e.cause
}

type Limits struct {
	MaxInputBytes int
	MaxFindings   int
}

type ReasonCode string

const (
	ReasonSecretData       ReasonCode = "manifest.secret_data"
	ReasonSecretStringData ReasonCode = "manifest.secret_string_data"
	ReasonEnvLiteral       ReasonCode = "manifest.env_literal_credential"
	ReasonCredentialArg    ReasonCode = "manifest.credential_argument"
	ReasonSensitiveField   ReasonCode = "manifest.sensitive_field"
	ReasonAnnotation       ReasonCode = "manifest.sensitive_annotation"
)

// Finding identifies a redacted location using an RFC 6901 JSON Pointer.
// It intentionally contains no value or value-derived metadata.
type Finding struct {
	Reason ReasonCode `json:"reason"`
	Path   string     `json:"path"`
}

type Result struct {
	Redacted          []byte
	Findings          []Finding
	FindingsTruncated bool
}

// InspectManifestJSON redacts one JSON value and reports bounded, deterministic findings.
// Zero limit values select defaults.
func InspectManifestJSON(input []byte, limits Limits) (Result, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return Result{}, err
	}
	if len(input) > limits.MaxInputBytes {
		return Result{}, &Error{
			Code:   ErrorInputTooLarge,
			Limit:  limits.MaxInputBytes,
			Actual: len(input),
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var manifest any
	if err := decoder.Decode(&manifest); err != nil {
		return Result{}, malformedJSONError(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Result{}, malformedJSONError(err)
	}

	inspector := manifestInspector{maxFindings: limits.MaxFindings}
	inspector.walk(manifest, "")

	redacted, err := json.Marshal(manifest)
	if err != nil {
		return Result{}, malformedJSONError(err)
	}
	return Result{
		Redacted:          redacted,
		Findings:          inspector.findings,
		FindingsTruncated: inspector.findingsTruncated,
	}, nil
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxInputBytes < 0 || limits.MaxFindings < 0 {
		return Limits{}, &Error{Code: ErrorInvalidLimits}
	}
	if limits.MaxInputBytes == 0 {
		limits.MaxInputBytes = DefaultMaxInputBytes
	}
	if limits.MaxFindings == 0 {
		limits.MaxFindings = DefaultMaxFindings
	}
	return limits, nil
}

func malformedJSONError(cause error) error {
	return &Error{Code: ErrorMalformedJSON, cause: cause}
}

type manifestInspector struct {
	maxFindings       int
	findings          []Finding
	findingsTruncated bool
}

func (i *manifestInspector) walk(value any, path string) {
	switch typed := value.(type) {
	case map[string]any:
		i.walkObject(typed, path)
	case []any:
		for index, item := range typed {
			i.walk(item, appendPath(path, fmt.Sprintf("%d", index)))
		}
	}
}

func (i *manifestInspector) walkObject(object map[string]any, path string) {
	kind, _ := object["kind"].(string)
	isSecret := strings.EqualFold(strings.TrimSpace(kind), "Secret")

	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value := object[key]
		valuePath := appendPath(path, key)

		if isSecret && (key == "data" || key == "stringData") {
			reason := ReasonSecretData
			if key == "stringData" {
				reason = ReasonSecretStringData
			}
			i.redactSecretValues(object, key, value, valuePath, reason)
			continue
		}
		if key == "annotations" {
			i.walkAnnotations(value, valuePath)
			continue
		}
		if key == "env" {
			i.redactEnvironment(value, valuePath)
			i.walk(value, valuePath)
			continue
		}
		if key == "command" || key == "args" {
			i.redactArguments(value, valuePath)
			i.walk(value, valuePath)
			continue
		}
		if isSensitiveField(key) && isSensitiveScalar(value) {
			object[key] = RedactedValue
			i.addFinding(ReasonSensitiveField, valuePath)
			continue
		}
		i.walk(value, valuePath)
	}
}

func (i *manifestInspector) redactSecretValues(
	parent map[string]any,
	key string,
	value any,
	path string,
	reason ReasonCode,
) {
	values, ok := value.(map[string]any)
	if !ok {
		if value != nil {
			parent[key] = RedactedValue
			i.addFinding(reason, path)
		}
		return
	}

	keys := make([]string, 0, len(values))
	for entryKey := range values {
		keys = append(keys, entryKey)
	}
	sort.Strings(keys)
	for _, entryKey := range keys {
		if values[entryKey] != nil {
			values[entryKey] = RedactedValue
			i.addFinding(reason, appendPath(path, entryKey))
		}
	}
}

func (i *manifestInspector) walkAnnotations(value any, path string) {
	annotations, ok := value.(map[string]any)
	if !ok {
		i.walk(value, path)
		return
	}

	keys := make([]string, 0, len(annotations))
	for key := range annotations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entryPath := appendPath(path, key)
		if isSensitiveAnnotation(key) && isNonEmptyString(annotations[key]) {
			annotations[key] = RedactedValue
			i.addFinding(ReasonAnnotation, entryPath)
			continue
		}
		i.walk(annotations[key], entryPath)
	}
}

func (i *manifestInspector) redactEnvironment(value any, path string) {
	entries, ok := value.([]any)
	if !ok {
		return
	}
	for index, entry := range entries {
		environment, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := environment["name"].(string)
		literal, hasLiteral := environment["value"]
		if !hasLiteral || !isCredentialEnvironmentName(name) || !isNonEmptyString(literal) {
			continue
		}
		environment["value"] = RedactedValue
		i.addFinding(ReasonEnvLiteral, appendPath(appendPath(path, fmt.Sprintf("%d", index)), "value"))
	}
}

func (i *manifestInspector) redactArguments(value any, path string) {
	arguments, ok := value.([]any)
	if !ok {
		return
	}
	expectValue := false
	for index, argument := range arguments {
		text, ok := argument.(string)
		if !ok {
			expectValue = false
			continue
		}
		if expectValue {
			expectValue = false
			if text != "" {
				arguments[index] = RedactedValue
				i.addFinding(ReasonCredentialArg, appendPath(path, fmt.Sprintf("%d", index)))
				continue
			}
		}

		redacted, sensitive, takesNext := redactCredentialArgument(text)
		expectValue = takesNext
		if sensitive {
			arguments[index] = redacted
			i.addFinding(ReasonCredentialArg, appendPath(path, fmt.Sprintf("%d", index)))
		}
	}
}

func (i *manifestInspector) addFinding(reason ReasonCode, path string) {
	if len(i.findings) < i.maxFindings {
		i.findings = append(i.findings, Finding{Reason: reason, Path: path})
		return
	}
	i.findingsTruncated = true
}

func redactCredentialArgument(argument string) (redacted string, sensitive, takesNext bool) {
	trimmed := strings.TrimSpace(argument)
	if strings.HasPrefix(trimmed, "--") {
		if separator := strings.IndexByte(trimmed, '='); separator >= 0 {
			key := trimmed[:separator]
			if isSensitiveCommandKey(key) && separator+1 < len(trimmed) {
				return argument[:strings.IndexByte(argument, '=')+1] + RedactedValue, true, false
			}
		} else if isSensitiveCommandKey(trimmed) {
			return argument, false, true
		}
	}

	if separator := strings.IndexByte(trimmed, '='); separator > 0 {
		name := trimmed[:separator]
		if isCredentialEnvironmentName(name) && separator+1 < len(trimmed) {
			return argument[:strings.IndexByte(argument, '=')+1] + RedactedValue, true, false
		}
	}
	if separator := strings.IndexByte(trimmed, ':'); separator > 0 {
		name := normalizeName(trimmed[:separator])
		if (name == "authorization" || name == "proxyauthorization" || name == "cookie") &&
			strings.TrimSpace(trimmed[separator+1:]) != "" {
			return argument[:strings.IndexByte(argument, ':')+1] + " " + RedactedValue, true, false
		}
	}
	if len(trimmed) > len("Bearer ") &&
		strings.EqualFold(trimmed[:len("Bearer ")], "Bearer ") {
		return RedactedValue, true, false
	}
	if hasURLPassword(trimmed) {
		return RedactedValue, true, false
	}
	return argument, false, false
}

func hasURLPassword(value string) bool {
	if !strings.Contains(value, "://") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return false
	}
	password, hasPassword := parsed.User.Password()
	return hasPassword && password != ""
}

func isSensitiveCommandKey(key string) bool {
	return isSensitiveNormalizedName(normalizeName(strings.TrimLeft(key, "-")))
}

func isSensitiveField(key string) bool {
	return isSensitiveNormalizedName(normalizeName(key))
}

func isSensitiveNormalizedName(name string) bool {
	switch name {
	case "authorization",
		"proxyauthorization",
		"password",
		"passwd",
		"token",
		"accesstoken",
		"refreshtoken",
		"idtoken",
		"bearertoken",
		"apikey",
		"clientsecret",
		"clientpassword",
		"secretaccesskey",
		"privatekey",
		"databaseurl",
		"dburl",
		"connectionstring",
		"cookie",
		"setcookie":
		return true
	default:
		return false
	}
}

func isCredentialEnvironmentName(name string) bool {
	normalized := normalizeEnvironmentName(name)
	if normalized == "" {
		return false
	}
	suffixes := [...]string{
		"PASSWORD",
		"PASSWD",
		"TOKEN",
		"ACCESS_TOKEN",
		"REFRESH_TOKEN",
		"ID_TOKEN",
		"BEARER_TOKEN",
		"API_KEY",
		"CLIENT_SECRET",
		"CLIENT_PASSWORD",
		"SECRET_ACCESS_KEY",
		"PRIVATE_KEY",
		"AUTHORIZATION",
		"DATABASE_URL",
		"DB_URL",
		"CONNECTION_STRING",
		"COOKIE",
	}
	for _, suffix := range suffixes {
		if normalized == suffix || strings.HasSuffix(normalized, "_"+suffix) {
			return true
		}
	}
	return false
}

func isSensitiveAnnotation(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "kubectl.kubernetes.io/last-applied-configuration" {
		return true
	}
	if separator := strings.LastIndexByte(lower, '/'); separator >= 0 {
		lower = lower[separator+1:]
	}
	return isSensitiveNormalizedName(normalizeName(lower)) ||
		isCredentialEnvironmentName(lower)
}

func normalizeName(value string) string {
	var builder strings.Builder
	for _, character := range strings.TrimSpace(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(unicode.ToLower(character))
		}
	}
	return builder.String()
}

func normalizeEnvironmentName(value string) string {
	var builder strings.Builder
	separatorPending := false
	for _, character := range strings.TrimSpace(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			if separatorPending && builder.Len() > 0 {
				builder.WriteByte('_')
			}
			separatorPending = false
			builder.WriteRune(unicode.ToUpper(character))
			continue
		}
		separatorPending = true
	}
	return strings.Trim(builder.String(), "_")
}

func isNonEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && text != ""
}

func isSensitiveScalar(value any) bool {
	if value == nil {
		return false
	}
	if text, ok := value.(string); ok {
		return text != ""
	}
	switch value.(type) {
	case map[string]any, []any:
		return false
	default:
		return true
	}
}

func appendPath(path, segment string) string {
	escaped := strings.ReplaceAll(segment, "~", "~0")
	escaped = strings.ReplaceAll(escaped, "/", "~1")
	return path + "/" + escaped
}
