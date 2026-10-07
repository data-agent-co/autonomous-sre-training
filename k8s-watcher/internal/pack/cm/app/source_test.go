package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/app"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
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

// fakeSource hands the pipeline a fixed list of mutations, then waits for
// shutdown like a real informer would.
type fakeSource struct{ mutations []*mutation.Mutation }

func (f fakeSource) Run(ctx context.Context, handle app.Handler) error {
	for _, m := range f.mutations {
		handle(ctx, m)
	}
	<-ctx.Done()
	return nil
}

func TestRun_FakeSourceReachesDryRunEmitter(t *testing.T) {
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	source := fakeSource{mutations: []*mutation.Mutation{
		{
			// Not labelled critical: the filter must drop it.
			Kind: mutation.KindConfigMap, Op: mutation.OpAdd,
			Namespace: "shop", Name: "ignored", UID: "uid-0",
		},
		{
			Kind: mutation.KindConfigMap, Op: mutation.OpUpdate,
			Namespace: "shop", Name: "checkout-config", UID: "uid-1",
			Labels:     map[string]string{"k8sgpt-detection-pack.io/critical": "true"},
			Data:       map[string]string{"timeout": "5:0123456789abcdef"},
			PrevData:   map[string]string{"timeout": "5:fedcba9876543210"},
			ObservedAt: time.Now(),
		},
	}}

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
	out := logs.String()
	if !strings.Contains(out, `"spec_name":"shop/checkout-config"`) {
		t.Fatalf("dry-run emit does not name the labelled ConfigMap; logs:\n%s", out)
	}
	if strings.Contains(out, "shop/ignored") {
		t.Fatalf("unlabelled ConfigMap reached the emitter; logs:\n%s", out)
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
