package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumnihq/lumni/internal/resource"
	"github.com/lumnihq/lumni/pkg/protocol"
)

type Backfiller struct {
	pool   *pgxpool.Pool
	client *http.Client
	logger *slog.Logger
	token  string
	apiURL string
}

func NewBackfiller(pool *pgxpool.Pool, client *http.Client, logger *slog.Logger, token, apiURL string) *Backfiller {
	return &Backfiller{pool: pool, client: client, logger: logger, token: token, apiURL: strings.TrimRight(apiURL, "/")}
}

type backfillRepository struct{ tenantID, id, owner, name string }

type apiPull struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

type apiCommit struct {
	SHA     string `json:"sha"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Files []struct {
		Filename string `json:"filename"`
		Status   string `json:"status"`
		SHA      string `json:"sha"`
	} `json:"files"`
}

func (b *Backfiller) Once(ctx context.Context) error {
	if b.token == "" {
		return nil
	}
	rows, err := b.pool.Query(ctx, `SELECT tenant_id::text,id::text,owner,name FROM repositories
		WHERE provider='github' AND owner<>'' AND name<>'' AND updated_at<clock_timestamp()-interval '5 minutes'
		ORDER BY updated_at LIMIT 10`)
	if err != nil {
		return err
	}
	var repositories []backfillRepository
	for rows.Next() {
		var repository backfillRepository
		if err := rows.Scan(&repository.tenantID, &repository.id, &repository.owner, &repository.name); err != nil {
			rows.Close()
			return err
		}
		repositories = append(repositories, repository)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, repository := range repositories {
		if err := b.backfill(ctx, repository); err != nil {
			b.logger.Warn("github backfill deferred", "tenant_id", repository.tenantID, "repository_id", repository.id, "error", err)
		}
	}
	return nil
}

func (b *Backfiller) backfill(ctx context.Context, repository backfillRepository) error {
	base := b.apiURL + "/repos/" + url.PathEscape(repository.owner) + "/" + url.PathEscape(repository.name)
	var pulls []apiPull
	if err := b.get(ctx, base+"/pulls?state=all&per_page=100", &pulls); err != nil {
		return err
	}
	var commits []apiCommit
	if err := b.get(ctx, base+"/commits?per_page=100", &commits); err != nil {
		return err
	}
	var latest apiCommit
	if len(commits) > 0 {
		if err := b.get(ctx, base+"/commits/"+url.PathEscape(commits[0].SHA), &latest); err != nil {
			return err
		}
	}

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, pull := range pulls {
		metadata, _ := json.Marshal(pull)
		_, err = tx.Exec(ctx, `INSERT INTO pull_requests(tenant_id,repository_id,external_number,state,head_sha,base_sha,title,metadata)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(tenant_id,repository_id,external_number) DO UPDATE
			SET state=EXCLUDED.state,head_sha=EXCLUDED.head_sha,base_sha=EXCLUDED.base_sha,title=EXCLUDED.title,metadata=EXCLUDED.metadata,updated_at=clock_timestamp()`, repository.tenantID, repository.id, fmt.Sprint(pull.Number), pull.State, pull.Head.SHA, pull.Base.SHA, pull.Title, metadata)
		if err != nil {
			return err
		}
	}
	for _, commit := range commits {
		parents := make([]string, 0, len(commit.Parents))
		for _, parent := range commit.Parents {
			parents = append(parents, parent.SHA)
		}
		metadata, _ := json.Marshal(commit)
		_, err = tx.Exec(ctx, `INSERT INTO git_commits(tenant_id,repository_id,sha,parent_shas,message,authored_at,metadata)
			VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id,repository_id,sha) DO UPDATE
			SET parent_shas=EXCLUDED.parent_shas,message=EXCLUDED.message,authored_at=EXCLUDED.authored_at,metadata=EXCLUDED.metadata`, repository.tenantID, repository.id, commit.SHA, parents, commit.Commit.Message, nullableTime(commit.Commit.Author.Date), metadata)
		if err != nil {
			return err
		}
	}
	repositoryResource := "github:repository:" + repository.id
	_, err = tx.Exec(ctx, `INSERT INTO resources(tenant_id,id,type,external_id,namespace,metadata)
		VALUES($1,$2,'repository',$3,'github',$4) ON CONFLICT(tenant_id,id) DO NOTHING`, repository.tenantID, repositoryResource, repository.id, []byte(`{"source":"github_backfill"}`))
	if err != nil {
		return err
	}
	for _, file := range latest.Files {
		fileRef := protocol.ResourceRef{Namespace: "github:" + repository.id, Type: "file", ExternalID: file.Filename}
		fileID := resource.CanonicalID(fileRef)
		version, source := file.SHA, "git_blob"
		if version == "" || file.Status == "removed" {
			version, source = latest.SHA, "git_commit"
		}
		metadata, _ := json.Marshal(map[string]any{"path": file.Filename, "status": file.Status, "repository_id": repository.id})
		_, err = tx.Exec(ctx, `INSERT INTO resources(tenant_id,id,type,external_id,namespace,current_version,version_source,version_confidence,version_observed_at,metadata)
			VALUES($1,$2,'file',$3,$4,$5,$6,1,clock_timestamp(),$7) ON CONFLICT(tenant_id,id) DO UPDATE
			SET current_version=EXCLUDED.current_version,version_source=EXCLUDED.version_source,version_confidence=1,version_observed_at=clock_timestamp(),metadata=EXCLUDED.metadata,updated_at=clock_timestamp()`, repository.tenantID, fileID, file.Filename, fileRef.Namespace, version, source, metadata)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO resource_edges(tenant_id,source_resource_id,relationship,target_resource_id,source,metadata)
			VALUES($1,$2,'CONTAINS',$3,'github_backfill',$4) ON CONFLICT(tenant_id,source_resource_id,relationship,target_resource_id)
			DO UPDATE SET metadata=EXCLUDED.metadata,updated_at=clock_timestamp()`, repository.tenantID, repositoryResource, fileID, metadata)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE repositories SET metadata=metadata||jsonb_build_object('last_backfilled_at',clock_timestamp()),updated_at=clock_timestamp()
		WHERE tenant_id=$1 AND id=$2`, repository.tenantID, repository.id)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (b *Backfiller) get(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("github API returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}
