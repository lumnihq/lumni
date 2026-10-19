package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
)

// This endpoint has only query scope and is separate from action interception.
func (s *Server) queryMCP() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		tenantID := tenant(r)
		server := mcp.NewServer(&mcp.Implementation{Name: "lumni-query", Version: "0.2.0"}, nil)
		queries := map[string]string{
			"lumni_recent_findings":       `SELECT to_jsonb(i) FROM incidents i WHERE tenant_id=$1 AND $2::text=$2::text ORDER BY created_at DESC LIMIT 50`,
			"lumni_explain_action":        `SELECT jsonb_build_object('action_id',a.id,'status',a.status,'operation',a.operation,'decisions',COALESCE((SELECT jsonb_agg(to_jsonb(d)-'snapshot') FROM decisions d WHERE d.tenant_id=a.tenant_id AND d.action_id=a.id),'[]'),'outcome',(SELECT to_jsonb(o)-'claim_token'-'claim_until' FROM outcomes o WHERE o.tenant_id=a.tenant_id AND o.action_id=a.id)) FROM actions a WHERE tenant_id=$1 AND id=$2`,
			"lumni_explain_incident":      `SELECT to_jsonb(i) FROM incidents i WHERE tenant_id=$1 AND id=$2`,
			"lumni_get_resource_activity": `SELECT jsonb_build_object('id',a.id,'operation',a.operation,'status',a.status,'work_id',a.work_id,'requested_at',a.requested_at) FROM actions a JOIN action_resources ar ON ar.tenant_id=a.tenant_id AND ar.action_id=a.id WHERE a.tenant_id=$1 AND ar.resource_id=$2 ORDER BY a.requested_at DESC LIMIT 100`,
			"lumni_get_outcome":           `SELECT to_jsonb(o)-'claim_token'-'claim_until' FROM outcomes o WHERE tenant_id=$1 AND id=$2`,
			"lumni_get_conflicts":         `SELECT to_jsonb(i) FROM incidents i WHERE tenant_id=$1 AND kind LIKE '%CONFLICT%' AND ($2='' OR work_id=$2) ORDER BY created_at DESC LIMIT 100`,
			"lumni_replay_incident":       `SELECT jsonb_build_object('mode','EVIDENCE_ONLY','incident',to_jsonb(i),'journal',COALESCE((SELECT jsonb_agg(to_jsonb(j) ORDER BY sequence) FROM(SELECT * FROM work_journal WHERE tenant_id=i.tenant_id AND work_id=i.work_id ORDER BY sequence LIMIT 500)j),'[]')) FROM incidents i WHERE tenant_id=$1 AND id=$2`,
		}
		type input struct {
			ID string `json:"id,omitempty" jsonschema:"Entity id (optional for recent findings and conflicts)"`
		}
		type output struct {
			Items []json.RawMessage `json:"items"`
		}
		for name, sql := range queries {
			mcp.AddTool(server, &mcp.Tool{Name: name, Description: "Read tenant-scoped Lumni correctness evidence. This tool never executes actions.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
				out := output{Items: []json.RawMessage{}}
				if in.ID == "" && name != "lumni_recent_findings" && name != "lumni_get_conflicts" {
					return nil, out, errors.New("id is required")
				}
				rows, err := s.pool.Query(ctx, sql, tenantID, in.ID)
				if err != nil {
					return nil, out, err
				}
				defer rows.Close()
				for rows.Next() {
					var b []byte
					if err = rows.Scan(&b); err != nil {
						return nil, out, err
					}
					out.Items = append(out.Items, b)
				}
				return nil, out, rows.Err()
			})
		}
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}
