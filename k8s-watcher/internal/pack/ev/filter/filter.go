// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package filter decides whether an incoming Kubernetes event is interesting
// enough to surface as a Result CRD. It also tags the event with the Pack's
// `category` and `signal_type` so the mapper can apply the right labels
// without re-deriving them.
//
// The filter is intentionally opinionated: the upstream events stream is
// noisy by default and the watcher would drown downstream consumers if
// every event became a Result.
package filter

import (
	"strings"
	"time"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
)

// Decision is the filter's output for one event.
type Decision struct {
	// Allow is true when the event should be surfaced as a Result.
	Allow bool

	// Category is the coarse area the event belongs to (e.g.,
	// "pod_lifecycle"), set as the Result's category label. Populated only
	// when Allow is true.
	Category string

	// SignalType is the specific fault subtype downstream consumers route
	// on (e.g., "oom_kill"). Populated only when Allow is true.
	SignalType string

	// Severity is the Pack severity ("warning" or "critical"). Populated
	// only when Allow is true.
	Severity string
}

// Filter is the public interface — caller-owned so tests can substitute.
type Filter struct {
	// Now is the time source. Override in tests; defaults to time.Now.
	Now func() time.Time
}

// New returns a Filter. Per-reason grace periods (60s for the scheduling
// reasons) are set in reasonMap.
func New() *Filter {
	return &Filter{Now: time.Now}
}

// Apply evaluates the event and returns the routing decision.
func (f *Filter) Apply(e *event.Event) Decision {
	if e == nil {
		return Decision{}
	}
	// cert-manager emits IssuerNotFound as Normal (not Warning) — allow it
	// before the Warning-only gate. This watcher's event source delivers
	// Warning events only, so a Normal IssuerNotFound never gets here.
	if e.Reason == "IssuerNotFound" {
		return Decision{Allow: true, Category: "tls_certs", SignalType: "cert_not_ready", Severity: "warning"}
	}
	// Only Warning events are actionable. Normal events are noise.
	if e.Type != "Warning" {
		return Decision{}
	}

	mapping, ok := reasonMap[e.Reason]
	if !ok {
		// Pod-lifecycle "Failed" reasons need note inspection to
		// distinguish image-pull failures from generic container errors.
		// cert-manager also fires "Failed" Warning events on Certificate and
		// CertificateRequest objects when the issuer is unreachable or errors.
		if e.Reason == "Failed" {
			if isImagePullFailure(e.Note) {
				return Decision{Allow: true, Category: "pod_lifecycle", SignalType: "image_pull_failure", Severity: "warning"}
			}
			if isCertManagerKind(e.InvolvedKind) {
				return Decision{Allow: true, Category: "tls_certs", SignalType: "cert_not_ready", Severity: "warning"}
			}
		}
		return Decision{}
	}

	// Apply per-reason grace periods.
	if mapping.minAge > 0 {
		age := f.now().Sub(e.FirstSeen)
		if age < mapping.minAge {
			return Decision{}
		}
	}

	return Decision{
		Allow:      true,
		Category:   mapping.category,
		SignalType: mapping.signalType,
		Severity:   mapping.severity,
	}
}

func (f *Filter) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// reasonMap is the curated allow-list of Warning-event reasons that produce
// Results.
var reasonMap = map[string]struct {
	category   string
	signalType string
	severity   string
	minAge     time.Duration
}{
	// Pod lifecycle.
	"BackOff":            {category: "pod_lifecycle", signalType: "back_off", severity: "warning"},
	"Killing":            {category: "pod_lifecycle", signalType: "killing", severity: "info"},
	"Unhealthy":          {category: "pod_lifecycle", signalType: "probe_failure", severity: "warning"},
	"OOMKilled":          {category: "pod_lifecycle", signalType: "oom_kill", severity: "critical"},
	"Error":              {category: "pod_lifecycle", signalType: "container_error", severity: "warning"},
	"ContainerCannotRun": {category: "pod_lifecycle", signalType: "container_cannot_run", severity: "critical"},
	// Scheduling — apply grace period to suppress normal-startup noise.
	"FailedScheduling": {category: "scheduling", signalType: "failed_scheduling", severity: "warning", minAge: 60 * time.Second},
	"Unschedulable":    {category: "scheduling", signalType: "unschedulable", severity: "warning", minAge: 60 * time.Second},
	// Storage.
	"FailedMount":        {category: "storage", signalType: "failed_mount", severity: "warning"},
	"FailedAttachVolume": {category: "storage", signalType: "failed_attach_volume", severity: "warning"},
	"VolumeFailedDelete": {category: "storage", signalType: "volume_failed_delete", severity: "info"},
	"ProvisioningFailed": {category: "storage", signalType: "provisioning_failed", severity: "warning"},
	// Eviction.
	"Evicted": {category: "eviction", signalType: "evicted", severity: "warning"},
}

func isImagePullFailure(note string) bool {
	n := strings.ToLower(note)
	return strings.Contains(n, "errimagepull") ||
		strings.Contains(n, "imagepullbackoff") ||
		strings.Contains(n, "failed to pull image")
}

// isCertManagerKind returns true for cert-manager Certificate and
// CertificateRequest InvolvedObjectKinds so that "Failed" Warning events
// on those objects are routed to the cert_not_ready signal.
func isCertManagerKind(kind string) bool {
	return kind == "Certificate" || kind == "CertificateRequest"
}
