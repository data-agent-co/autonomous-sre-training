// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package mutation defines the watcher-internal representation of a single
// ConfigMap mutation event. Decouples informer wire shapes from the
// filter/diff/mapper chain.
package mutation

import "time"

// Kind identifies which resource type was mutated.
type Kind string

const KindConfigMap Kind = "ConfigMap"

// Op is the mutation operation.
type Op string

const (
	OpAdd    Op = "Add"
	OpUpdate Op = "Update"
	OpDelete Op = "Delete"
)

// Mutation is the watcher-internal representation of one observed change to
// a ConfigMap.
type Mutation struct {
	Kind      Kind
	Op        Op
	Namespace string
	Name      string
	UID       string

	// Labels and annotations of the resource AT THE TIME of the mutation.
	// Used by the filter to apply the critical-label opt-in.
	Labels      map[string]string
	Annotations map[string]string

	// ResourceVersion of the new (post-mutation) state.
	ResourceVersion string

	// Data and BinaryData hold the current resource contents (empty for
	// OpDelete). The watcher's informer passes per-key "<len>:<sha16>"
	// digests here, never values (see adapt.MutationFromConfigMapEvent).
	Data       map[string]string
	BinaryData map[string][]byte

	// PrevData / PrevBinaryData are the contents from before the mutation
	// (empty for OpAdd; the last known contents for OpDelete).
	PrevData       map[string]string
	PrevBinaryData map[string][]byte

	// ObservedAt is the wall-clock timestamp the watcher dispatched this
	// mutation to handlers.
	ObservedAt time.Time
}
