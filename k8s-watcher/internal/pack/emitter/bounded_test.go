// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package emitter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/k8sgpt/v1alpha1"
)

// quietLog is a logger that swallows output so test logs stay clean.
func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeEmitter is a controllable upstream. emitDelay simulates apiserver
// latency; failNext returns an error from the next call.
type fakeEmitter struct {
	calls atomic.Int64
	delay time.Duration

	failMu sync.Mutex
	failN  int
}

func (f *fakeEmitter) Emit(ctx context.Context, _ *corev1alpha1.Result) error {
	f.calls.Add(1)
	f.failMu.Lock()
	shouldFail := f.failN > 0
	if shouldFail {
		f.failN--
	}
	f.failMu.Unlock()
	if shouldFail {
		return errors.New("upstream failure")
	}
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(f.delay):
		}
	}
	return nil
}

// counterValue reads the current count for a CounterVec label set.
func counterValue(t *testing.T, c *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	m, err := c.GetMetricWithLabelValues(labels...)
	require.NoError(t, err)
	pb := &dto.Metric{}
	require.NoError(t, m.Write(pb))
	return pb.Counter.GetValue()
}

// histSampleCount reads the current sample count of a Histogram.
func histSampleCount(t *testing.T, h prometheus.Histogram) uint64 {
	t.Helper()
	pb := &dto.Metric{}
	require.NoError(t, h.(prometheus.Collector).(prometheus.Metric).Write(pb))
	return pb.Histogram.GetSampleCount()
}

// laneConfigs are valid Configs with each pipeline's default values (see
// withDefaults in ev/app and cm/app), by component.
var laneConfigs = map[string]Config{
	"events-watcher":    {QueueDepth: 1000, Workers: 4, QPS: 50, Burst: 100, DropPolicy: DropOldest},
	"configmap-watcher": {QueueDepth: 100, Workers: 1, QPS: 20, Burst: 50, DropPolicy: DropOldest},
}

// startBlocked starts be with a context the test cancels on cleanup, so a
// worker blocked in a slow upstream returns at once instead of after the 5s
// emit timeout.
func startBlocked(t *testing.T, be *BoundedEmitter) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = be.Shutdown(context.Background())
	})
	be.Start(ctx)
}

func newResult(name string) *corev1alpha1.Result {
	return &corev1alpha1.Result{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test"},
	}
}

func TestBoundedEmitter_DropNewest_DropsWhenQueueFull(t *testing.T) {
	// Block upstream so the queue fills.
	fake := &fakeEmitter{delay: 1 * time.Hour}
	reg := prometheus.NewRegistry()
	be, err := NewBounded(fake, Config{
		QueueDepth: 2,
		Workers:    1,
		QPS:        100,
		Burst:      100,
		DropPolicy: DropNewest,
	}, "events-watcher", reg, quietLog())
	require.NoError(t, err)
	startBlocked(t, be)

	// Worker picks up first item and blocks in upstream.Emit. So queue
	// can hold up to QueueDepth more items before drops start.
	require.NoError(t, be.Emit(context.Background(), newResult("a")))
	// Wait until the worker has dequeued 'a' and sits in the blocked Emit
	// call; the next QueueDepth items are pure queue fillers.
	require.Eventually(t, func() bool { return fake.calls.Load() == 1 },
		2*time.Second, time.Millisecond, "the worker should take 'a' and block in upstream")

	require.NoError(t, be.Emit(context.Background(), newResult("b")))
	require.NoError(t, be.Emit(context.Background(), newResult("c")))

	// Queue is full now; further Emits with DropNewest must return ErrDropped.
	for i := 0; i < 5; i++ {
		err := be.Emit(context.Background(), newResult(fmt.Sprintf("d%d", i)))
		require.ErrorIs(t, err, ErrDropped, "iteration %d", i)
	}

	require.Equal(t, float64(5), counterValue(t, be.metrics.dropped, "queue_full"))
}

func TestBoundedEmitter_DropOldest_PreservesNewItems(t *testing.T) {
	// Same blocking-upstream setup as drop-newest; expect new items to win.
	fake := &fakeEmitter{delay: 1 * time.Hour}
	reg := prometheus.NewRegistry()
	be, err := NewBounded(fake, Config{
		QueueDepth: 2,
		Workers:    1,
		QPS:        100,
		Burst:      100,
		DropPolicy: DropOldest,
	}, "events-watcher", reg, quietLog())
	require.NoError(t, err)
	startBlocked(t, be)

	// Saturate the worker and queue.
	require.NoError(t, be.Emit(context.Background(), newResult("a")))
	require.Eventually(t, func() bool { return fake.calls.Load() == 1 },
		2*time.Second, time.Millisecond, "the worker should take 'a' and block in upstream")
	require.NoError(t, be.Emit(context.Background(), newResult("b")))
	require.NoError(t, be.Emit(context.Background(), newResult("c")))

	// Drop-oldest: subsequent Emits succeed but evict older queued items.
	// Returns nil (the new item was accepted), but the dropped counter
	// increments per evicted item.
	for i := 0; i < 5; i++ {
		require.NoError(t, be.Emit(context.Background(), newResult(fmt.Sprintf("d%d", i))))
	}

	require.Equal(t, float64(5), counterValue(t, be.metrics.dropped, "queue_full"))
}

func TestBoundedEmitter_EmitProcessesResults(t *testing.T) {
	fake := &fakeEmitter{}
	reg := prometheus.NewRegistry()
	be, err := NewBounded(fake, Config{
		QueueDepth: 10,
		Workers:    2,
		QPS:        1000,
		Burst:      1000,
		DropPolicy: DropOldest,
	}, "events-watcher", reg, quietLog())
	require.NoError(t, err)
	t.Cleanup(func() { _ = be.Shutdown(context.Background()) })

	be.Start(context.Background())

	for i := 0; i < 5; i++ {
		require.NoError(t, be.Emit(context.Background(), newResult(fmt.Sprintf("r%d", i))))
	}

	// Workers should drain quickly with no upstream latency.
	require.Eventually(t, func() bool { return fake.calls.Load() == 5 },
		2*time.Second, 10*time.Millisecond, "upstream should receive all 5 results")
	require.Equal(t, float64(0), counterValue(t, be.metrics.dropped, "queue_full"))
}

func TestBoundedEmitter_LaneDefaultsProcessResults(t *testing.T) {
	for component, cfg := range laneConfigs {
		t.Run(component, func(t *testing.T) {
			fake := &fakeEmitter{}
			be, err := NewBounded(fake, cfg, component, prometheus.NewRegistry(), quietLog())
			require.NoError(t, err)
			t.Cleanup(func() { _ = be.Shutdown(context.Background()) })

			be.Start(context.Background())

			for i := 0; i < 5; i++ {
				require.NoError(t, be.Emit(context.Background(), newResult(fmt.Sprintf("r%d", i))))
			}
			require.Eventually(t, func() bool { return fake.calls.Load() == 5 },
				2*time.Second, 10*time.Millisecond)
			require.Equal(t, float64(0), counterValue(t, be.metrics.dropped, "queue_full"))
		})
	}
}

func TestBoundedEmitter_UpstreamFailureLoggedButDoesNotRequeue(t *testing.T) {
	// Two failures then success. Each item is attempted exactly once.
	fake := &fakeEmitter{}
	fake.failMu.Lock()
	fake.failN = 2
	fake.failMu.Unlock()

	reg := prometheus.NewRegistry()
	be, err := NewBounded(fake, Config{
		QueueDepth: 10,
		Workers:    1,
		QPS:        1000,
		Burst:      1000,
		DropPolicy: DropOldest,
	}, "events-watcher", reg, quietLog())
	require.NoError(t, err)
	t.Cleanup(func() { _ = be.Shutdown(context.Background()) })

	be.Start(context.Background())

	for i := 0; i < 3; i++ {
		require.NoError(t, be.Emit(context.Background(), newResult(fmt.Sprintf("r%d", i))))
	}

	require.Eventually(t, func() bool { return fake.calls.Load() == 3 },
		2*time.Second, 10*time.Millisecond, "all three results attempted exactly once")
	// No re-enqueues means no drops either.
	require.Equal(t, float64(0), counterValue(t, be.metrics.dropped, "queue_full"))
}

func TestBoundedEmitter_EmitAfterShutdown_ReturnsErrShutdown(t *testing.T) {
	fake := &fakeEmitter{}
	reg := prometheus.NewRegistry()
	be, err := NewBounded(fake, Config{
		QueueDepth: 10,
		Workers:    1,
		QPS:        1000,
		Burst:      1000,
		DropPolicy: DropOldest,
	}, "events-watcher", reg, quietLog())
	require.NoError(t, err)

	be.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	require.NoError(t, be.Shutdown(ctx))

	err = be.Emit(context.Background(), newResult("late"))
	require.ErrorIs(t, err, ErrShutdown)
}

// TestBoundedEmitter_ShutdownDrainsInflight checks that Shutdown returns only
// after the writes already in progress have finished.
//
// It used to call Shutdown right after the Emits and expect at least one
// write to have finished, but nothing made a worker take a Result first:
// with the stop channel closed and the queue ready, each worker's select
// picks one at random, so now and then every worker exited without writing
// anything. The test now waits until a write is in progress.
func TestBoundedEmitter_ShutdownDrainsInflight(t *testing.T) {
	// Modest delay so a write is still in progress when Shutdown starts.
	fake := &fakeEmitter{delay: 30 * time.Millisecond}
	reg := prometheus.NewRegistry()
	be, err := NewBounded(fake, Config{
		QueueDepth: 100,
		Workers:    4,
		QPS:        1000,
		Burst:      1000,
		DropPolicy: DropOldest,
	}, "events-watcher", reg, quietLog())
	require.NoError(t, err)

	be.Start(context.Background())

	for i := 0; i < 10; i++ {
		require.NoError(t, be.Emit(context.Background(), newResult(fmt.Sprintf("r%d", i))))
	}
	require.Eventually(t, func() bool { return fake.calls.Load() >= 1 },
		2*time.Second, time.Millisecond, "a worker should start writing")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, be.Shutdown(ctx))

	// Workers stopped. Every write that started finished and was recorded
	// in the emit-duration histogram before Shutdown returned.
	started := fake.calls.Load()
	require.GreaterOrEqual(t, started, int64(1))
	require.Equal(t, uint64(started), histSampleCount(t, be.metrics.emitDuration))
}

func TestBoundedEmitter_RateLimitPaces(t *testing.T) {
	// At QPS=10, sending 5 items should take at least ~400ms total (first
	// item burns the burst budget, subsequent ones wait per the limiter).
	fake := &fakeEmitter{}
	reg := prometheus.NewRegistry()
	be, err := NewBounded(fake, Config{
		QueueDepth: 100,
		Workers:    1, // single worker so the rate limit is the bottleneck
		QPS:        10,
		Burst:      1, // burst=1 so each item must wait a token
		DropPolicy: DropOldest,
	}, "events-watcher", reg, quietLog())
	require.NoError(t, err)
	t.Cleanup(func() { _ = be.Shutdown(context.Background()) })

	be.Start(context.Background())

	start := time.Now()
	for i := 0; i < 5; i++ {
		require.NoError(t, be.Emit(context.Background(), newResult(fmt.Sprintf("r%d", i))))
	}
	require.Eventually(t, func() bool { return fake.calls.Load() == 5 },
		2*time.Second, 10*time.Millisecond)
	elapsed := time.Since(start)
	// 5 items at QPS=10 with Burst=1: first item ~immediate, next 4 wait
	// ~100ms each = ~400ms total. Allow some slack for scheduling.
	require.GreaterOrEqual(t, elapsed, 300*time.Millisecond,
		"rate limit should have paced the drain")
}

func TestBoundedEmitter_ConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"zero queue", Config{Workers: 1, QPS: 1, Burst: 1, DropPolicy: DropOldest}, "queueDepth"},
		{"zero workers", Config{QueueDepth: 1, QPS: 1, Burst: 1, DropPolicy: DropOldest}, "workers"},
		{"zero qps", Config{QueueDepth: 1, Workers: 1, Burst: 1, DropPolicy: DropOldest}, "qps"},
		{"zero burst", Config{QueueDepth: 1, Workers: 1, QPS: 1, DropPolicy: DropOldest}, "burst"},
		{"bad policy", Config{QueueDepth: 1, Workers: 1, QPS: 1, Burst: 1, DropPolicy: "drop-random"}, "dropPolicy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewBounded(&fakeEmitter{}, tc.cfg, "events-watcher", prometheus.NewRegistry(), quietLog())
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestBoundedEmitter_NilUpstreamRejected(t *testing.T) {
	_, err := NewBounded(nil, laneConfigs["events-watcher"], "events-watcher", prometheus.NewRegistry(), quietLog())
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "upstream"))
}

func TestBoundedEmitter_MetricsExposedOnRegistry(t *testing.T) {
	// Sanity check: every metric family shows up on the registry the caller
	// passed in, with the caller's component label, so they land on that
	// pipeline's /metrics endpoint (see the metrics package).
	for component, cfg := range laneConfigs {
		t.Run(component, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			be, err := NewBounded(&fakeEmitter{}, cfg, component, reg, quietLog())
			require.NoError(t, err)
			t.Cleanup(func() { _ = be.Shutdown(context.Background()) })

			mfs, err := reg.Gather()
			require.NoError(t, err)
			var names []string
			for _, mf := range mfs {
				names = append(names, mf.GetName())
				for _, m := range mf.GetMetric() {
					require.Equal(t, component, labelValue(m, "component"), mf.GetName())
				}
			}
			for _, want := range []string{
				"k8sgpt_pack_emitter_dropped_total",
				"k8sgpt_pack_emitter_emit_duration_seconds",
				"k8sgpt_pack_emitter_queue_depth",
				"k8sgpt_pack_results_emitted_total",
				"k8sgpt_pack_emitter_suppressed_total",
			} {
				require.Contains(t, names, want)
			}
		})
	}
}

// labelValue returns the value of m's label name, or "" without one.
func labelValue(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}
