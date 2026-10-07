// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package app runs the configmap-watcher pipeline: the critical-label
// filter, the mapper (which computes the key diff), and the bounded Result
// emitter, plus the lane's HTTP server (/healthz, /readyz, /metrics).
// cmd/watcher runs it as one lane.
//
// Run honors the supplied context for shutdown and returns an error
// instead of calling os.Exit, so a parent process running several
// pipelines as goroutines can supervise it.
//
// Mutations arrive through the Source the caller passes in Options; /readyz
// reports whether it has synced. The emitter and metrics packages are
// shared with events-watcher, and Results are written through
// Options.ResultClient, a dynamic client.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"k8s.io/client-go/dynamic"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/filter"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mapper"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/emitter"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/metrics"
)

// component is the value of the `component` label on this pipeline's
// metrics.
const component = "configmap-watcher"

// Handler is the callback a Source calls once per mutation. It runs the
// whole pipeline synchronously for that mutation.
type Handler func(context.Context, *mutation.Mutation)

// Source delivers mutations to the pipeline. Run must call handle for every
// mutation it wants considered and block until ctx is cancelled; a non-nil
// error ends the whole Run.
type Source interface {
	Run(ctx context.Context, handle Handler) error
}

// SyncedSource is optionally implemented by a Source that needs time before
// it delivers live mutations, such as an informer's initial list. /readyz
// reports 503 until HasSynced returns true; a Source without it is ready
// as soon as Run starts.
type SyncedSource interface {
	HasSynced() bool
}

// Options configures a configmap-watcher run. Zero values fall back to the
// defaults in withDefaults.
type Options struct {
	ResultNamespace string
	HealthAddr      string
	DryRun          bool

	EmitterQueueDepth int
	EmitterWorkers    int
	EmitterQPS        float64
	EmitterBurst      int
	EmitterDropPolicy string

	// Source feeds mutations into the pipeline. Required.
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
	if o.EmitterQueueDepth == 0 {
		o.EmitterQueueDepth = 100
	}
	if o.EmitterWorkers == 0 {
		// One worker writes the edits of a ConfigMap in the order they
		// happened. With two, an older edit could be written over a newer
		// one and the Result would describe the older edit.
		o.EmitterWorkers = 1
	}
	if o.EmitterQPS == 0 {
		o.EmitterQPS = 20
	}
	if o.EmitterBurst == 0 {
		o.EmitterBurst = 50
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

// Run starts configmap-watcher and blocks until ctx is cancelled or a
// fatal subsystem error occurs.
func Run(ctx context.Context, o Options) error {
	o.withDefaults()
	log := o.Log
	if o.Source == nil {
		return errors.New("configmap-watcher: Options.Source is required")
	}
	log.Info("configmap-watcher starting",
		"version", o.Version,
		"resultNamespace", o.ResultNamespace,
		"healthAddr", o.HealthAddr,
		"dryRun", o.DryRun,
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
	mp := mapper.New(o.ResultNamespace)

	handler := func(ctx context.Context, m *mutation.Mutation) {
		if !flt.Apply(m).Allow {
			return
		}
		if err := em.Emit(ctx, mp.ToResult(m)); err != nil {
			log.Warn("bounded emit refused", "err", err, "kind", m.Kind, "ns", m.Namespace, "name", m.Name)
		}
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

	informerDone := make(chan error, 1)
	go func() { informerDone <- o.Source.Run(runCtx, handler) }()

	httpDone := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", httpSrv.Addr, "endpoints", []string{"/healthz", "/readyz", "/metrics"})
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
			log.Error("configmap source failed", "err", err)
			runErr = err
		}
	case err := <-httpDone:
		if err != nil {
			log.Error("http server failed", "err", err)
			runErr = err
		}
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)

	log.Info("configmap-watcher stopped")
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

func buildEmitter(o Options, log *slog.Logger) (emitter.Emitter, error) {
	if o.DryRun {
		return emitter.NewDryRun(log.With("component", "emitter", "mode", "dry-run")), nil
	}
	if o.ResultClient == nil {
		return nil, errors.New("build emitter: Options.ResultClient is required unless DryRun is set")
	}
	return emitter.New(o.ResultClient, emitter.SameChange, log.With("component", "emitter")), nil
}
