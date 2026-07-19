// Package httpserver configures and runs the control-plane HTTP server.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/config"
	"github.com/gin-gonic/gin"
)

const shutdownTimeout = 10 * time.Second

// Run starts the platform HTTP server.
func Run(ctx context.Context) error {
	store, err := persistence.OpenCompatible(ctx, os.Getenv("KUBEQUEUE_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	namespaceScope, err := config.NamespaceScopeFromEnvironment()
	if err != nil {
		return err
	}

	router := gin.New()
	router.Use(
		requestLogger(),
		gin.Recovery(),
		corsMiddleware(os.Getenv("KUBEQUEUE_CORS_ALLOWED_ORIGINS")),
	)
	router.GET("/healthz", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/readyz", func(c *gin.Context) {
		if err := store.Ping(c.Request.Context()); err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.Status(http.StatusNoContent)
	})
	registerAPI(
		router,
		application.NewJobs(store, namespaceScope),
		store,
		os.Getenv("KUBEQUEUE_ADMIN_TOKEN"),
	)

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

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		slog.Info("http request",
			"operation", "http_request",
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(started).Milliseconds(),
		)
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
