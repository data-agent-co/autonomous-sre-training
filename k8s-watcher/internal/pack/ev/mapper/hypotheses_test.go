package mapper

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/filter"
)

// TestToResult_LeavesCatalogUnchanged guards the shared signalHypotheses
// table: building a Result must work on a copy, never write the baseline
// confidence into the table itself.
func TestToResult_LeavesCatalogUnchanged(t *testing.T) {
	m := New("k8sgpt-system")
	e := &event.Event{InvolvedKind: "Pod", InvolvedNamespace: "shop", InvolvedName: "cart-1", Reason: "OOMKilled", Note: "OOM"}
	d := filter.Decision{Allow: true, Category: "pod_lifecycle", SignalType: "oom_kill", Severity: "critical"}

	r := m.ToResult(e, d)

	for signal, hyps := range signalHypotheses {
		for i, h := range hyps {
			require.Zerof(t, h.Confidence, "signalHypotheses[%q][%d] was written to", signal, i)
		}
	}

	// The Result still carries the baseline on the leading hypothesis.
	var details struct {
		Hypotheses []hypothesis `json:"hypotheses"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.Spec.Details), &details))
	require.NotEmpty(t, details.Hypotheses)
	require.InDelta(t, 0.85, details.Hypotheses[0].Confidence, 1e-9)
	for _, h := range details.Hypotheses[1:] {
		require.Zero(t, h.Confidence, "only the leading hypothesis carries a confidence")
	}
}

// TestToResult_ConcurrentCatalogSignals maps catalog signals from several
// goroutines at once. Run with -race: the mapper is shared by the event
// pipeline and the probe workers.
func TestToResult_ConcurrentCatalogSignals(t *testing.T) {
	m := New("k8sgpt-system")
	d := filter.Decision{Allow: true, Category: "pod_lifecycle", SignalType: "back_off", Severity: "warning"}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := &event.Event{InvolvedKind: "Pod", InvolvedNamespace: "shop", InvolvedName: fmt.Sprintf("cart-%d", i), Reason: "BackOff"}
			for range 50 {
				_ = m.ToResult(e, d)
			}
		}()
	}
	wg.Wait()
}
