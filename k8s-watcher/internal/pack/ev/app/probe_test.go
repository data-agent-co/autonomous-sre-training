package app_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/app"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
)

// timedSource delivers its events, reports how long handling all of them
// took, then waits for shutdown.
type timedSource struct {
	events []*event.Event
	took   chan time.Duration
}

func (s timedSource) Run(ctx context.Context, handle app.Handler) error {
	start := time.Now()
	for _, e := range s.events {
		handle(ctx, e)
	}
	s.took <- time.Since(start)
	<-ctx.Done()
	return nil
}

// TestRun_SlowPrometheusDoesNotStallPipeline floods the pipeline with probe
// failures while Prometheus never answers. Each probe lookup waits for the
// lookup's 2s timeout; none of that may hold up the events behind them.
func TestRun_SlowPrometheusDoesNotStallPipeline(t *testing.T) {
	release := make(chan struct{})
	prom := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer prom.Close()
	defer close(release)

	const probeFailures = 6
	now := time.Now()
	var events []*event.Event
	for i := range probeFailures {
		events = append(events, &event.Event{
			InvolvedNamespace: "shop",
			InvolvedName:      fmt.Sprintf("checkout-%d", i),
			InvolvedKind:      "Pod",
			InvolvedUID:       fmt.Sprintf("uid-probe-%d", i),
			Reason:            "Unhealthy",
			Note:              `Liveness probe failed: Get "http://10.0.0.1:8080/healthz": context deadline exceeded`,
			Type:              "Warning",
			FirstSeen:         now,
			LastSeen:          now,
			Count:             1,
		})
	}
	events = append(events, &event.Event{
		InvolvedNamespace: "shop",
		InvolvedName:      "cart-7d9f",
		InvolvedKind:      "Pod",
		InvolvedUID:       "uid-oom",
		Reason:            "OOMKilled",
		Note:              "Memory limit exceeded",
		Type:              "Warning",
		FirstSeen:         now,
		LastSeen:          now,
		Count:             1,
	})
	source := timedSource{events: events, took: make(chan time.Duration, 1)}

	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.Options{
			Source:                 source,
			DryRun:                 true,
			HealthAddr:             "127.0.0.1:0",
			ProbeEnrichmentEnabled: true,
			PrometheusURL:          prom.URL,
			Log:                    slog.New(slog.NewJSONHandler(logs, nil)),
		})
	}()

	select {
	case took := <-source.took:
		if took > time.Second {
			t.Fatalf("handling %d events took %s; probe lookups block the pipeline", len(events), took)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("handling %d events took more than 3s; probe lookups block the pipeline", len(events))
	}
	// Well inside the 2s lookup timeout, so it cannot have waited for one.
	oomDeadline := time.Now().Add(time.Second)
	for !strings.Contains(logs.String(), `"spec_name":"shop/cart-7d9f"`) {
		if time.Now().After(oomDeadline) {
			t.Fatalf("the OOMKilled Result waited behind the probe lookups; logs:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The probe failures are still reported, once their lookups time out.
	deadline := time.Now().Add(10 * time.Second)
	for i := range probeFailures {
		want := fmt.Sprintf(`"spec_name":"shop/checkout-%d"`, i)
		for !strings.Contains(logs.String(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("no Result for checkout-%d; logs:\n%s", i, logs.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}
