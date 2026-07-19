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
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/config"
	platformoidc "github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/oidc"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/runtimemetrics"
	"github.com/gin-gonic/gin"
)

const shutdownTimeout = 10 * time.Second

// Run starts the platform HTTP server.
func Run(ctx context.Context) error {
	adminToken := os.Getenv("KUBEQUEUE_ADMIN_TOKEN")
	developmentSeed, err := developmentLocalAdminSeedFromEnvironment()
	if err != nil {
		return err
	}
	store, err := persistence.OpenCompatible(ctx, os.Getenv("KUBEQUEUE_DATABASE_URL"))
	if err != nil {
		runtimemetrics.SetSchemaHealthy(false)
		return err
	}
	runtimemetrics.SetSchemaHealthy(true)
	defer func() { _ = store.Close() }()
	sessions, browserOrigin, err := sessionsFromEnvironment(store)
	if err != nil {
		return err
	}
	var localAccounts *application.LocalAccounts
	if sessions != nil {
		localAccounts, err = application.NewLocalAccounts(store, sessions)
		if err != nil {
			return fmt.Errorf("configure local accounts: %w", err)
		}
	}
	setup, err := newSetupService(store, browserOrigin)
	if err != nil {
		return err
	}
	if localAccounts == nil {
		return errors.New("first-time local setup requires browser sessions")
	}
	setup.WithLocalPasswordHasher(localAccounts.HashPassword)
	if developmentSeed {
		if localAccounts == nil {
			return errors.New("development local-admin seed requires browser sessions")
		}
		slog.Warn(
			"development-only admin/admin account is enabled; never use this setting in production",
			"operation", "development_local_admin_seed",
		)
		if err := localAccounts.SeedDevelopmentLocalAdmin(ctx); err != nil {
			return err
		}
	}
	oidcValidator := platformoidc.NewValidator(nil)
	oidcProviders, err := store.ActiveOIDCProviders(ctx)
	if err != nil {
		return err
	}
	sessionEncryptionKey, err := sessionEncryptionKeyFromEnvironment()
	if err != nil {
		return err
	}
	var identityProviders *application.IdentityProviders
	var dynamicOIDC *dynamicOIDCClient
	if len(sessionEncryptionKey) > 0 {
		identityProviders, err = application.NewIdentityProviders(
			store, oidcValidator, sessionEncryptionKey,
		)
		if err != nil {
			return fmt.Errorf("configure identity providers: %w", err)
		}
		dynamicOIDC = newDynamicOIDCClient(identityProviders)
		if sessions != nil {
			sessions.WithTokenRefresher(dynamicOIDC)
		}
	}
	serviceAccounts, err := serviceAccountsFromEnvironment(store)
	if err != nil {
		return err
	}
	breakGlass, err := breakGlassFromEnvironment(ctx, store)
	if err != nil {
		return err
	}
	accessManagement, err := application.NewAccessManagement(store, store)
	if err != nil {
		return fmt.Errorf("configure access management: %w", err)
	}
	admissionAdministration, err := application.NewAdmissionAdministration(store, store)
	if err != nil {
		return fmt.Errorf("configure admission administration: %w", err)
	}
	if strings.TrimSpace(adminToken) == "" && len(oidcProviders) == 0 &&
		localAccounts == nil && serviceAccounts == nil && breakGlass == nil {
		return errors.New("at least one authentication mode must be configured")
	}
	oidcAuthentication := application.NewOIDCAuthentication(store, oidcValidator)
	oidcAuthenticators := []bearerAuthenticator{oidcAuthentication}
	apiAuthenticators := append([]bearerAuthenticator(nil), oidcAuthenticators...)
	if serviceAccounts != nil {
		apiAuthenticators = append(
			[]bearerAuthenticator{nativeBearerAuthenticator{serviceAccounts: serviceAccounts}},
			apiAuthenticators...,
		)
	}
	if breakGlass != nil {
		apiAuthenticators = append(
			[]bearerAuthenticator{breakGlassBearerAuthenticator{service: breakGlass}},
			apiAuthenticators...,
		)
	}
	namespaceScope, err := config.NamespaceScopeFromEnvironment()
	if err != nil {
		return err
	}
	auditPolicy, err := audit.NewRetentionPolicy(365 * 24 * time.Hour)
	if err != nil {
		return fmt.Errorf("configure audit retention: %w", err)
	}
	auditWriter, err := application.NewBoundedAuditWriter(store, 256)
	if err != nil {
		return fmt.Errorf("configure bounded audit writer: %w", err)
	}
	auditWriterContext, stopAuditWriter := context.WithCancel(ctx)
	auditWriterDone := make(chan struct{})
	go func() {
		defer close(auditWriterDone)
		if err := auditWriter.Run(auditWriterContext); err != nil &&
			!errors.Is(err, context.Canceled) {
			slog.Error("audit writer stopped", "operation", "audit_writer", "error", err)
		}
	}()
	defer func() {
		stopAuditWriter()
		<-auditWriterDone
	}()

	router := gin.New()
	router.HandleMethodNotAllowed = true
	router.Use(
		requestCorrelationMiddleware(),
		requestMetrics(),
		requestLogger(),
		recoveryMiddleware(),
		corsMiddleware(os.Getenv("KUBEQUEUE_CORS_ALLOWED_ORIGINS")),
		auditDenialMiddleware(auditWriter, auditPolicy),
	)
	router.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/v1") {
			writeError(c, http.StatusNotFound, "ROUTE_NOT_FOUND", "API route not found")
			return
		}
		c.Status(http.StatusNotFound)
	})
	router.NoMethod(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/v1") {
			writeError(c, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"method is not allowed for this API route")
			return
		}
		c.Status(http.StatusMethodNotAllowed)
	})
	router.GET("/healthz", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/metrics", gin.WrapH(runtimemetrics.Handler()))
	router.GET("/readyz", func(c *gin.Context) {
		if err := store.Ping(c.Request.Context()); err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.Status(http.StatusNoContent)
	})
	registerSetupAPI(router, setup)
	registerSessionAPI(
		router,
		sessions,
		localAccounts,
		runtimeLoginStatus{local: store, oidc: identityProviders},
		adminToken,
		browserOrigin,
		os.Getenv("KUBEQUEUE_BFF_INTERNAL_KEY"),
		oidcAuthenticators...,
	)
	registerOIDCTokenExchange(router, os.Getenv("KUBEQUEUE_BFF_INTERNAL_KEY"), dynamicOIDC)
	registerIdentityProviderAPI(
		router, identityProviders, adminToken, sessions, browserOrigin, apiAuthenticators...,
	)
	registerAPI(
		router,
		application.NewJobs(store, namespaceScope, store),
		application.NewSupportDiagnostics(
			store, store, auditWriter, os.Getenv("KUBEQUEUE_RELEASE_VERSION"),
		),
		store,
		store,
		adminToken,
		sessions,
		browserOrigin,
		apiAuthenticators...,
	)
	registerAccessAPI(
		router,
		accessManagement,
		serviceAccounts,
		adminToken,
		sessions,
		browserOrigin,
		apiAuthenticators...,
	)
	registerAdmissionAdministrationAPI(
		router,
		admissionAdministration,
		adminToken,
		sessions,
		browserOrigin,
		apiAuthenticators...,
	)
	registerAuditAPI(
		router,
		application.NewAuditService(store, store),
		adminToken,
		sessions,
		browserOrigin,
		apiAuthenticators...,
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
		metadata, _ := application.AuditRequestMetadataFromContext(c.Request.Context())
		slog.Info("http request",
			"operation", "http_request",
			"request_id", metadata.RequestID,
			"trace_id", metadata.TraceID,
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(started).Milliseconds(),
		)
	}
}

func requestMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		done := runtimemetrics.HTTPStarted()
		defer done()
		started := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		runtimemetrics.ObserveHTTP(
			c.Request.Method, route, c.Writer.Status(), time.Since(started),
		)
	}
}

func recoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				c.Abort()
				writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR",
					fmt.Sprint(recovered))
			}
		}()
		c.Next()
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
			c.Header("Access-Control-Allow-Headers",
				"Authorization, Content-Type, Idempotency-Key, If-Match, traceparent, X-Request-ID")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Expose-Headers", "ETag, X-Request-ID")
			c.Header("Vary", "Origin")
		}

		if c.Request.Method == http.MethodOptions {
			if origin != "" && !allowed {
				c.Abort()
				if strings.HasPrefix(c.Request.URL.Path, "/api/v1") {
					writeError(c, http.StatusForbidden, "CORS_FORBIDDEN",
						"request origin is not allowed")
				} else {
					c.Status(http.StatusForbidden)
				}
				return
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
