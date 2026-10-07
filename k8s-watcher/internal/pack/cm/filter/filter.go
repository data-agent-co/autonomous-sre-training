// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package filter implements the critical-label opt-in. Only mutations on
// ConfigMaps carrying the critical label produce Results; everything else
// is silently ignored.
package filter

import (
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
)

// CriticalLabelKey is the label whose presence (with value "true") opts a
// ConfigMap into Result emission.
const CriticalLabelKey = "k8sgpt-detection-pack.io/critical"

// Decision is the filter's output for one mutation.
type Decision struct {
	Allow bool
}

// Filter is the public filter.
type Filter struct{}

// New returns a Filter.
func New() *Filter {
	return &Filter{}
}

// Apply returns Allow=true when the mutation should emit a Result.
func (f *Filter) Apply(m *mutation.Mutation) Decision {
	if m == nil {
		return Decision{}
	}
	if m.Labels[CriticalLabelKey] != "true" {
		return Decision{}
	}
	return Decision{Allow: true}
}
