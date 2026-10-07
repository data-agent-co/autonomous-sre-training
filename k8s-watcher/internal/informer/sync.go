package informer

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

// DefaultSyncTimeout bounds the wait for an informer's initial list when the
// caller passes no timeout. client-go retries a failing list forever, so
// without a bound a missing RBAC rule would leave the watcher waiting with
// nothing in its log but the retries.
const DefaultSyncTimeout = 2 * time.Minute

// lastError keeps the most recent list or watch error an informer reported,
// so a sync timeout can say why the list never completed.
type lastError struct {
	mu  sync.Mutex
	err error
}

func (l *lastError) set(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.err = err
}

func (l *lastError) get() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// trackWatchErrors installs a watch error handler on inf that logs each list
// or watch failure with logArgs and keeps the latest for startAndWait. It
// must be called before the informer starts.
func trackWatchErrors(inf cache.SharedIndexInformer, logger *slog.Logger, msg string, logArgs ...any) (*lastError, error) {
	last := &lastError{}
	err := inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		last.set(err)
		logger.Error(msg, append(logArgs, "err", err)...)
	})
	if err != nil {
		return nil, err
	}
	return last, nil
}

// startAndWait starts factory and waits for inf's initial list. It gives up
// after timeout (DefaultSyncTimeout when timeout is not positive) with an
// error that carries the last list or watch error, and returns ctx.Err()
// when ctx ends first. The informer runs until ctx ends either way; a caller
// that gets an error is expected to give up and cancel ctx.
func startAndWait(ctx context.Context, factory informers.SharedInformerFactory, inf cache.SharedIndexInformer, timeout time.Duration, last *lastError) error {
	if timeout <= 0 {
		timeout = DefaultSyncTimeout
	}
	factory.Start(ctx.Done())
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if cache.WaitForCacheSync(waitCtx.Done(), inf.HasSynced) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := last.get(); err != nil {
		return fmt.Errorf("initial list did not complete within %s; last error: %w", timeout, err)
	}
	return fmt.Errorf("initial list did not complete within %s", timeout)
}
