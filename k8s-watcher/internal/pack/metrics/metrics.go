// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package metrics owns each pipeline's /metrics endpoint and the info gauge
// that tells "component is alive and scrapeable" from "component is silent
// because broken".
//
// Each pipeline gets its own registry, so its emitter metrics (see the
// emitter package) and the `component` label stay scoped to its own port.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// New returns a registry pre-populated with the info gauge for component
// (the value of the `component` label, e.g. "events-watcher") plus an
// http.Handler ready to mount at /metrics. Callers should use the returned
// handler (not promhttp.Handler()) so the metrics surface stays scoped to
// this component's registry and doesn't leak Go runtime / process metrics
// from the default registerer.
func New(component, version string) (*prometheus.Registry, http.Handler) {
	reg := prometheus.NewRegistry()

	info := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "k8sgpt_pack_component_info",
			Help: "Per-component build/runtime info. Value is always 1; labels carry the data. Presence of this series tells a running, scrapeable component from a silent one.",
		},
		[]string{"component", "version"},
	)
	info.WithLabelValues(component, version).Set(1)
	reg.MustRegister(info)

	return reg, promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})
}
