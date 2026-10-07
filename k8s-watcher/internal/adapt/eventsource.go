package adapt

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	listerscorev1 "k8s.io/client-go/listers/core/v1"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/informer"
	evapp "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/app"
)

// EventSource feeds the events-watcher pipeline from the Warning-event
// informer. Normal events are not watched, so they never reach the pipeline.
//
// With WatchPods set it also keeps a Pod cache for the pipeline's probe
// enrichment, the way the Pack's own informer did.
type EventSource struct {
	Client    kubernetes.Interface
	Log       *slog.Logger
	WatchPods bool
	// SyncTimeout bounds each informer's initial list. Defaults to
	// informer.DefaultSyncTimeout.
	SyncTimeout time.Duration

	podLister atomic.Pointer[listerscorev1.PodLister]
	synced    atomic.Bool
}

var (
	_ evapp.Source          = (*EventSource)(nil)
	_ evapp.PodListerSource = (*EventSource)(nil)
	_ evapp.SyncedSource    = (*EventSource)(nil)
)

// Run starts the informers, delivers every converted event to handle, and
// blocks until ctx is cancelled. The Pod cache is synced before the event
// watch starts, so the initial replay of events can already use it. An
// informer whose initial list does not complete within SyncTimeout ends Run
// with an error.
func (s *EventSource) Run(ctx context.Context, handle evapp.Handler) error {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	if s.WatchPods {
		lister, err := informer.StartPodInformer(ctx, s.Client, s.SyncTimeout, log)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.podLister.Store(&lister)
	}

	deliver := func(ev *corev1.Event) {
		if e := EventFromCore(ev); e != nil {
			handle(ctx, e)
		}
	}
	if err := informer.StartEventInformer(ctx, s.Client, deliver, s.SyncTimeout, log); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	s.synced.Store(true)
	<-ctx.Done()
	return nil
}

// HasSynced reports whether every informer has completed its initial list.
func (s *EventSource) HasSynced() bool {
	return s.synced.Load()
}

// PodLister returns the Pod cache, or nil until it has synced (or when
// WatchPods is off).
func (s *EventSource) PodLister() listerscorev1.PodLister {
	if p := s.podLister.Load(); p != nil {
		return *p
	}
	return nil
}
