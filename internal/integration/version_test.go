//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/lumnihq/lumni/internal/core"
	"github.com/lumnihq/lumni/internal/resource"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func TestLateAndIncomparableVersionsStayInHistory(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	service := core.New(f.pool)
	ref := protocol.ResourceRef{Type: "subscription", ExternalID: "versioned"}
	resourceID := resource.CanonicalID(ref)
	if _, err := service.PutResource(t.Context(), f.tenant, protocol.Resource{ResourceRef: ref}); err != nil {
		t.Fatal(err)
	}
	sequence10, sequence9, sequence11 := int64(10), int64(9), int64(11)
	now := time.Now().UTC()
	for _, update := range []protocol.ResourceVersionInput{
		{Version: "10", VersionSource: "etag", Sequence: &sequence10, ObservedAt: &now},
		{Version: "9", VersionSource: "etag", Sequence: &sequence9, ObservedAt: timePtr(now.Add(time.Second))},
		{Version: "commit-11", VersionSource: "git_sha", Sequence: &sequence11, ObservedAt: timePtr(now.Add(2 * time.Second))},
	} {
		if err := service.UpdateVersion(t.Context(), f.tenant, resourceID, update); err != nil {
			t.Fatal(err)
		}
	}
	current, err := service.GetResource(t.Context(), f.tenant, resourceID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentVersion == nil || *current.CurrentVersion != "10" || current.VersionSource == nil || *current.VersionSource != "etag" {
		t.Fatalf("incomparable version replaced authority: %+v", current)
	}
	var history, authoritative int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE authoritative) FROM resource_versions WHERE tenant_id=$1 AND resource_id=$2`, f.tenant, resourceID).Scan(&history, &authoritative); err != nil {
		t.Fatal(err)
	}
	if history != 3 || authoritative != 1 {
		t.Fatalf("history=%d authoritative=%d", history, authoritative)
	}
}

func timePtr(value time.Time) *time.Time { return &value }
