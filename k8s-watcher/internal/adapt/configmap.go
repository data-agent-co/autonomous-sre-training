package adapt

import (
	"context"
	"log/slog"
	"maps"
	"sync/atomic"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/informer"
	cmapp "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/app"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
)

// MutationFromConfigMapEvent maps one informer notification onto the
// configmap-watcher pipeline's Mutation, or returns nil when the change is
// not one to report (ev.Critical is false).
//
// Data and BinaryData hold the per-key digests the informer cached, never
// values, so the Pack's diff still counts added / removed / changed keys and
// its Result shows digests where it used to show values. An add carries the
// new content only. An update carries the old revision in PrevData. A delete
// carries the last cached state in PrevData and nothing as current, so the
// diff reports every key as removed.
func MutationFromConfigMapEvent(ev informer.ConfigMapEvent, now time.Time) *mutation.Mutation {
	if !ev.Critical || ev.CM == nil {
		return nil
	}
	var op mutation.Op
	switch ev.ChangeType {
	case informer.ChangeTypeAdd:
		op = mutation.OpAdd
	case informer.ChangeTypeUpdate:
		op = mutation.OpUpdate
	case informer.ChangeTypeDelete:
		op = mutation.OpDelete
	default:
		return nil
	}
	cm := ev.CM
	annotations := maps.Clone(cm.Annotations)
	// Cache-only: it never existed on the real object.
	delete(annotations, informer.DataDigestAnnotation)
	m := &mutation.Mutation{
		Kind:            mutation.KindConfigMap,
		Op:              op,
		Namespace:       cm.Namespace,
		Name:            cm.Name,
		UID:             string(cm.UID),
		Labels:          maps.Clone(cm.Labels),
		Annotations:     annotations,
		ResourceVersion: cm.ResourceVersion,
		Data:            maps.Clone(cm.Data),
		BinaryData:      cloneBytes(cm.BinaryData),
		ObservedAt:      now,
	}
	switch {
	case op == mutation.OpUpdate && ev.Old != nil:
		m.PrevData = maps.Clone(ev.Old.Data)
		m.PrevBinaryData = cloneBytes(ev.Old.BinaryData)
	case op == mutation.OpDelete:
		m.PrevData, m.Data = m.Data, nil
		m.PrevBinaryData, m.BinaryData = m.BinaryData, nil
	}
	return m
}

func cloneBytes(in map[string][]byte) map[string][]byte {
	if in == nil {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// ConfigMapSource feeds the configmap-watcher pipeline from the ConfigMap
// informer. Only changes MutationFromConfigMapEvent keeps reach the pipeline.
type ConfigMapSource struct {
	Client kubernetes.Interface
	Log    *slog.Logger
	// Now is the clock stamped on each Mutation. Defaults to time.Now.
	Now func() time.Time
	// SyncTimeout bounds the informer's initial list. Defaults to
	// informer.DefaultSyncTimeout.
	SyncTimeout time.Duration

	synced atomic.Bool
}

var (
	_ cmapp.Source       = (*ConfigMapSource)(nil)
	_ cmapp.SyncedSource = (*ConfigMapSource)(nil)
)

// Run starts the informer, delivers every reportable change to handle, and
// blocks until ctx is cancelled. An initial list that does not complete
// within SyncTimeout ends Run with an error.
func (s *ConfigMapSource) Run(ctx context.Context, handle cmapp.Handler) error {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	deliver := func(ev informer.ConfigMapEvent) {
		if m := MutationFromConfigMapEvent(ev, now()); m != nil {
			handle(ctx, m)
		}
	}
	if err := informer.StartConfigMapInformer(ctx, s.Client, deliver, s.SyncTimeout, log); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	s.synced.Store(true)
	<-ctx.Done()
	return nil
}

// HasSynced reports whether the informer has completed its initial list.
func (s *ConfigMapSource) HasSynced() bool {
	return s.synced.Load()
}
