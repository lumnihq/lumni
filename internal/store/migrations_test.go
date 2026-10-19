//go:build integration

package store

import (
	"context"
	"database/sql"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
)

func TestConcurrentServiceStartupMigrations(t *testing.T) {
	dsn := os.Getenv("LUMNI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("LUMNI_TEST_DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := "startup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = db.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errors <- Migrate(t.Context(), u.String(), "up") })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var version int
	if err = db.QueryRowContext(t.Context(), "SELECT max(version_id) FROM "+schema+".goose_db_version").Scan(&version); err != nil || version != 24 {
		t.Fatalf("version=%d %v", version, err)
	}
}

func TestUpgradePreservesExternalHistory(t *testing.T) {
	databaseURL := os.Getenv("LUMNI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LUMNI_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := "upgrade_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	// The temporary schema owns every test table. No existing workspace is downgraded.
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrations, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
	var tenant string
	if err = db.QueryRowContext(ctx, `INSERT INTO tenants(name,slug)VALUES('upgrade','upgrade')RETURNING id::text`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO actors(tenant_id,id,type)VALUES($1,'actor','AGENT')`,
		`INSERT INTO work(tenant_id,id,actor_id,goal,status)VALUES($1,'work','actor','preserve history','COMPLETED')`,
		`INSERT INTO actions(tenant_id,id,work_id,actor_id,operation,arguments_hash,status,effect_key)VALUES($1,'committed','work','actor','refund','hash','SUCCEEDED','legacy-key'),($1,'unknown','work','actor','refund','hash2','UNKNOWN','unknown-key')`,
		`INSERT INTO resources(tenant_id,id,type,external_id)VALUES($1,'resource','subscription','sub-1')`,
		`INSERT INTO action_resources(tenant_id,action_id,resource_id,mode,operation)VALUES($1,'committed','resource','WRITE','refund')`,
		`INSERT INTO action_results(tenant_id,action_id,status,external_result_id,result_metadata)VALUES($1,'committed','SUCCEEDED','re_original','{"original":true}'),($1,'unknown','UNKNOWN',NULL,'{"timeout":true}')`,
		`INSERT INTO effects(tenant_id,id,action_id,operation,effect_key,status,external_id)VALUES($1,'effect','committed','refund','legacy-key','COMMITTED','re_original')`,
		`INSERT INTO decisions(tenant_id,action_id,decision,reason_code,enforced,snapshot,kernel_version,resolver_version,policy_version)VALUES($1,'committed','ALLOW','NO_CONFLICT',true,'{"original":true}','coord-v1','resolver-v1','policy-v1')`,
		`INSERT INTO costs(tenant_id,id,action_id,work_id,provider,cost_type,amount_micros_usd,observed_at)VALUES($1,gen_random_uuid(),'committed','work','fixture','MODEL',123456,now())`,
	}
	for _, statement := range statements {
		if _, err = db.ExecContext(ctx, statement, tenant); err != nil {
			t.Fatal(err)
		}
	}
	for upgrade := 0; upgrade < 2; upgrade++ {
		if _, err = provider.Up(ctx); err != nil {
			t.Fatal(err)
		}
		var valid bool
		err = db.QueryRowContext(ctx, `SELECT
		 (SELECT execution_mode='EXTERNAL' FROM work WHERE tenant_id=$1 AND id='work')
		 AND (SELECT effect_identity_version='legacy' FROM actions WHERE tenant_id=$1 AND id='committed')
		 AND (SELECT status='SUCCEEDED' AND external_result_id='re_original' AND result_metadata='{"original":true}'::jsonb FROM action_results WHERE tenant_id=$1 AND action_id='committed')
		 AND (SELECT status='UNKNOWN' FROM action_results WHERE tenant_id=$1 AND action_id='unknown')
		 AND EXISTS(SELECT 1 FROM effect_occurrences WHERE tenant_id=$1 AND id='effect' AND status='COMMITTED')
		 AND EXISTS(SELECT 1 FROM effect_holds WHERE tenant_id=$1 AND action_id='unknown')
		 AND EXISTS(SELECT 1 FROM decisions WHERE tenant_id=$1 AND snapshot='{"original":true}'::jsonb)
		 AND (SELECT count(*)=2 FROM console_action_facts WHERE tenant_id=$1)
		 AND (SELECT sum(total)=2 AND sum(cost_micros_usd)=123456 FROM console_metrics WHERE tenant_id=$1)
		 AND (SELECT sum(total)=1 FROM console_reason_resource_metrics WHERE tenant_id=$1 AND resource_id='resource')
		 AND (SELECT sum(cost_micros_usd)=123456 FROM console_cost_metrics WHERE tenant_id=$1)`, tenant).Scan(&valid)
		if err != nil || !valid {
			t.Fatalf("upgrade %d changed history: valid=%v err=%v", upgrade, valid, err)
		}
		if upgrade == 0 {
			if _, err = provider.DownTo(ctx, 5); err != nil {
				t.Fatal(err)
			}
		}
	}
}
