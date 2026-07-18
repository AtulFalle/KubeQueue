package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCORSMiddleware(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		origin     string
		method     string
		wantStatus int
		wantOrigin string
	}{
		{
			name:       "allows configured origin",
			origin:     "http://localhost:8081",
			method:     http.MethodOptions,
			wantStatus: http.StatusNoContent,
			wantOrigin: "http://localhost:8081",
		},
		{
			name:       "rejects unknown preflight origin",
			origin:     "https://example.com",
			method:     http.MethodOptions,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "does not add headers without an origin",
			method:     http.MethodGet,
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router := gin.New()
			router.Use(corsMiddleware("http://localhost:8081"))
			router.Any("/test", func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})

			request := httptest.NewRequest(tt.method, "/test", nil)
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if origin := response.Header().Get("Access-Control-Allow-Origin"); origin != tt.wantOrigin {
				t.Fatalf("allowed origin = %q, want %q", origin, tt.wantOrigin)
			}
		})
	}
}
