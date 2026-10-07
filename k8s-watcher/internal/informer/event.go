// Package informer sets up the Kubernetes informers that feed the watcher:
// Warning events, ConfigMaps (cached with per-key digests in place of
// values) and, for probe enrichment, Pods. Each Start function returns once
// the initial list has completed, or with an error once syncTimeout has
// passed.
package informer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// EventHandler is called for each Warning event add/update. Kind is not
// filtered server-side.
type EventHandler func(ev *corev1.Event)

// warningEventFieldSelector is the informer's server-side filter.
func warningEventFieldSelector() string {
	return fields.OneTermEqualSelector("type", string(corev1.EventTypeWarning)).String()
}

// StartEventInformer starts an informer watching Warning events cluster-wide
// and returns after the initial cache sync. Uses server-side fieldSelector
// type=Warning. resyncPeriod=0 (no periodic resync — the EV pipeline's dedup
// and upserting emitter absorb any re-delivery). List and watch failures are
// logged; client-go's reflector keeps retrying them. If the initial list has
// not completed within syncTimeout (DefaultSyncTimeout when 0), it returns
// an error naming the last failure, for example a missing RBAC rule.
func StartEventInformer(ctx context.Context, client kubernetes.Interface, handler EventHandler, syncTimeout time.Duration, logger *slog.Logger) error {
	fieldSelector := warningEventFieldSelector()
	factory := informers.NewSharedInformerFactoryWithOptions(
		client,
		0, // resyncPeriod=0: no periodic resync
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.FieldSelector = fieldSelector
		}),
	)
	inf := factory.Core().V1().Events().Informer()
	last, err := trackWatchErrors(inf, logger, "event informer watch error", "field_selector", fieldSelector)
	if err != nil {
		return fmt.Errorf("set event watch error handler: %w", err)
	}
	_, err = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if ev, ok := obj.(*corev1.Event); ok {
				handler(ev)
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			if ev, ok := newObj.(*corev1.Event); ok {
				handler(ev)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("add event handler: %w", err)
	}
	if err := startAndWait(ctx, factory, inf, syncTimeout, last); err != nil {
		return fmt.Errorf("event informer (%s): %w", fieldSelector, err)
	}
	logger.Info("event informer synced", "field_selector", fieldSelector)
	return nil
}
