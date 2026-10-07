package informer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
)

// StartPodInformer starts an informer caching all Pods cluster-wide and
// returns its lister after the initial cache sync. It has no handlers: the
// cache only answers lookups. resyncPeriod=0 (no periodic resync). List and
// watch failures are logged; client-go's reflector keeps retrying them. If
// the initial list has not completed within syncTimeout (DefaultSyncTimeout
// when 0), it returns an error naming the last failure.
func StartPodInformer(ctx context.Context, client kubernetes.Interface, syncTimeout time.Duration, logger *slog.Logger) (listerscorev1.PodLister, error) {
	factory := informers.NewSharedInformerFactory(client, 0)
	pods := factory.Core().V1().Pods()
	inf := pods.Informer()
	last, err := trackWatchErrors(inf, logger, "pod informer watch error")
	if err != nil {
		return nil, fmt.Errorf("set pod watch error handler: %w", err)
	}
	if err := startAndWait(ctx, factory, inf, syncTimeout, last); err != nil {
		return nil, fmt.Errorf("pod informer: %w", err)
	}
	logger.Info("pod informer synced")
	return pods.Lister(), nil
}
