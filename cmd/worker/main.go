package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lumnihq/lumni/internal/analytics"
	"github.com/lumnihq/lumni/internal/composio"
	"github.com/lumnihq/lumni/internal/config"
	"github.com/lumnihq/lumni/internal/coordination"
	"github.com/lumnihq/lumni/internal/resource"
	"github.com/lumnihq/lumni/internal/semantic"
	"github.com/lumnihq/lumni/internal/store"
	workerpkg "github.com/lumnihq/lumni/internal/worker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := store.Migrate(ctx, cfg.DatabaseURL, "up"); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}
	pools, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database failed", "error", err)
		os.Exit(1)
	}
	defer pools.Close()
	resolver := resource.NewResolver(pools.Query)
	kernel := coordination.New(pools.Admission, resolver, cfg.LeaseTTL)
	var options []workerpkg.Option
	composioClient, err := composio.NewClient(cfg.Composio)
	if err != nil {
		logger.Error("Composio configuration failed", "error", err)
		os.Exit(1)
	}
	options = append(options, workerpkg.WithComposio(composioClient))
	if cfg.AnalyticsBackend == "clickhouse" || cfg.AnalyticsBackend == "auto" {
		clickhouse := analytics.NewClickHouse(cfg.ClickHouseURL, cfg.ClickHouseDatabase, cfg.ClickHouseUser, cfg.ClickHousePassword, cfg.AnalyticsTimeout)
		if cfg.AnalyticsBackend == "auto" && clickhouse.Ping(ctx) != nil {
			logger.Info("optional ClickHouse exporter is not active")
		} else {
			exporter := analytics.NewExporter(pools.Worker, cfg.ClickHouseURL, cfg.ClickHouseDatabase, cfg.ClickHouseUser, cfg.ClickHousePassword, cfg.AnalyticsTimeout, logger)
			options = append(options, workerpkg.WithAnalyticsExporter(exporter))
		}
	}
	if cfg.SemanticProvider == "openrouter" {
		evaluator := semantic.NewOpenRouter(semantic.OpenRouterConfig{
			APIKey:       cfg.OpenRouterAPIKey,
			Model:        cfg.JEVModel,
			DecisionsURL: cfg.OpenRouterDecisions,
			ZDRURL:       cfg.OpenRouterZDR,
			Timeout:      cfg.SemanticTimeout,
			MaxRetries:   cfg.SemanticMaxRetries,
		})
		service := semantic.NewService(pools.Worker, evaluator, cfg.SemanticCandidates, logger)
		options = append(options, workerpkg.WithSemanticService(service))
	}
	w := workerpkg.New(pools.Worker, kernel, cfg.WebhookTimeout, logger, cfg.SecretEncryptionKey, cfg.GitHubAPIToken, cfg.GitHubAPIURL, options...)
	w.SetPayloadRetention(cfg.PayloadRetention)
	logger.Info("worker started")
	if err := w.Run(ctx, cfg.WorkerInterval); err != nil && err != context.Canceled {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
