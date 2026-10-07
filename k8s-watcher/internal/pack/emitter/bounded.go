// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// BoundedEmitter wraps an existing Emitter with a bounded in-memory queue
// + worker pool + rate limit, so a storm (a failing node spamming events,
// dedup-defeating cardinality, a burst of ConfigMap edits) does not pile up
// apiserver writes, pile up goroutines, or OOM the pod.
//
// Drop-in behavior:
//
//	Emit() enqueues the Result and returns immediately. The actual
//	apiserver call happens later, on one of `workers` background
//	goroutines, paced by the configured token-bucket rate limit.
//
// Drop policy on full queue:
//
//	drop-oldest — pop the oldest queued item, push the new one. Preserves
//	              recency. The default for both pipelines.
//	drop-newest — refuse the new item, keep the queue as-is. Preserves
//	              the order in which Results were queued.
//
// All sentinel error returns from Emit() are recoverable from the caller's
// perspective: log + observe-via-metric, no retry. Retrying would just
// refill the queue we're trying to relieve.

package emitter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/util/flowcontrol"

	corev1alpha1 "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/k8sgpt/v1alpha1"
)

// ErrDropped is returned by Emit when the queue is full and the configured
// drop policy refused the new item. Callers should observe via the dropped
// counter rather than retrying.
var ErrDropped = errors.New("bounded emitter: dropped")

// ErrShutdown is returned by Emit after Shutdown has been called.
var ErrShutdown = errors.New("bounded emitter: shutdown")

// DropPolicy selects which side of the queue to drop from on overflow.
type DropPolicy string

const (
	DropOldest DropPolicy = "drop-oldest"
	DropNewest DropPolicy = "drop-newest"
)

// Config tunes the bounded behavior. Each pipeline's defaults are applied in
// its app.Options.withDefaults.
type Config struct {
	// QueueDepth is the maximum number of Results buffered between Emit()
	// and the worker pool. Must be > 0.
	QueueDepth int
	// Workers is the number of goroutines draining the queue. Must be > 0.
	Workers int
	// QPS is the steady-state apiserver write rate (Results/sec) allowed
	// across all workers. Must be > 0.
	QPS float32
	// Burst is the token bucket's burst capacity. Allows short spikes to
	// drain at >QPS without dropping. Must be >= Workers (otherwise some
	// workers will block waiting for the same bucket).
	Burst int
	// DropPolicy is how the queue handles overflow. See DropPolicy constants.
	DropPolicy DropPolicy
}

// validate returns nil if cfg is internally consistent.
func (c Config) validate() error {
	if c.QueueDepth <= 0 {
		return fmt.Errorf("queueDepth must be > 0, got %d", c.QueueDepth)
	}
	if c.Workers <= 0 {
		return fmt.Errorf("workers must be > 0, got %d", c.Workers)
	}
	if c.QPS <= 0 {
		return fmt.Errorf("qps must be > 0, got %f", c.QPS)
	}
	if c.Burst <= 0 {
		return fmt.Errorf("burst must be > 0, got %d", c.Burst)
	}
	if c.DropPolicy != DropOldest && c.DropPolicy != DropNewest {
		return fmt.Errorf("dropPolicy must be drop-oldest or drop-newest, got %q", c.DropPolicy)
	}
	return nil
}

// boundedMetrics owns the Prometheus collectors the bounded emitter
// exposes. Registered against the pipeline's own registry (see the metrics
// package) so they show up on its /metrics alongside
// k8sgpt_pack_component_info.
type boundedMetrics struct {
	dropped      *prometheus.CounterVec
	emitDuration prometheus.Histogram
	// emitted counts successful apiserver Result writes, labeled by the
	// Pack-specific dimensions extracted from the Result's labels. Lets the
	// dashboard answer "what did the watcher just detect?" without reading
	// Results from the apiserver.
	emitted *prometheus.CounterVec
	// suppressed counts no-change suppressions labeled the same way as
	// emitted. The aggregate dropped{reason="no_change"} counter answers
	// "how much are we suppressing?" but not "what are we suppressing?" —
	// this counter does the latter, so operators can see which signal_types
	// are getting suppressed most and decide whether the suppression rule
	// is too aggressive, the source is too chatty, or the mapper is
	// producing semantically-equal specs for observations that should look
	// different.
	suppressed *prometheus.CounterVec
}

func newBoundedMetrics(component string, reg prometheus.Registerer, queueLenFn func() float64) (*boundedMetrics, error) {
	dropped := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "k8sgpt_pack_emitter_dropped_total",
			Help:        "Total Results dropped by the bounded emitter, by reason: queue_full (the queue overflowed) or no_change (the Result in the cluster already said the same).",
			ConstLabels: prometheus.Labels{"component": component},
		},
		[]string{"reason"},
	)
	emitDuration := prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "k8sgpt_pack_emitter_emit_duration_seconds",
			Help:        "Latency of a single Result emit: the apiserver Get plus the Create or Update, if any. Excludes time spent waiting on the rate-limit token; that's the rate-limit-imposed pacing, not the apiserver's.",
			ConstLabels: prometheus.Labels{"component": component},
			Buckets:     prometheus.ExponentialBuckets(0.005, 2, 10), // 5ms .. ~2.5s
		},
	)
	queueDepth := prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name:        "k8sgpt_pack_emitter_queue_depth",
			Help:        "Number of Results currently buffered between Emit() and the apiserver writers. High sustained values indicate workers can't keep up with the producer.",
			ConstLabels: prometheus.Labels{"component": component},
		},
		queueLenFn,
	)
	emitted := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "k8sgpt_pack_results_emitted_total",
			Help:        "Total Results successfully written to the apiserver, labeled by signal_type/category/severity/target_namespace. target_namespace is the namespace of the detected workload, parsed from spec.name; \"cluster\" for cluster-scoped, \"unknown\" if absent. Lets dashboards break detection volume down by namespace.",
			ConstLabels: prometheus.Labels{"component": component},
		},
		[]string{"signal_type", "category", "severity", "target_namespace"},
	)
	suppressed := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "k8sgpt_pack_emitter_suppressed_total",
			Help:        "Total Results suppressed by the no-change gate, labeled by signal_type/category/severity/target_namespace. Mirrors the emitted counter so suppression ratio can be computed per (signal_type, namespace).",
			ConstLabels: prometheus.Labels{"component": component},
		},
		[]string{"signal_type", "category", "severity", "target_namespace"},
	)
	for _, c := range []prometheus.Collector{dropped, emitDuration, queueDepth, emitted, suppressed} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register metric: %w", err)
		}
	}
	// Pre-touch known drop reasons so the metric family is present (at
	// value 0) on the registry from boot — otherwise dashboards and rate()
	// queries can't tell "metric not scraped" from "no drops yet."
	dropped.WithLabelValues("queue_full").Add(0)
	// "no_change" reason — apiserver already had an equal Result. Kept
	// apart from queue_full so queue pressure can be read on its own.
	dropped.WithLabelValues("no_change").Add(0)
	// Pre-touch the emitted metric family for the same reason: dashboards
	// using `sum by(signal_type) (rate(...))` need at least one series
	// present at boot, otherwise the panel reads "No data" on a healthy
	// idle cluster.
	emitted.WithLabelValues("unknown", "unknown", "unknown", "unknown").Add(0)
	suppressed.WithLabelValues("unknown", "unknown", "unknown", "unknown").Add(0)
	return &boundedMetrics{
		dropped:      dropped,
		emitDuration: emitDuration,
		emitted:      emitted,
		suppressed:   suppressed,
	}, nil
}

// BoundedEmitter is a queued, rate-limited Emitter. Construct with
// NewBounded, call Start to spin up the worker pool, call Shutdown
// (typically from the pipeline's deferred cleanup) to stop it. Shutdown
// waits for the writes in progress; Results still queued are not written.
//
// Emit returns ErrDropped when the queue is full and the configured
// DropPolicy refused the new item. ErrShutdown after Shutdown has been
// called. Otherwise nil — note that "enqueued" does NOT mean "written
// to apiserver"; the apiserver write happens on a background worker and
// its outcome is observed via metrics, not the Emit return value.
type BoundedEmitter struct {
	upstream Emitter
	cfg      Config
	log      *slog.Logger
	metrics  *boundedMetrics

	queue    chan *corev1alpha1.Result
	rl       flowcontrol.RateLimiter
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopCh   chan struct{}
	stopped  bool
	mu       sync.Mutex
}

// NewBounded wraps `upstream` with bounded queue + worker pool + rate
// limit. Metrics are registered into reg under fixed names; component
// labels each series so multiple components can register against the
// same Prometheus instance without collision.
func NewBounded(upstream Emitter, cfg Config, component string, reg prometheus.Registerer, log *slog.Logger) (*BoundedEmitter, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if upstream == nil {
		return nil, fmt.Errorf("upstream emitter is required")
	}
	if log == nil {
		return nil, fmt.Errorf("logger is required")
	}
	be := &BoundedEmitter{
		upstream: upstream,
		cfg:      cfg,
		log:      log,
		queue:    make(chan *corev1alpha1.Result, cfg.QueueDepth),
		rl:       flowcontrol.NewTokenBucketRateLimiter(cfg.QPS, cfg.Burst),
		stopCh:   make(chan struct{}),
	}
	m, err := newBoundedMetrics(component, reg, func() float64 { return float64(len(be.queue)) })
	if err != nil {
		return nil, err
	}
	be.metrics = m
	return be, nil
}

// Emit enqueues the result for asynchronous apiserver write. Returns
// ErrDropped if the queue is full and the drop policy refused the
// item, ErrShutdown if Shutdown has been called. Non-blocking by
// construction — the slowest path is a single channel pop on
// drop-oldest contention.
func (b *BoundedEmitter) Emit(_ context.Context, r *corev1alpha1.Result) error {
	b.mu.Lock()
	stopped := b.stopped
	b.mu.Unlock()
	if stopped {
		return ErrShutdown
	}

	if b.cfg.DropPolicy == DropNewest {
		select {
		case b.queue <- r:
			return nil
		default:
			b.metrics.dropped.WithLabelValues("queue_full").Inc()
			return ErrDropped
		}
	}

	// drop-oldest: if the queue is full, pop the oldest item and push the
	// new one. Loop because we may race against a worker draining.
	for {
		select {
		case b.queue <- r:
			return nil
		default:
		}
		select {
		case <-b.queue:
			b.metrics.dropped.WithLabelValues("queue_full").Inc()
		default:
			// queue drained between full-check and pop attempt; loop
		}
	}
}

// Start launches the worker pool. Call once; calls after Shutdown are
// no-ops. Workers shut down when ctx is cancelled OR Shutdown is called.
func (b *BoundedEmitter) Start(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	for i := 0; i < b.cfg.Workers; i++ {
		b.wg.Add(1)
		go b.runWorker(ctx, i)
	}
}

func (b *BoundedEmitter) runWorker(ctx context.Context, id int) {
	defer b.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.stopCh:
			return
		case r := <-b.queue:
			if r == nil {
				continue
			}
			// Pace via the rate limit token bucket. Wait is ctx-aware so
			// graceful shutdown isn't blocked behind a long pacing wait.
			if err := b.rl.Wait(ctx); err != nil {
				return
			}
			start := time.Now()
			emitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := b.upstream.Emit(emitCtx, r)
			cancel()
			b.metrics.emitDuration.Observe(time.Since(start).Seconds())
			switch {
			case errors.Is(err, ErrNoChange):
				// Suppression gate: existing Result was equal. Count as a
				// 'no_change' drop, not an emit — keeps the emit-rate panel
				// honest (re-deliveries don't inflate it) and gives
				// operators a separate signal for "we're seeing the same
				// observation repeatedly with no underlying change."
				b.metrics.dropped.WithLabelValues("no_change").Inc()
				// Also increment the labeled suppressed counter so operators
				// can see WHICH signal_types are getting suppressed most —
				// often a hint that a mapper or upstream source needs tuning.
				sigType := resultLabel(r, "k8sgpt-detection-pack.io/signal_type")
				cat := resultLabel(r, "k8sgpt-detection-pack.io/category")
				sev := resultLabel(r, "k8sgpt-detection-pack.io/severity")
				ns := resultTargetNamespace(r)
				b.metrics.suppressed.WithLabelValues(sigType, cat, sev, ns).Inc()
				// Debug-level so high-volume re-deliveries don't flood logs
				// by default. Operators tuning suppression enable debug
				// logs temporarily.
				b.log.Debug("suppression: no-change skip",
					"worker", id,
					"name", r.Name,
					"namespace", r.Namespace,
					"signal_type", sigType,
				)
			case err != nil:
				// Don't re-enqueue. Re-enqueueing on transient apiserver
				// errors would defeat the throttling story.
				b.log.Error("bounded emit upstream failed",
					"worker", id,
					"name", r.Name,
					"namespace", r.Namespace,
					"err", err,
				)
			default:
				b.metrics.emitted.WithLabelValues(
					resultLabel(r, "k8sgpt-detection-pack.io/signal_type"),
					resultLabel(r, "k8sgpt-detection-pack.io/category"),
					resultLabel(r, "k8sgpt-detection-pack.io/severity"),
					resultTargetNamespace(r),
				).Inc()
			}
		}
	}
}

// resultLabel returns the value of `key` from the Result's metadata.labels,
// or "unknown" if absent or empty. Used to populate the Prometheus label
// dimensions on k8sgpt_pack_results_emitted_total. Mapping the Pack's
// k8sgpt-detection-pack.io/* convention into metric labels keeps the
// counter's cardinality bounded by the Pack's own taxonomy (no operator
// can accidentally explode it via free-form Result labels).
func resultLabel(r *corev1alpha1.Result, key string) string {
	if r == nil {
		return "unknown"
	}
	if v, ok := r.GetLabels()[key]; ok && v != "" {
		return v
	}
	return "unknown"
}

// resultTargetNamespace returns the namespace of the workload the Result
// describes — parsed from spec.name (formatted as "namespace/name" for
// namespaced resources, or just "name" for cluster-scoped ones). Used as
// the target_namespace metric label so dashboards can break detection
// volume down by namespace.
//
// Sentinel values:
//
//	"unknown" — Result is nil or spec.name is empty (defensive; mappers always set spec.name)
//	"cluster" — spec.name has no "/" separator (cluster-scoped resource: Node, ClusterRole, etc.)
func resultTargetNamespace(r *corev1alpha1.Result) string {
	if r == nil {
		return "unknown"
	}
	name := r.Spec.Name
	if name == "" {
		return "unknown"
	}
	if i := strings.Index(name, "/"); i > 0 {
		return name[:i]
	}
	return "cluster"
}

// Shutdown signals workers to stop and waits up to ctx's deadline for
// them to drain in-flight work. Safe to call multiple times.
func (b *BoundedEmitter) Shutdown(ctx context.Context) error {
	b.stopOnce.Do(func() {
		b.mu.Lock()
		b.stopped = true
		b.mu.Unlock()
		close(b.stopCh)
	})
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Compile-time interface check.
var _ Emitter = (*BoundedEmitter)(nil)
