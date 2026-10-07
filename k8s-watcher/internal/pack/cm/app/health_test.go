package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/app"
)

// syncingSource is a Source whose initial sync finishes only when the test
// says so, like an informer still running its first list.
type syncingSource struct{ synced atomic.Bool }

func (s *syncingSource) Run(ctx context.Context, _ app.Handler) error {
	<-ctx.Done()
	return nil
}

func (s *syncingSource) HasSynced() bool { return s.synced.Load() }

// freeAddr returns a loopback address with a port that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// status GETs path on addr, retrying until the server answers.
func status(t *testing.T, addr, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + path)
		if err == nil {
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRun_ReadyOnlyAfterSourceSynced(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := freeAddr(t)
	src := &syncingSource{}

	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.Options{
			Source:     src,
			DryRun:     true,
			HealthAddr: addr,
			Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	if got := status(t, addr, "/healthz"); got != http.StatusOK {
		t.Fatalf("/healthz while syncing = %d, want 200 (the process is alive)", got)
	}
	if got := status(t, addr, "/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz while syncing = %d, want 503", got)
	}
	src.synced.Store(true)
	if got := status(t, addr, "/readyz"); got != http.StatusOK {
		t.Fatalf("/readyz after sync = %d, want 200", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}
