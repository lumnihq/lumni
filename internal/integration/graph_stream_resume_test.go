//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lumni/pkg/protocol"
)

func graphStreamBody(t *testing.T, c consoleClient, query, header string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("GET", "/v1/graph/stream?types=actor.created,action.checked"+query, nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+c.key)
	if header != "" {
		r.Header.Set("Last-Event-ID", header)
	}
	w := httptest.NewRecorder()
	c.server.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("stream: %d %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func graphStreamChanges(t *testing.T, body string) []map[string]any {
	t.Helper()
	changes := []map[string]any{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event["event_type"] != nil {
			changes = append(changes, event)
		}
	}
	return changes
}

func TestGraphStreamReconnectReplaysGapWithQueryOrHeader(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	c.call(t, "POST", "/v1/actors", map[string]any{"id": "before-gap", "type": "AGENT"}, 201)
	var cursor int64
	if err := c.f.pool.QueryRow(t.Context(), `SELECT max(sequence) FROM graph_changes WHERE tenant_id=$1`, c.f.tenant).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	c.call(t, "POST", "/v1/actors", map[string]any{"id": "during-gap", "type": "AGENT"}, 201)
	d := c.check(t, "gap-action")
	for _, tc := range []struct{ name, query, header string }{
		{"query without header", fmt.Sprintf("&cursor=%d", cursor), ""},
		{"header without query", "", fmt.Sprint(cursor)},
		{"query takes precedence", fmt.Sprintf("&cursor=%d", cursor), "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := graphStreamBody(t, c, tc.query, tc.header)
			changes := graphStreamChanges(t, body)
			if len(changes) != 2 || changes[0]["entity_id"] != "during-gap" || changes[1]["entity_id"] != d.ActionID || strings.Contains(body, "cursor_expired") {
				t.Fatalf("reconnect lost or repeated activity: %s", body)
			}
			for _, event := range changes {
				if event["sequence"].(float64) <= float64(cursor) {
					t.Fatal(event)
				}
			}
		})
	}
	if changes := graphStreamChanges(t, graphStreamBody(t, c, "", "")); len(changes) != 0 {
		t.Fatalf("new connections must start at head: %v", changes)
	}
}

func TestGraphStreamExpiredCursorReplaysFirstRetainedEvent(t *testing.T) {
	c := newConsoleClient(t, protocol.ModeShadow)
	var cursor int64
	if err := c.f.pool.QueryRow(t.Context(), `SELECT max(sequence) FROM graph_changes WHERE tenant_id=$1`, c.f.tenant).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	// Simulate worker retention, deleting this fixture's old events only.
	c.call(t, "POST", "/v1/actors", map[string]any{"id": "pruned-during-gap", "type": "AGENT"}, 201)
	if _, err := c.f.pool.Exec(t.Context(), `DELETE FROM graph_changes WHERE tenant_id=$1`, c.f.tenant); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first-retained", "second-retained"} {
		if _, err := c.f.pool.Exec(t.Context(), `INSERT INTO graph_changes(tenant_id,event_type,entity_type,entity_id,affected_lenses)VALUES($1,'actor.created','actor',$2,ARRAY['TRANSACTION'])`, c.f.tenant, id); err != nil {
			t.Fatal(err)
		}
	}
	body := graphStreamBody(t, c, fmt.Sprintf("&cursor=%d", cursor), "")
	changes := graphStreamChanges(t, body)
	if !strings.Contains(body, "event: reset") || len(changes) != 2 || changes[0]["entity_id"] != "first-retained" || changes[1]["entity_id"] != "second-retained" {
		t.Fatalf("expired cursor skipped retained activity: %s", body)
	}
	resetCursor := int64(changes[0]["sequence"].(float64)) - 1
	reconnected := graphStreamBody(t, c, fmt.Sprintf("&cursor=%d", resetCursor), "")
	if strings.Contains(reconnected, "event: reset") || len(graphStreamChanges(t, reconnected)) != 2 {
		t.Fatalf("reset position must be resumable without another reset: %s", reconnected)
	}
}
