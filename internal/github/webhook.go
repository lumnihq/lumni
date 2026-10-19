package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"lumni/internal/resource"
	"lumni/pkg/protocol"
)

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

type RepositoryInput struct {
	ID            string          `json:"id,omitempty"`
	Provider      string          `json:"provider"`
	ExternalID    string          `json:"external_id"`
	Owner         string          `json:"owner,omitempty"`
	Name          string          `json:"name"`
	DefaultBranch string          `json:"default_branch,omitempty"`
	URL           *string         `json:"url,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

func Verify(secret string, body []byte, header string) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	received, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(received, mac.Sum(nil))
}

func (s *Service) PutRepository(ctx context.Context, tenantID string, in RepositoryInput) (map[string]any, error) {
	if in.Provider == "" || in.ExternalID == "" || in.Name == "" {
		return nil, errors.New("provider, external_id, and name are required")
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	if len(in.Metadata) == 0 {
		in.Metadata = []byte("{}")
	}
	var id, provider, externalID, owner, name, branch string
	var url *string
	var metadata []byte
	var created, updated time.Time
	err := s.pool.QueryRow(ctx, `INSERT INTO repositories(id,tenant_id,provider,external_id,owner,name,default_branch,url,metadata)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)ON CONFLICT(tenant_id,provider,external_id)DO UPDATE SET owner=EXCLUDED.owner,name=EXCLUDED.name,default_branch=EXCLUDED.default_branch,url=EXCLUDED.url,metadata=EXCLUDED.metadata,updated_at=clock_timestamp()RETURNING id::text,provider,external_id,owner,name,default_branch,url,metadata,created_at,updated_at`, in.ID, tenantID, in.Provider, in.ExternalID, in.Owner, in.Name, in.DefaultBranch, in.URL, in.Metadata).Scan(&id, &provider, &externalID, &owner, &name, &branch, &url, &metadata, &created, &updated)
	if err != nil {
		return nil, err
	}
	var meta any
	_ = json.Unmarshal(metadata, &meta)
	return map[string]any{"id": id, "provider": provider, "external_id": externalID, "owner": owner, "name": name, "default_branch": branch, "url": url, "metadata": meta, "created_at": created, "updated_at": updated}, nil
}

func (s *Service) ListRepositories(ctx context.Context, tenantID string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,provider,external_id,owner,name,default_branch,url,metadata,created_at,updated_at FROM repositories WHERE tenant_id=$1 ORDER BY updated_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, provider, externalID, owner, name, branch string
		var url *string
		var metadata []byte
		var created, updated time.Time
		if err := rows.Scan(&id, &provider, &externalID, &owner, &name, &branch, &url, &metadata, &created, &updated); err != nil {
			return nil, err
		}
		var meta any
		_ = json.Unmarshal(metadata, &meta)
		out = append(out, map[string]any{"id": id, "provider": provider, "external_id": externalID, "owner": owner, "name": name, "default_branch": branch, "url": url, "metadata": meta, "created_at": created, "updated_at": updated})
	}
	return out, rows.Err()
}

func (s *Service) TenantBySlug(ctx context.Context, slug string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug=$1`, slug).Scan(&id)
	return id, err
}

func (s *Service) Ingest(ctx context.Context, tenantID, delivery, event string, body []byte) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,id,type,source,occurred_at,payload)VALUES($1,$2,$3,'github',clock_timestamp(),$4)ON CONFLICT(tenant_id,id)DO NOTHING`, tenantID, "github:"+delivery, "GITHUB_"+strings.ToUpper(event), body)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	var payload struct {
		Repository struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			HTMLURL       string `json:"html_url"`
			DefaultBranch string `json:"default_branch"`
			Owner         struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		PullRequest *struct {
			Number       int `json:"number"`
			State, Title string
			Head         struct {
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				SHA string `json:"sha"`
			} `json:"base"`
		} `json:"pull_request"`
		After   string `json:"after"`
		Ref     string `json:"ref"`
		Commits []struct {
			ID        string    `json:"id"`
			Message   string    `json:"message"`
			Timestamp time.Time `json:"timestamp"`
			Added     []string  `json:"added"`
			Modified  []string  `json:"modified"`
			Removed   []string  `json:"removed"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, err
	}
	repoID := uuid.NewString()
	url := payload.Repository.HTMLURL
	metadata, _ := json.Marshal(map[string]any{"full_name": payload.Repository.FullName})
	err = tx.QueryRow(ctx, `INSERT INTO repositories(id,tenant_id,provider,external_id,owner,name,default_branch,url,metadata)VALUES($1,$2,'github',$3,$4,$5,$6,$7,$8)ON CONFLICT(tenant_id,provider,external_id)DO UPDATE SET owner=EXCLUDED.owner,name=EXCLUDED.name,default_branch=EXCLUDED.default_branch,url=EXCLUDED.url,metadata=EXCLUDED.metadata,updated_at=clock_timestamp()RETURNING id::text`, repoID, tenantID, fmt.Sprint(payload.Repository.ID), payload.Repository.Owner.Login, payload.Repository.Name, payload.Repository.DefaultBranch, url, metadata).Scan(&repoID)
	if err != nil {
		return false, err
	}
	if payload.PullRequest != nil {
		_, err = tx.Exec(ctx, `INSERT INTO pull_requests(tenant_id,repository_id,external_number,state,head_sha,base_sha,title,metadata)VALUES($1,$2,$3,$4,$5,$6,$7,$8)ON CONFLICT(tenant_id,repository_id,external_number)DO UPDATE SET state=EXCLUDED.state,head_sha=EXCLUDED.head_sha,base_sha=EXCLUDED.base_sha,title=EXCLUDED.title,metadata=EXCLUDED.metadata,updated_at=clock_timestamp()`, tenantID, repoID, fmt.Sprint(payload.PullRequest.Number), payload.PullRequest.State, payload.PullRequest.Head.SHA, payload.PullRequest.Base.SHA, payload.PullRequest.Title, body)
		if err != nil {
			return false, err
		}
	}
	if payload.After != "" {
		resourceID := "github:repository:" + repoID
		_, err = tx.Exec(ctx, `INSERT INTO resources(tenant_id,id,type,external_id,namespace,current_version,version_source,version_confidence,version_observed_at,metadata)VALUES($1,$2,'repository',$3,'github',$4,'git_sha',1,clock_timestamp(),$5)ON CONFLICT(tenant_id,id)DO UPDATE SET current_version=EXCLUDED.current_version,version_source='git_sha',version_confidence=1,version_observed_at=clock_timestamp(),updated_at=clock_timestamp(),metadata=EXCLUDED.metadata`, tenantID, resourceID, repoID, payload.After, body)
		if err != nil {
			return false, err
		}
	}
	for _, commit := range payload.Commits {
		_, err = tx.Exec(ctx, `INSERT INTO git_commits(tenant_id,repository_id,sha,message,authored_at,metadata)
			VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant_id,repository_id,sha) DO UPDATE
			SET message=EXCLUDED.message,authored_at=EXCLUDED.authored_at,metadata=EXCLUDED.metadata`, tenantID, repoID, commit.ID, commit.Message, nullableTime(commit.Timestamp), body)
		if err != nil {
			return false, err
		}
		for status, files := range map[string][]string{"added": commit.Added, "modified": commit.Modified, "removed": commit.Removed} {
			for _, path := range files {
				fileRef := protocol.ResourceRef{Namespace: "github:" + repoID, Type: "file", ExternalID: path}
				fileID := resource.CanonicalID(fileRef)
				fileMetadata, _ := json.Marshal(map[string]any{"path": path, "status": status, "repository_id": repoID})
				_, err = tx.Exec(ctx, `INSERT INTO resources(tenant_id,id,type,external_id,namespace,current_version,version_source,version_confidence,version_observed_at,metadata)
					VALUES($1,$2,'file',$3,$4,$5,'git_commit',1,clock_timestamp(),$6)
					ON CONFLICT(tenant_id,id) DO UPDATE SET current_version=EXCLUDED.current_version,version_source='git_commit',version_confidence=1,
					version_observed_at=clock_timestamp(),metadata=EXCLUDED.metadata,updated_at=clock_timestamp()`, tenantID, fileID, path, fileRef.Namespace, commit.ID, fileMetadata)
				if err != nil {
					return false, err
				}
				_, err = tx.Exec(ctx, `INSERT INTO resource_versions(tenant_id,resource_id,version,version_source,observed_at,source,confidence,authoritative,metadata)
					VALUES($1,$2,$3,'git_commit',clock_timestamp(),'github',1,true,$4) ON CONFLICT DO NOTHING`, tenantID, fileID, commit.ID, fileMetadata)
				if err != nil {
					return false, err
				}
				_, err = tx.Exec(ctx, `INSERT INTO resource_edges(tenant_id,source_resource_id,relationship,target_resource_id,source,metadata)
					VALUES($1,$2,'CONTAINS',$3,'github',$4) ON CONFLICT(tenant_id,source_resource_id,relationship,target_resource_id)
					DO UPDATE SET metadata=EXCLUDED.metadata,updated_at=clock_timestamp()`, tenantID, "github:repository:"+repoID, fileID, fileMetadata)
				if err != nil {
					return false, err
				}
			}
		}
	}
	return true, tx.Commit(ctx)
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
