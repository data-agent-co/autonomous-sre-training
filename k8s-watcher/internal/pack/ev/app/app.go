// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package app runs the events-watcher pipeline: filter, dedup, mapper with
// probe enrichment, and the bounded Result emitter, plus the lane's HTTP
// server (/healthz, /readyz, /metrics). cmd/watcher runs it as one lane.
//
// Run honors the supplied context for shutdown and returns an error
// instead of calling os.Exit, so a parent process running several
// pipelines as goroutines can supervise it.
//
// Events arrive through the Source the caller passes in Options; /readyz
// reports whether it has synced. probe_failure events are mapped on their
// own workers (see probeQueue). The emitter and metrics packages are shared
// with configmap-watcher, and Results are written through
// Options.ResultClient, a dynamic client.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"k8s.io/client-go/dynamic"
	listerscorev1 "k8s.io/client-go/listers/core/v1"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/emitter"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/dedup"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/filter"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/mapper"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/probectx"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/metrics"
)

// component is the value of the `component` label on this pipeline's
// metrics.
const component = "events-watcher"

// Handler is the callback a Source calls once per event. It runs the whole
// pipeline synchronously for that event.
type Handler func(context.Context, *event.Event)

// Source delivers events to the pipeline. Run must call handle for every
// event it observes and block until ctx is cancelled; a non-nil error ends
// the whole Run.
type Source interface {
	Run(ctx context.Context, handle Handler) error
}

// SyncedSource is optionally implemented by a Source that needs time before
// it delivers live events, such as an informer's initial list. /readyz
// reports 503 until HasSynced returns true; a Source without it is ready
// as soon as Run starts.
type SyncedSource interface {
	HasSynced() bool
}

// PodListerSource is optionally implemented by a Source that also keeps a
// Pod cache. Probe enrichment uses it to read pod CPU limits and restart
// counts; without it the kube side of the lookup reports nothing and the
// mapper falls back to the legacy classification.
type PodListerSource interface {
	PodLister() listerscorev1.PodLister
}

// Options configures an events-watcher run. Zero values fall back to the
// defaults in withDefaults.
type Options struct {
	ResultNamespace string
	HealthAddr      string
	DedupWindow     time.Duration
	SweepInterval   time.Duration
	DryRun          bool

	EmitterQueueDepth int
	EmitterWorkers    int
	EmitterQPS        float64
	EmitterBurst      int
	EmitterDropPolicy string

	// ProbeEnrichmentEnabled gates the per-event Prometheus + pod-lister
	// lookup that promotes probe_failure from network_partition →
	// resource_exhaustion. When false the mapper falls back to legacy
	// classification (the static signal→fault_class map).
	ProbeEnrichmentEnabled bool

	// PrometheusURL is used by the probe-context lookup. Empty disables
	// the Prometheus side of the lookup (kube-lister side still runs).
	// When ProbeEnrichmentEnabled is false this value is ignored.
	PrometheusURL string

	// Source feeds events into the pipeline. Required.
	Source Source

	// ResultClient writes the Results. Required unless DryRun is set.
	ResultClient dynamic.Interface

	Version string
	Log     *slog.Logger
}

func (o *Options) withDefaults() {
	if o.ResultNamespace == "" {
		o.ResultNamespace = "k8sgpt-system"
	}
	if o.HealthAddr == "" {
		o.HealthAddr = ":8080"
	}
	if o.DedupWindow == 0 {
		o.DedupWindow = 5 * time.Minute
	}
	if o.SweepInterval == 0 {
		o.SweepInterval = time.Minute
	}
	if o.EmitterQueueDepth == 0 {
		o.EmitterQueueDepth = 1000
	}
	if o.EmitterWorkers == 0 {
		o.EmitterWorkers = 4
	}
	if o.EmitterQPS == 0 {
		o.EmitterQPS = 50
	}
	if o.EmitterBurst == 0 {
		o.EmitterBurst = 100
	}
	if o.EmitterDropPolicy == "" {
		o.EmitterDropPolicy = "drop-oldest"
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
}

// Run starts events-watcher and blocks until ctx is cancelled or a fatal
// subsystem error occurs.
func Run(ctx context.Context, o Options) error {
	o.withDefaults()
	log := o.Log
	if o.Source == nil {
		return errors.New("events-watcher: Options.Source is required")
	}
	log.Info("events-watcher starting",
		"version", o.Version,
		"resultNamespace", o.ResultNamespace,
		"healthAddr", o.HealthAddr,
		"dedupWindow", o.DedupWindow.String(),
		"dryRun", o.DryRun,
		"probeEnrichmentEnabled", o.ProbeEnrichmentEnabled,
		"emitterQueueDepth", o.EmitterQueueDepth,
		"emitterWorkers", o.EmitterWorkers,
		"emitterQPS", o.EmitterQPS,
		"emitterBurst", o.EmitterBurst,
		"emitterDropPolicy", o.EmitterDropPolicy,
	)

	metricsReg, metricsHandler := metrics.New(component, o.Version)

	upstream, err := buildEmitter(o, log)
	if err != nil {
		return fmt.Errorf("emitter init: %w", err)
	}
	em, err := emitter.NewBounded(upstream, emitter.Config{
		QueueDepth: o.EmitterQueueDepth,
		Workers:    o.EmitterWorkers,
		QPS:        float32(o.EmitterQPS),
		Burst:      o.EmitterBurst,
		DropPolicy: emitter.DropPolicy(o.EmitterDropPolicy),
	}, component, metricsReg, log.With("component", "bounded-emitter"))
	if err != nil {
		return fmt.Errorf("bounded emitter init: %w", err)
	}

	flt := filter.New()
	dd := dedup.New(o.DedupWindow)

	// Pod-resource context lookup for probe_failure classification. The
	// lister is read on every lookup rather than once, because the Source
	// publishes it only after its cache syncs: lookups before that return
	// Available=false (graceful — mapper falls back to legacy behavior).
	podSource, _ := o.Source.(PodListerSource)

	mp := mapper.New(o.ResultNamespace)

	emit := func(ctx context.Context, m *mapper.Mapper, e *event.Event, d filter.Decision) {
		if err := em.Emit(ctx, m.ToResult(e, d)); err != nil {
			log.Warn("bounded emit refused", "err", err, "key", dedupKey(e).String())
		}
	}

	// With enrichment on, probe_failure events go through probeMapper on
	// the probe queue (see probeQueue); kubeMapper, which skips Prometheus,
	// maps the ones that find the queue full.
	var (
		probes     *probeQueue
		kubeMapper *mapper.Mapper
	)
	if o.ProbeEnrichmentEnabled {
		listerFn := func() listerscorev1.PodLister {
			if podSource == nil {
				return nil
			}
			return podSource.PodLister()
		}
		kubeLookup := func(ns, name string) probectx.ProbeContext {
			return probectx.NewKubeLookup(listerFn())(ns, name)
		}
		promURL := o.PrometheusURL
		if promURL == "" {
			promURL = os.Getenv("PROMETHEUS_URL")
		}
		lookup := probectx.NewPrometheusLookup(promURL, kubeLookup, 2*time.Second)
		probeMapper := mapper.New(o.ResultNamespace, mapper.WithProbeCtxLookup(lookup))
		kubeMapper = mapper.New(o.ResultNamespace, mapper.WithProbeCtxLookup(kubeLookup))
		probes = newProbeQueue(probeQueueDepth, func(ctx context.Context, e *event.Event, d filter.Decision) {
			emit(ctx, probeMapper, e, d)
		})
	} else {
		log.Info("probe enrichment disabled; probe_failure events stay on the legacy signal→fault_class mapping")
	}

	handler := func(ctx context.Context, e *event.Event) {
		decision := flt.Apply(e)
		if !decision.Allow {
			return
		}
		key := dedupKey(e)
		if !dd.ShouldEmit(key) {
			log.Debug("dedup suppressed", "key", key.String())
			return
		}
		if probes != nil && decision.SignalType == "probe_failure" {
			if probes.submit(e, decision) {
				return
			}
			log.Debug("probe queue full; mapping without Prometheus", "key", key.String())
			emit(ctx, kubeMapper, e, decision)
			return
		}
		emit(ctx, mp, e, decision)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", readyz(o.Source))
	mux.Handle("/metrics", metricsHandler)
	httpSrv := &http.Server{
		Addr:              o.HealthAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	em.Start(runCtx)
	defer func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := em.Shutdown(shutdownCtx); err != nil {
			log.Warn("bounded emitter shutdown timed out", "err", err)
		}
	}()

	if probes != nil {
		probeCtx, stopProbes := context.WithCancel(runCtx)
		probes.start(probeCtx, probeWorkers)
		// Runs before the emitter's Shutdown above, so no worker emits
		// into a stopped emitter.
		defer func() {
			stopProbes()
			probes.wait()
		}()
	}

	go runSweeper(runCtx, dd, o.SweepInterval, log.With("component", "sweeper"))

	informerDone := make(chan error, 1)
	go func() { informerDone <- o.Source.Run(runCtx, handler) }()

	httpDone := make(chan error, 1)
	go func() {
		log.Info("health server listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpDone <- err
			return
		}
		httpDone <- nil
	}()

	var runErr error
	select {
	case <-runCtx.Done():
		log.Info("shutdown signal received")
	case err := <-informerDone:
		if err != nil {
			log.Error("event source failed", "err", err)
			runErr = err
		}
	case err := <-httpDone:
		if err != nil {
			log.Error("health server failed", "err", err)
			runErr = err
		}
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)

	log.Info("events-watcher stopped")
	return runErr
}

// readyz answers 200 once src has synced (see SyncedSource) and 503 before.
// /healthz stays 200 throughout: a slow first list is not a reason to
// restart the process, and a list that never completes ends Run instead.
func readyz(src Source) http.HandlerFunc {
	synced, _ := src.(SyncedSource)
	return func(w http.ResponseWriter, _ *http.Request) {
		if synced != nil && !synced.HasSynced() {
			http.Error(w, "source not synced", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func dedupKey(e *event.Event) dedup.Key {
	return dedup.Key{
		InvolvedKind:      e.InvolvedKind,
		InvolvedNamespace: e.InvolvedNamespace,
		InvolvedName:      e.InvolvedName,
		Reason:            e.Reason,
	}
}

func buildEmitter(o Options, log *slog.Logger) (emitter.Emitter, error) {
	if o.DryRun {
		return emitter.NewDryRun(log.With("component", "emitter", "mode", "dry-run")), nil
	}
	if o.ResultClient == nil {
		return nil, errors.New("build emitter: Options.ResultClient is required unless DryRun is set")
	}
	return emitter.New(o.ResultClient, emitter.SameEvent, log.With("component", "emitter")), nil
}

func runSweeper(ctx context.Context, dd *dedup.Cache, interval time.Duration, log *slog.Logger) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			removed := dd.SweepStale()
			if len(removed) > 0 {
				log.Debug("dedup sweep", "removed", len(removed))
			}
		}
	}
}
