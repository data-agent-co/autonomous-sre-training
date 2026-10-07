package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/filter"
)

func TestProbeQueue_SubmitRefusesWhenFullInsteadOfBlocking(t *testing.T) {
	release := make(chan struct{})
	var processed atomic.Int32
	q := newProbeQueue(2, func(context.Context, *event.Event, filter.Decision) {
		<-release
		processed.Add(1)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.start(ctx, 1)

	e, d := &event.Event{Reason: "Unhealthy"}, filter.Decision{SignalType: "probe_failure"}
	require.True(t, q.submit(e, d))
	require.Eventually(t, func() bool { return len(q.jobs) == 0 }, time.Second, 5*time.Millisecond,
		"the worker takes the first event and holds it")
	require.True(t, q.submit(e, d))
	require.True(t, q.submit(e, d))
	require.False(t, q.submit(e, d), "queue of 2 is full")

	close(release)
	require.Eventually(t, func() bool { return processed.Load() == 3 }, time.Second, 5*time.Millisecond)
	cancel()
	q.wait()
}

func TestProbeQueue_WorkersStopWithContext(t *testing.T) {
	q := newProbeQueue(1, func(context.Context, *event.Event, filter.Decision) {
		t.Error("nothing was submitted")
	})
	ctx, cancel := context.WithCancel(context.Background())
	q.start(ctx, 3)
	cancel()

	done := make(chan struct{})
	go func() {
		q.wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("workers still running after cancel")
	}
}
