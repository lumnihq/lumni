package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func Migrate(ctx context.Context, databaseURL, direction string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	migrations, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 300))
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations, goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	switch direction {
	case "up":
		_, err = provider.Up(ctx)
	case "down":
		_, err = provider.Down(ctx)
	case "down-to-zero":
		_, err = provider.DownTo(ctx, 0)
	case "status":
		var status []*goose.MigrationStatus
		status, err = provider.Status(ctx)
		if err == nil {
			for _, migration := range status {
				fmt.Printf("%05d %s\n", migration.Source.Version, migration.State)
			}
		}
	default:
		return fmt.Errorf("unsupported migration direction %q", direction)
	}
	return err
}
