//go:build integration

package integration

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	githubingest "lumni/internal/github"
	"lumni/internal/resource"
	"lumni/pkg/protocol"
)

type githubRoundTrip func(*http.Request) (*http.Response, error)

func (f githubRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestGitHubDeliveryEnrichesCommitsAndChangedFiles(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	service := githubingest.New(f.pool)
	payload := []byte(`{
		"repository":{"id":991,"name":"lumni","full_name":"acme/lumni","html_url":"https://github.example/acme/lumni","default_branch":"main","owner":{"login":"acme"}},
		"after":"abc123","ref":"refs/heads/main",
		"commits":[{"id":"abc123","message":"update coordination","timestamp":"2026-08-28T10:00:00Z","added":[],"modified":["internal/kernel.go"],"removed":[]}]
	}`)
	created, err := service.Ingest(t.Context(), f.tenant, "delivery-1", "push", payload)
	if err != nil || !created {
		t.Fatalf("first delivery: created=%v err=%v", created, err)
	}
	created, err = service.Ingest(t.Context(), f.tenant, "delivery-1", "push", payload)
	if err != nil || created {
		t.Fatalf("duplicate delivery: created=%v err=%v", created, err)
	}
	var repositoryID string
	if err := f.pool.QueryRow(t.Context(), `SELECT id::text FROM repositories WHERE tenant_id=$1 AND provider='github' AND external_id='991'`, f.tenant).Scan(&repositoryID); err != nil {
		t.Fatal(err)
	}
	var commits int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM git_commits WHERE tenant_id=$1 AND repository_id=$2 AND sha='abc123'`, f.tenant, repositoryID).Scan(&commits); err != nil {
		t.Fatal(err)
	}
	if commits != 1 {
		t.Fatalf("commit count=%d", commits)
	}
	fileID := resource.CanonicalID(protocol.ResourceRef{Namespace: "github:" + repositoryID, Type: "file", ExternalID: "internal/kernel.go"})
	var version, source string
	if err := f.pool.QueryRow(t.Context(), `SELECT current_version,version_source FROM resources WHERE tenant_id=$1 AND id=$2`, f.tenant, fileID).Scan(&version, &source); err != nil {
		t.Fatal(err)
	}
	if version != "abc123" || source != "git_commit" {
		t.Fatalf("file version=%s source=%s", version, source)
	}
}

func TestGitHubAPIBackfillEnrichesRepository(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	service := githubingest.New(f.pool)
	repository, err := service.PutRepository(t.Context(), f.tenant, githubingest.RepositoryInput{Provider: "github", ExternalID: "42", Owner: "acme", Name: "lumni"})
	if err != nil {
		t.Fatal(err)
	}
	repositoryID := repository["id"].(string)
	if _, err := f.pool.Exec(t.Context(), `UPDATE repositories SET updated_at=clock_timestamp()-interval '10 minutes' WHERE tenant_id=$1 AND id=$2`, f.tenant, repositoryID); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: githubRoundTrip(func(request *http.Request) (*http.Response, error) {
		body := `[]`
		switch request.URL.Path {
		case "/repos/acme/lumni/pulls":
			body = `[{"number":7,"state":"open","title":"P1","head":{"sha":"head1"},"base":{"sha":"base1"}}]`
		case "/repos/acme/lumni/commits":
			body = `[{"sha":"commit1","parents":[{"sha":"parent1"}],"commit":{"message":"coordination","author":{"date":"2026-08-28T10:00:00Z"}}}]`
		case "/repos/acme/lumni/commits/commit1":
			body = `{"sha":"commit1","files":[{"filename":"internal/api.go","status":"modified","sha":"blob1"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	backfiller := githubingest.NewBackfiller(f.pool, client, slog.New(slog.NewTextHandler(io.Discard, nil)), "token", "https://api.example")
	if err := backfiller.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	var pulls, commits int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pull_requests WHERE tenant_id=$1 AND repository_id=$2`, f.tenant, repositoryID).Scan(&pulls); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM git_commits WHERE tenant_id=$1 AND repository_id=$2`, f.tenant, repositoryID).Scan(&commits); err != nil {
		t.Fatal(err)
	}
	fileID := resource.CanonicalID(protocol.ResourceRef{Namespace: "github:" + repositoryID, Type: "file", ExternalID: "internal/api.go"})
	var version, source string
	if err := f.pool.QueryRow(t.Context(), `SELECT current_version,version_source FROM resources WHERE tenant_id=$1 AND id=$2`, f.tenant, fileID).Scan(&version, &source); err != nil {
		t.Fatal(err)
	}
	if pulls != 1 || commits != 1 || version != "blob1" || source != "git_blob" {
		t.Fatalf("backfill mismatch: pulls=%d commits=%d version=%s source=%s", pulls, commits, version, source)
	}
}
