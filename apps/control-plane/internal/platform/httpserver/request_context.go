package httpserver

import (
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const requestIDHeader = "X-Request-ID"

var boundedCorrelationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)

func requestCorrelationMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ensureRequestCorrelation(c)
		c.Next()
	}
}

func ensureRequestCorrelation(c *gin.Context) {
	if metadata, ok := application.AuditRequestMetadataFromContext(c.Request.Context()); ok &&
		metadata.RequestID != "" && metadata.TraceID != "" {
		c.Header(requestIDHeader, metadata.RequestID)
		return
	}
	requestID := strings.TrimSpace(c.GetHeader(requestIDHeader))
	if !boundedCorrelationID.MatchString(requestID) {
		requestID = uuid.NewString()
	}
	traceID := traceIDFromTraceparent(c.GetHeader("traceparent"))
	if traceID == "" {
		traceID = uuid.NewString()
	}
	c.Header(requestIDHeader, requestID)
	c.Request = c.Request.WithContext(application.WithAuditRequestMetadata(
		c.Request.Context(),
		application.AuditRequestMetadata{
			RequestID: requestID,
			TraceID:   traceID,
			Source:    directPeerAddress(c.Request.RemoteAddr),
			UserAgent: c.Request.UserAgent(),
		},
	))
}

func traceIDFromTraceparent(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 4 || len(parts[0]) != 2 || len(parts[1]) != 32 ||
		len(parts[2]) != 16 || len(parts[3]) != 2 || parts[0] == "ff" {
		return ""
	}
	trace, traceErr := hex.DecodeString(parts[1])
	parent, parentErr := hex.DecodeString(parts[2])
	flags, flagsErr := hex.DecodeString(parts[3])
	if traceErr != nil || parentErr != nil || flagsErr != nil ||
		allZero(trace) || allZero(parent) || len(flags) != 1 {
		return ""
	}
	return strings.ToLower(parts[1])
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
