package app

import (
	"context"
	"sync"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/filter"
)

// Probe enrichment asks Prometheus about every probe_failure, which can take
// up to the lookup's timeout. Done on the event pipeline, a burst of probe
// failures with a slow or unreachable Prometheus would hold up every event
// behind it, so those events are mapped and emitted on a probeQueue instead.
//
// Workers can finish events out of order. Two events for the same Result
// pass dedup at least the dedup window apart, and a queued event is written
// at most probeQueueDepth/probeWorkers lookups plus the one in flight and
// its own after it passed dedup (about 132s with the 2s timeout). So with a
// window of 2m15s or more (the default is 5m) the older event is always
// written first. A shorter window can let it overwrite the newer one.
const (
	// probeWorkers is how many probe_failure lookups run at once.
	probeWorkers = 4
	// probeQueueDepth bounds the probe_failure events waiting for a worker.
	// When it is full the pipeline maps the event itself, without the
	// Prometheus lookup, so it never waits.
	probeQueueDepth = 256
)

type probeJob struct {
	e *event.Event
	d filter.Decision
}

// probeQueue hands probe_failure events to a fixed set of workers through a
// bounded queue. process maps and emits one event; it runs on the workers.
type probeQueue struct {
	jobs    chan probeJob
	process func(context.Context, *event.Event, filter.Decision)
	wg      sync.WaitGroup
}

func newProbeQueue(depth int, process func(context.Context, *event.Event, filter.Decision)) *probeQueue {
	return &probeQueue{jobs: make(chan probeJob, depth), process: process}
}

// start launches workers that run until ctx ends. Events still queued then
// are not processed.
func (q *probeQueue) start(ctx context.Context, workers int) {
	for range workers {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-q.jobs:
					if ctx.Err() != nil {
						return
					}
					q.process(ctx, j.e, j.d)
				}
			}
		}()
	}
}

// submit queues an event without blocking. It returns false when the queue
// is full.
func (q *probeQueue) submit(e *event.Event, d filter.Decision) bool {
	select {
	case q.jobs <- probeJob{e: e, d: d}:
		return true
	default:
		return false
	}
}

// wait blocks until every worker has returned. A worker in the middle of a
// lookup returns once the lookup's own timeout has passed.
func (q *probeQueue) wait() {
	q.wg.Wait()
}
