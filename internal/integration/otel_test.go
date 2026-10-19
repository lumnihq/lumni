//go:build integration

package integration

import (
	"testing"
	"time"

	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc/metadata"

	"lumni/internal/auth"
	"lumni/internal/otel"
	"lumni/pkg/protocol"
)

func TestOTLPDeduplicatesAndCorrelatesSpan(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	authService := auth.New(f.pool)
	key, err := authService.Create(t.Context(), f.tenant, protocol.APIKeyCreateInput{Name: "otel", Scopes: []string{"ingest"}})
	if err != nil {
		t.Fatal(err)
	}
	receiver := otel.New(f.pool, authService)
	stringValue := func(key, value string) *commonv1.KeyValue {
		return &commonv1.KeyValue{Key: key, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}}}
	}
	intValue := func(key string, value int64) *commonv1.KeyValue {
		return &commonv1.KeyValue{Key: key, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: value}}}
	}
	now := time.Now().UTC()
	request := &collectortracev1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{stringValue("lumni.runtime", "integration")}},
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
			TraceId: []byte("0123456789abcdef"), SpanId: []byte("span-001"), Name: "agent tool",
			StartTimeUnixNano: uint64(now.UnixNano()), EndTimeUnixNano: uint64(now.Add(time.Millisecond).UnixNano()),
			Attributes: []*commonv1.KeyValue{
				stringValue("lumni.actor.id", f.actor), stringValue("lumni.work.id", f.work),
				stringValue("lumni.action.id", "otel-action"), stringValue("lumni.operation", "subscription.read"),
				stringValue("gen_ai.provider.name", "example"), stringValue("gen_ai.request.model", "model-1"),
				intValue("lumni.cost.amount_micros_usd", 250), intValue("gen_ai.usage.input_tokens", 10),
			},
		}}}},
	}}}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+key.Secret))
	if _, err := receiver.Export(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Export(ctx, request); err != nil {
		t.Fatal(err)
	}
	var spans, actions, costs int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM otel_spans WHERE tenant_id=$1`, f.tenant).Scan(&spans); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM actions WHERE tenant_id=$1 AND id='otel-action'`, f.tenant).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM costs WHERE tenant_id=$1 AND action_id='otel-action'`, f.tenant).Scan(&costs); err != nil {
		t.Fatal(err)
	}
	if spans != 1 || actions != 1 || costs != 1 {
		t.Fatalf("deduplication/correlation failed: spans=%d actions=%d costs=%d", spans, actions, costs)
	}
}
