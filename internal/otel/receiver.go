package otel

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"lumni/internal/auth"
)

type Receiver struct {
	collectortracev1.UnimplementedTraceServiceServer
	pool *pgxpool.Pool
	auth *auth.Service
}

func New(pool *pgxpool.Pool, authService *auth.Service) *Receiver {
	return &Receiver{pool: pool, auth: authService}
}

func (r *Receiver) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", r.httpTraces)
	return mux
}

func (r *Receiver) httpTraces(w http.ResponseWriter, req *http.Request) {
	p, err := r.httpPrincipal(req)
	if err != nil || (!hasScope(p.Scopes, "ingest") && !hasScope(p.Scopes, "admin")) {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 16<<20))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var input collectortracev1.ExportTraceServiceRequest
	if strings.Contains(req.Header.Get("Content-Type"), "json") {
		err = protojson.Unmarshal(body, &input)
	} else {
		err = proto.Unmarshal(body, &input)
	}
	if err != nil {
		http.Error(w, "invalid OTLP payload", http.StatusBadRequest)
		return
	}
	if err := r.ingest(req.Context(), p.TenantID, &input); err != nil {
		http.Error(w, "ingestion failed", http.StatusInternalServerError)
		return
	}
	output := &collectortracev1.ExportTraceServiceResponse{}
	if strings.Contains(req.Header.Get("Content-Type"), "json") {
		w.Header().Set("Content-Type", "application/json")
		data, _ := protojson.Marshal(output)
		_, _ = w.Write(data)
	} else {
		w.Header().Set("Content-Type", "application/x-protobuf")
		data, _ := proto.Marshal(output)
		_, _ = w.Write(data)
	}
}

func (r *Receiver) Export(ctx context.Context, input *collectortracev1.ExportTraceServiceRequest) (*collectortracev1.ExportTraceServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	header := ""
	if values := md.Get("authorization"); len(values) > 0 {
		header = values[0]
	}
	secret, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "bearer token required")
	}
	p, err := r.auth.Authenticate(ctx, secret)
	if err != nil || (!hasScope(p.Scopes, "ingest") && !hasScope(p.Scopes, "admin")) {
		return nil, status.Error(codes.PermissionDenied, "ingest scope required")
	}
	if err := r.ingest(ctx, p.TenantID, input); err != nil {
		return nil, status.Error(codes.Internal, "ingestion failed")
	}
	return &collectortracev1.ExportTraceServiceResponse{}, nil
}

func (r *Receiver) httpPrincipal(req *http.Request) (auth.Principal, error) {
	secret, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return auth.Principal{}, errors.New("missing bearer token")
	}
	return r.auth.Authenticate(req.Context(), secret)
}

func (r *Receiver) ingest(ctx context.Context, tenantID string, input *collectortracev1.ExportTraceServiceRequest) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, resourceSpans := range input.ResourceSpans {
		resourceAttrs := attributes(resourceSpans.Resource.GetAttributes())
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, span := range scopeSpans.Spans {
				attrs := clone(resourceAttrs)
				for key, value := range attributes(span.Attributes) {
					attrs[key] = value
				}
				encoded, _ := json.Marshal(attrs)
				start := time.Unix(0, int64(span.StartTimeUnixNano)).UTC()
				var end *time.Time
				if span.EndTimeUnixNano > 0 {
					value := time.Unix(0, int64(span.EndTimeUnixNano)).UTC()
					end = &value
				}
				traceID, spanID := hex.EncodeToString(span.TraceId), hex.EncodeToString(span.SpanId)
				tag, err := tx.Exec(ctx, `INSERT INTO otel_spans (tenant_id,trace_id,span_id,name,start_time,end_time,attributes)
					VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (tenant_id,trace_id,span_id) DO NOTHING`, tenantID, traceID, spanID, span.Name, start, end, encoded)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					continue
				}
				if err := r.correlate(ctx, tx, tenantID, span, attrs, start); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit(ctx)
}

func (r *Receiver) correlate(ctx context.Context, tx pgx.Tx, tenantID string, span *tracev1.Span, attrs map[string]any, observed time.Time) error {
	actorID := stringAttr(attrs, "lumni.actor.id")
	if actorID != "" {
		_, err := tx.Exec(ctx, `INSERT INTO actors (tenant_id,id,type,runtime,display_name,metadata)
			VALUES ($1,$2,'AGENT',$3,$2,'{}') ON CONFLICT (tenant_id,id) DO NOTHING`, tenantID, actorID, stringAttr(attrs, "lumni.runtime"))
		if err != nil {
			return err
		}
	}
	agentID := stringAttr(attrs, "lumni.agent.id")
	if agentID != "" && actorID != "" {
		_, err := tx.Exec(ctx, `INSERT INTO agents (tenant_id,id,actor_id,name,framework,model_provider,model,metadata)
			VALUES ($1,$2,$3,$2,$4,$5,$6,'{}') ON CONFLICT (tenant_id,id) DO NOTHING`, tenantID, agentID, actorID, stringAttr(attrs, "gen_ai.system"), stringAttr(attrs, "gen_ai.provider.name"), stringAttr(attrs, "gen_ai.request.model"))
		if err != nil {
			return err
		}
	}
	sessionID := stringAttr(attrs, "lumni.session.id")
	if sessionID != "" && actorID != "" {
		_, err := tx.Exec(ctx, `INSERT INTO sessions(tenant_id,id,agent_id,actor_id,runtime,status,metadata)VALUES($1,$2,NULLIF($3,''),$4,$5,'RUNNING','{}')ON CONFLICT(tenant_id,id)DO NOTHING`, tenantID, sessionID, agentID, actorID, stringAttr(attrs, "lumni.runtime"))
		if err != nil {
			return err
		}
	}
	workID := stringAttr(attrs, "lumni.work.id")
	if workID != "" && actorID != "" {
		goal := stringAttr(attrs, "lumni.work.goal")
		if goal == "" {
			goal = span.Name
		}
		_, err := tx.Exec(ctx, `INSERT INTO work(tenant_id,id,actor_id,agent_id,session_id,goal,status,metadata)VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,'RUNNING','{}')ON CONFLICT(tenant_id,id)DO NOTHING`, tenantID, workID, actorID, agentID, sessionID, goal)
		if err != nil {
			return err
		}
	}
	actionID := stringAttr(attrs, "lumni.action.id")
	operation := stringAttr(attrs, "lumni.operation")
	if actionID != "" && workID != "" && actorID != "" && operation != "" {
		hash := hex.EncodeToString(span.SpanId)
		_, err := tx.Exec(ctx, `INSERT INTO actions(tenant_id,id,work_id,actor_id,session_id,operation,tool,source,status,risk,arguments_hash,metadata)VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,'otel','PROPOSED','MEDIUM',$8,'{}')ON CONFLICT(tenant_id,id)DO NOTHING`, tenantID, actionID, workID, actorID, sessionID, operation, stringAttr(attrs, "gen_ai.tool.name"), hash)
		if err != nil {
			return err
		}
	}
	amount := intAttr(attrs, "lumni.cost.amount_micros_usd")
	if amount > 0 {
		_, err := tx.Exec(ctx, `INSERT INTO costs(tenant_id,actor_id,agent_id,session_id,work_id,action_id,provider,model,cost_type,input_units,output_units,amount_micros_usd,observed_at,metadata)VALUES($1,NULLIF($2,''),NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,NULLIF($8,''),'MODEL',$9,$10,$11,$12,'{}')`, tenantID, actorID, agentID, sessionID, workID, actionID, stringAttr(attrs, "gen_ai.provider.name"), stringAttr(attrs, "gen_ai.request.model"), intAttr(attrs, "gen_ai.usage.input_tokens"), intAttr(attrs, "gen_ai.usage.output_tokens"), amount, observed)
		if err != nil {
			return err
		}
	}
	return nil
}

func attributes(values []*commonv1.KeyValue) map[string]any {
	out := make(map[string]any, len(values))
	for _, item := range values {
		out[item.Key] = anyValue(item.Value)
	}
	return out
}

func anyValue(value *commonv1.AnyValue) any {
	if value == nil {
		return nil
	}
	switch v := value.Value.(type) {
	case *commonv1.AnyValue_StringValue:
		return v.StringValue
	case *commonv1.AnyValue_BoolValue:
		return v.BoolValue
	case *commonv1.AnyValue_IntValue:
		return v.IntValue
	case *commonv1.AnyValue_DoubleValue:
		return v.DoubleValue
	case *commonv1.AnyValue_BytesValue:
		return hex.EncodeToString(v.BytesValue)
	case *commonv1.AnyValue_ArrayValue:
		out := make([]any, 0, len(v.ArrayValue.Values))
		for _, item := range v.ArrayValue.Values {
			out = append(out, anyValue(item))
		}
		return out
	case *commonv1.AnyValue_KvlistValue:
		return attributes(v.KvlistValue.Values)
	default:
		return nil
	}
}

func clone(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func hasScope(scopes []string, scope string) bool {
	for _, v := range scopes {
		if v == scope {
			return true
		}
	}
	return false
}
func stringAttr(values map[string]any, key string) string {
	if v, ok := values[key]; ok {
		return fmt.Sprint(v)
	}
	return ""
}
func intAttr(values map[string]any, key string) int64 {
	switch v := values[key].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	default:
		return 0
	}
}
