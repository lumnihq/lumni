package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Pools struct {
	Admission *pgxpool.Pool
	Ingestion *pgxpool.Pool
	Query     *pgxpool.Pool
	Worker    *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Pools, error) {
	admission, err := openPool(ctx, databaseURL, 40, 4)
	if err != nil {
		return nil, fmt.Errorf("admission pool: %w", err)
	}
	cleanup := func() { admission.Close() }
	ingestion, err := openPool(ctx, databaseURL, 16, 1)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("ingestion pool: %w", err)
	}
	query, err := openPool(ctx, databaseURL, 12, 1)
	if err != nil {
		cleanup()
		ingestion.Close()
		return nil, fmt.Errorf("query pool: %w", err)
	}
	worker, err := openPool(ctx, databaseURL, 8, 1)
	if err != nil {
		cleanup()
		ingestion.Close()
		query.Close()
		return nil, fmt.Errorf("worker pool: %w", err)
	}
	return &Pools{Admission: admission, Ingestion: ingestion, Query: query, Worker: worker}, nil
}

func openPool(ctx context.Context, url string, max, min int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = max
	cfg.MinConns = min
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func (p *Pools) Close() {
	if p == nil {
		return
	}
	p.Admission.Close()
	p.Ingestion.Close()
	p.Query.Close()
	p.Worker.Close()
}
