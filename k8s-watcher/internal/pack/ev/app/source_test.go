package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/app"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
)

// syncBuffer lets the test read log output while the pipeline goroutines
// are still writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeSource hands the pipeline a fixed list of events, then waits for
// shutdown like a real informer would.
type fakeSource struct{ events []*event.Event }

func (f fakeSource) Run(ctx context.Context, handle app.Handler) error {
	for _, e := range f.events {
		handle(ctx, e)
	}
	<-ctx.Done()
	return nil
}

func TestRun_FakeSourceReachesDryRunEmitter(t *testing.T) {
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Now()
	source := fakeSource{events: []*event.Event{{
		InvolvedNamespace: "shop",
		InvolvedName:      "cart-7d9f",
		InvolvedKind:      "Pod",
		InvolvedUID:       "uid-1",
		Reason:            "OOMKilled",
		Note:              "Memory limit exceeded",
		Type:              "Warning",
		FirstSeen:         now,
		LastSeen:          now,
		Count:             1,
	}}}

	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.Options{
			Source:     source,
			DryRun:     true,
			HealthAddr: "127.0.0.1:0",
			Log:        slog.New(slog.NewJSONHandler(logs, nil)),
		})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), `"msg":"dry-run emit"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no dry-run emit within 5s; logs:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), `"spec_name":"shop/cart-7d9f"`) {
		t.Fatalf("dry-run emit does not name the involved pod; logs:\n%s", logs.String())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestRun_RequiresSource(t *testing.T) {
	err := app.Run(context.Background(), app.Options{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "Source is required") {
		t.Fatalf("want a missing-Source error, got %v", err)
	}
}
