// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package event defines the watcher-internal representation of a Kubernetes
// event, decoupled from the upstream wire types and from the Result CRD
// shape. The event source fills it from core/v1 Events.
//
// The filter package decides whether an event is interesting, the dedup
// package collapses repeated identical events within a window, and the
// mapper package builds the Result CRD.
package event

import "time"

// Event is the watcher-internal representation of a single Kubernetes event.
type Event struct {
	// Namespace + Name of the involved object that the event is about.
	InvolvedNamespace string
	InvolvedName      string
	InvolvedKind      string
	InvolvedUID       string

	// Reason is the short machine-readable cause (e.g., "OOMKilled",
	// "FailedScheduling"). Used for routing and dedup key construction.
	Reason string

	// Note is the human-readable description Kubernetes attaches to the
	// event ("Memory limit exceeded by ...", "0/3 nodes available ...").
	Note string

	// Type is "Normal" or "Warning". The event source delivers Warning
	// events only, and the filter discards Normal ones (apart from
	// cert-manager's IssuerNotFound).
	Type string

	// ReportingController identifies the component that produced the event
	// (e.g., "kubelet", "default-scheduler"). Useful for filtering and
	// downstream attribution.
	ReportingController string

	// FirstSeen is the timestamp of the first occurrence of the event in
	// its series; LastSeen is the most recent occurrence. Kubernetes
	// coalesces repeated events into a series with a count.
	FirstSeen time.Time
	LastSeen  time.Time

	// Count is the number of occurrences in the series.
	Count int32
}
