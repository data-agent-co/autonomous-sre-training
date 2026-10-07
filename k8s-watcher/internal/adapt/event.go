// Package adapt converts what this module's informers (internal/informer)
// deliver into the input types of the Detection Pack pipelines, and packages
// each informer as the Source a pipeline's app.Run expects.
package adapt

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
)

// EventFromCore maps a core/v1 Event onto the events-watcher pipeline's
// Event. It returns nil when the event names no involved object, the same
// rule the Pack applied to its events.v1 input.
//
// The Pack read events.v1, where eventTime is always the first observation
// and series.lastObservedTime the latest. core/v1 events written through
// the legacy API leave eventTime empty and carry firstTimestamp /
// lastTimestamp instead, so each end of the series falls back through the
// fields that can hold it. FirstSeen matters most: the filter's grace period
// for FailedScheduling measures age from it, and a FirstSeen taken from the
// latest occurrence would keep a repeating event forever "too young".
func EventFromCore(e *corev1.Event) *event.Event {
	if e == nil || e.InvolvedObject.Name == "" {
		return nil
	}
	reporter := e.ReportingController
	if reporter == "" {
		reporter = e.Source.Component
	}
	out := &event.Event{
		InvolvedNamespace:   e.InvolvedObject.Namespace,
		InvolvedName:        e.InvolvedObject.Name,
		InvolvedKind:        e.InvolvedObject.Kind,
		InvolvedUID:         string(e.InvolvedObject.UID),
		Reason:              e.Reason,
		Note:                e.Message,
		Type:                e.Type,
		ReportingController: reporter,
		FirstSeen:           firstSeen(e),
		LastSeen:            lastSeen(e),
		Count:               e.Count,
	}
	if e.Series != nil {
		out.Count = e.Series.Count
	}
	return out
}

// firstSeen is the start of the event's series: eventTime, then
// firstTimestamp, then lastTimestamp, then the series' last observation,
// then the object's creation time.
func firstSeen(e *corev1.Event) time.Time {
	candidates := []time.Time{e.EventTime.Time, e.FirstTimestamp.Time, e.LastTimestamp.Time}
	if e.Series != nil {
		candidates = append(candidates, e.Series.LastObservedTime.Time)
	}
	candidates = append(candidates, e.CreationTimestamp.Time)
	return firstNonZero(candidates...)
}

// lastSeen is the most recent occurrence: the series' last observation,
// then lastTimestamp, then eventTime, then firstTimestamp, then the
// object's creation time.
func lastSeen(e *corev1.Event) time.Time {
	var candidates []time.Time
	if e.Series != nil {
		candidates = append(candidates, e.Series.LastObservedTime.Time)
	}
	candidates = append(candidates, e.LastTimestamp.Time, e.EventTime.Time, e.FirstTimestamp.Time, e.CreationTimestamp.Time)
	return firstNonZero(candidates...)
}

func firstNonZero(times ...time.Time) time.Time {
	for _, t := range times {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}
