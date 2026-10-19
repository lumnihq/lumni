package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"

	"github.com/lumnihq/lumni/internal/analytics"
	"github.com/lumnihq/lumni/internal/api"
	"github.com/lumnihq/lumni/internal/auth"
	"github.com/lumnihq/lumni/internal/composio"
	"github.com/lumnihq/lumni/internal/config"
	"github.com/lumnihq/lumni/internal/coordination"
	"github.com/lumnihq/lumni/internal/core"
	"github.com/lumnihq/lumni/internal/otel"
	"github.com/lumnihq/lumni/internal/resource"
	"github.com/lumnihq/lumni/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := store.Migrate(ctx, cfg.DatabaseURL, "up"); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}
	pools, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer pools.Close()
	authService := auth.New(pools.Query)
	if cfg.BootstrapAdminKey != "" {
		tenantID, err := authService.Bootstrap(ctx, cfg.BootstrapName, cfg.BootstrapSlug, cfg.BootstrapAdminKey, cfg.DefaultMode)
		if err != nil {
			logger.Error("bootstrap failed", "error", err)
			os.Exit(1)
		}
		logger.Info("bootstrap tenant ready", "tenant_id", tenantID, "tenant_slug", cfg.BootstrapSlug)
	}
	coreService := core.NewWithPools(pools.Ingestion, pools.Query)
	resolver := resource.NewResolver(pools.Query)
	kernel := coordination.New(pools.Admission, resolver, cfg.LeaseTTL)
	apiServer := api.New(pools.Query, authService, coreService, kernel, logger, cfg.RequestBodyLimit, cfg.GitHubWebhookSecret, cfg.SecretEncryptionKey)
	apiServer.SetPayloadRetention(cfg.PayloadRetention)
	analyticsStore := analytics.Store(analytics.NewPostgres(pools.Query))
	if cfg.AnalyticsBackend == "clickhouse" || cfg.AnalyticsBackend == "auto" {
		clickhouse := analytics.NewClickHouse(cfg.ClickHouseURL, cfg.ClickHouseDatabase, cfg.ClickHouseUser, cfg.ClickHousePassword, cfg.AnalyticsTimeout)
		if cfg.AnalyticsBackend == "clickhouse" || clickhouse.Ping(ctx) == nil {
			analyticsStore = analytics.NewResilient(clickhouse, analytics.NewPostgres(pools.Query), logger)
		} else {
			logger.Info("optional ClickHouse analytics is not active; using PostgreSQL")
		}
	}
	composioClient, err := composio.NewClient(cfg.Composio)
	if err != nil {
		logger.Error("Composio configuration failed", "error", err)
		os.Exit(1)
	}
	apiServer.Configure(api.RuntimeOptions{Composio: composioClient, ConsoleDir: cfg.ConsoleDir, Analytics: analyticsStore, SessionIdleTTL: cfg.SessionIdleTTL, SessionAbsoluteTTL: cfg.SessionAbsoluteTTL, PlatformKeys: cfg.PlatformKeys, TenantChecksPerDay: cfg.TenantChecksPerDay, TenantChecksPerSecond: cfg.TenantChecksPerSecond})
	otelReceiver := otel.New(pools.Ingestion, authService)

	httpServer := &http.Server{Addr: cfg.HTTPAddr, Handler: apiServer.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second}
	otlpHTTPServer := &http.Server{Addr: cfg.OTLPHTTPAddr, Handler: otelReceiver.HTTPHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	grpcListener, err := net.Listen("tcp", cfg.OTLPGRPCAddr)
	if err != nil {
		logger.Error("OTLP gRPC listener failed", "error", err)
		os.Exit(1)
	}
	grpcServer := grpc.NewServer()
	collectortracev1.RegisterTraceServiceServer(grpcServer, otelReceiver)

	errCh := make(chan error, 3)
	go func() { logger.Info("HTTP API listening", "addr", cfg.HTTPAddr); errCh <- httpServer.ListenAndServe() }()
	go func() {
		logger.Info("OTLP HTTP listening", "addr", cfg.OTLPHTTPAddr)
		errCh <- otlpHTTPServer.ListenAndServe()
	}()
	go func() {
		logger.Info("OTLP gRPC listening", "addr", cfg.OTLPGRPCAddr)
		errCh <- grpcServer.Serve(grpcListener)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
		}
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	grpcServer.GracefulStop()
	_ = otlpHTTPServer.Shutdown(shutdownCtx)
	_ = httpServer.Shutdown(shutdownCtx)
}

func logLevel(value string) slog.Level {
	switch value {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
