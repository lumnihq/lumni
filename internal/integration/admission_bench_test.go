//go:build integration

package integration

import (
	"fmt"
	"github.com/lumnihq/lumni/pkg/protocol"
	"sort"
	"testing"
	"time"
)

func BenchmarkAdmission(b *testing.B) {
	f := setup(b, protocol.ModeEnforce)
	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in := input(f, "fixture.update", fmt.Sprintf("benchmark-%d", i), "")
		start := time.Now()
		d, err := f.kernel.Check(b.Context(), f.tenant, in)
		latencies = append(latencies, time.Since(start))
		if err != nil || d.Decision != protocol.DecisionAllow {
			b.Fatalf("admission: %+v %v", d, err)
		}
	}
	b.StopTimer()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) > 0 {
		b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100])/float64(time.Millisecond), "p95-ms")
	}
}
