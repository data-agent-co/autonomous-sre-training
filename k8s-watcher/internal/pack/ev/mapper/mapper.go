// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package mapper translates a watcher-internal event.Event (plus the
// filter's Decision) into a K8sGPT *corev1alpha1.Result: its name and
// labels, the failure text with candidate causes (hypotheses.go, probe.go),
// the fault_class (fault_class.go) and the episode_id (episode.go).
package mapper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/k8sgpt/v1alpha1"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/filter"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/probectx"
)

const (
	// BackendIdentifier is placed in ResultSpec.Backend for every EV
	// Result. Identifies the component to downstream consumers.
	BackendIdentifier = "k8sgpt-detection-pack:ev"

	// LabelComponent is the Pack-wide component label.
	LabelComponent = "k8sgpt-detection-pack.io/component"

	// ComponentName is the value of LabelComponent on EV Results.
	ComponentName = "ev"

	labelCategory   = "k8sgpt-detection-pack.io/category"
	labelSignalType = "k8sgpt-detection-pack.io/signal_type"
	labelSeverity   = "k8sgpt-detection-pack.io/severity"
)

// Mapper builds Result CRDs from events. Results are placed into Namespace.
type Mapper struct {
	Namespace string

	// ProbeCtxLookup, when non-nil, is consulted on probe_failure events
	// to refine the fault_class projection (e.g. distinguish a CPU-
	// throttle-induced probe timeout from a real network partition).
	// Safe to leave nil — Probes then fall back to the kubelet-note-only
	// differential.
	ProbeCtxLookup probectx.Lookup
}

// Option configures a Mapper at construction time.
type Option func(*Mapper)

// WithProbeCtxLookup installs the pod-resource-context lookup used to
// classify probe failures. See the probectx package for implementations.
func WithProbeCtxLookup(l probectx.Lookup) Option {
	return func(m *Mapper) { m.ProbeCtxLookup = l }
}

// New returns a Mapper that emits Results into the given namespace.
func New(namespace string, opts ...Option) *Mapper {
	m := &Mapper{Namespace: namespace}
	for _, o := range opts {
		o(m)
	}
	return m
}

// ToResult builds a *corev1alpha1.Result from an event + the filter's
// Decision. The decision is expected to have Allow=true; callers should
// not invoke ToResult on a non-allowed event.
func (m *Mapper) ToResult(e *event.Event, d filter.Decision) *corev1alpha1.Result {
	ctx := m.probeCtxFor(e, d)
	fc, conf := m.faultClassFor(e, d, ctx)

	t := e.LastSeen
	if t.IsZero() {
		t = time.Now()
	}
	episode := episodeID(e, fc, t)

	return &corev1alpha1.Result{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CRDName(e),
			Namespace: m.Namespace,
			Labels:    crdLabels(d, fc, episode),
		},
		Spec: corev1alpha1.ResultSpec{
			Backend:               BackendIdentifier,
			AutoRemediationStatus: corev1alpha1.AutoRemediationStatus{},
			Kind:                  e.InvolvedKind,
			Name:                  specName(e),
			ParentObject:          "",
			Error:                 []corev1alpha1.Failure{{Text: failureText(e, d, ctx)}},
			Details:               buildDetails(e, d, ctx, fc, episode, conf),
		},
	}
}

// probeCtxFor returns the ProbeContext snapshot relevant for this event.
// Only probe_failure events on Pods consult the lookup; everything else
// gets a zero-value ProbeContext (Available=false).
func (m *Mapper) probeCtxFor(e *event.Event, d filter.Decision) probectx.ProbeContext {
	if m.ProbeCtxLookup == nil {
		return probectx.ProbeContext{}
	}
	if d.SignalType != "probe_failure" || e.InvolvedKind != "Pod" {
		return probectx.ProbeContext{}
	}
	return m.ProbeCtxLookup(e.InvolvedNamespace, e.InvolvedName)
}

// faultClassFor returns the fault_class label value (or "" if none
// applies) plus the leading-hypothesis confidence. probe_failure is
// dynamic via probeClassify; everything else uses the static
// signalToFaultClass table.
func (m *Mapper) faultClassFor(e *event.Event, d filter.Decision, ctx probectx.ProbeContext) (string, float64) {
	if d.SignalType == "probe_failure" {
		p, ok := parseProbe(e.Note)
		if !ok {
			return FaultClassIndeterminate, 0.4
		}
		_, fc, conf := probeClassify(p, ctx)
		return fc, conf
	}
	if fc, ok := signalToFaultClass[d.SignalType]; ok {
		return fc, 0.85
	}
	return "", 0
}

// CRDName returns the deterministic metadata.name for the Result. Format:
// "ev-<sanitized-reason>-<sanitized-name>-<uid-hash>" (RFC 1123, lowercase).
// Stable across re-emissions of the same event class.
func CRDName(e *event.Event) string {
	reason := sanitize(strings.ToLower(e.Reason))
	name := sanitize(strings.ToLower(e.InvolvedName))
	if reason == "" {
		reason = "unknown"
	}
	if name == "" {
		name = "unknown"
	}
	// Keep the full name human-readable; truncate aggressively if the
	// combination would exceed RFC 1123's 253-char limit.
	combined := fmt.Sprintf("ev-%s-%s", reason, name)
	if len(combined) > 240 {
		combined = combined[:240]
	}
	return fmt.Sprintf("%s-%s", combined, hashSuffix(e))
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}

// hashSuffix returns 8 hex chars derived from the involved-object identity.
// Distinct UIDs produce distinct hashes; same UID across re-emissions is
// stable so the CRD name doesn't churn.
func hashSuffix(e *event.Event) string {
	src := e.InvolvedUID
	if src == "" {
		src = e.InvolvedKind + "/" + e.InvolvedNamespace + "/" + e.InvolvedName
	}
	h := sha256.Sum256([]byte(src))
	return hex.EncodeToString(h[:])[:8]
}

func crdLabels(d filter.Decision, faultClass, episode string) map[string]string {
	out := map[string]string{
		LabelComponent: ComponentName,
	}
	if d.Category != "" {
		out[labelCategory] = d.Category
	}
	if d.SignalType != "" {
		out[labelSignalType] = d.SignalType
	}
	if d.Severity != "" {
		out[labelSeverity] = d.Severity
	}
	if faultClass != "" {
		out[labelFaultClass] = faultClass
	}
	if episode != "" {
		out[labelEpisodeID] = episode
	}
	return out
}

func specName(e *event.Event) string {
	if e.InvolvedNamespace == "" {
		return e.InvolvedName
	}
	return e.InvolvedNamespace + "/" + e.InvolvedName
}

func failureText(e *event.Event, d filter.Decision, ctx probectx.ProbeContext) string {
	// Enriched signals get "what happened" + an ordered differential of
	// candidate causes (probe_failure additionally parses the kubelet note
	// for a failure-mode-specific list). Info-tier signals with no catalog
	// entry fall back to the raw note.
	if txt, ok := enrichedFailureText(e, d, ctx); ok {
		return txt
	}
	if e.Note != "" {
		return e.Note
	}
	return e.Reason
}

func buildDetails(e *event.Event, d filter.Decision, ctx probectx.ProbeContext, faultClass, episode string, confidence float64) string {
	payload := map[string]any{
		"reason":              e.Reason,
		"type":                e.Type,
		"note":                e.Note,
		"reportingController": e.ReportingController,
		"involvedKind":        e.InvolvedKind,
		"involvedNamespace":   e.InvolvedNamespace,
		"involvedName":        e.InvolvedName,
		"involvedUID":         e.InvolvedUID,
		"firstSeen":           e.FirstSeen,
		"lastSeen":            e.LastSeen,
		"count":               e.Count,
		"category":            d.Category,
		"signal_type":         d.SignalType,
		"severity":            d.Severity,
	}
	if faultClass != "" {
		payload["fault_class"] = faultClass
	}
	if episode != "" {
		payload["episode_id"] = episode
	}
	if confidence > 0 {
		payload["confidence"] = confidence
	}
	// Attach the candidate-cause differential so a downstream agent can
	// consume it machine-readably (not just from the human error text).
	if hyps := hypothesesFor(e, d, ctx); len(hyps) > 0 {
		payload["hypotheses"] = hyps
		// probe_failure also gets the parsed structured probe detail.
		if d.SignalType == "probe_failure" {
			if p, ok := parseProbe(e.Note); ok {
				probe := map[string]any{
					"kind":        p.Kind,
					"mechanism":   p.Mechanism,
					"target":      p.Target,
					"failureMode": p.FailureMode,
				}
				if p.Timeout != "" {
					probe["timeout"] = p.Timeout
				}
				if p.StatusCode != "" {
					probe["statusCode"] = p.StatusCode
				}
				if ctx.Available {
					probe["cpu_limit_millicores"] = ctx.CPULimitMillicores
					probe["cpu_throttle_rate"] = ctx.CPUThrottleRate
					probe["restarts_15m"] = ctx.RestartsLast15m
				}
				payload["probe"] = probe
			}
		}
	}
	b, _ := json.Marshal(payload)
	return string(b)
}
