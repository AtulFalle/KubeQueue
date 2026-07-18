// Package httpserver configures and runs the control-plane HTTP server.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const shutdownTimeout = 10 * time.Second

// Run starts the platform HTTP server. Product routes are added in later milestones.
func Run(ctx context.Context) error {
	router := gin.New()
	router.Use(
		gin.Logger(),
		gin.Recovery(),
		corsMiddleware(os.Getenv("KUBEQUEUE_CORS_ALLOWED_ORIGINS")),
	)
	router.GET("/healthz", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	server := &http.Server{
		Addr:              address(),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP: %w", err)
		}
		return nil
	}
}

func address() string {
	port := os.Getenv("KUBEQUEUE_API_PORT")
	if port == "" {
		port = "8080"
	}
	return ":" + port
}

func corsMiddleware(configuredOrigins string) gin.HandlerFunc {
	allowedOrigins := make(map[string]struct{})
	for origin := range strings.SplitSeq(configuredOrigins, ",") {
		if trimmed := strings.TrimSpace(origin); trimmed != "" {
			allowedOrigins[trimmed] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		_, allowed := allowedOrigins[origin]
		if allowed {
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		}

		if c.Request.Method == http.MethodOptions {
			if origin != "" && !allowed {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
