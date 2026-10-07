// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package mapper

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/filter"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/probectx"
)

func TestToResult_PodOOMKilledPopulatesAllFields(t *testing.T) {
	m := New("k8sgpt-system")
	e := &event.Event{
		InvolvedKind:        "Pod",
		InvolvedNamespace:   "payments",
		InvolvedName:        "api-7d8c",
		InvolvedUID:         "uid-abc-123",
		Reason:              "OOMKilled",
		Note:                "Container worker exceeded memory limit",
		Type:                "Warning",
		ReportingController: "kubelet",
		FirstSeen:           time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC),
		LastSeen:            time.Date(2026, 5, 21, 10, 2, 0, 0, time.UTC),
		Count:               3,
	}
	d := filter.Decision{Allow: true, Category: "pod_lifecycle", SignalType: "oom_kill", Severity: "critical"}

	r := m.ToResult(e, d)
	require.Equal(t, "k8sgpt-system", r.Namespace)
	require.Equal(t, BackendIdentifier, r.Spec.Backend)
	require.Equal(t, "Pod", r.Spec.Kind)
	require.Equal(t, "payments/api-7d8c", r.Spec.Name)
	// oom_kill is an enriched signal: the error text now carries a "what
	// happened" header (incl. the raw note), a candidate-cause differential,
	// and the symptom caveat.
	require.Contains(t, r.Spec.Error[0].Text, "Container worker exceeded memory limit")
	require.Contains(t, r.Spec.Error[0].Text, "Possible causes")
	require.Equal(t, "ev", r.Labels[LabelComponent])
	require.Equal(t, "pod_lifecycle", r.Labels["k8sgpt-detection-pack.io/category"])
	require.Equal(t, "oom_kill", r.Labels["k8sgpt-detection-pack.io/signal_type"])
	require.Equal(t, "critical", r.Labels["k8sgpt-detection-pack.io/severity"])

	var details map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Spec.Details), &details))
	require.Equal(t, "OOMKilled", details["reason"])
	require.Equal(t, "kubelet", details["reportingController"])
}

func TestToResult_ClusterScopedKindUsesBareName(t *testing.T) {
	m := New("k8sgpt-system")
	e := &event.Event{
		InvolvedKind: "Node",
		InvolvedName: "worker-1",
		Reason:       "NodeNotSchedulable",
		Type:         "Warning",
	}
	d := filter.Decision{Allow: true, Category: "node_health", SignalType: "node_not_schedulable", Severity: "warning"}
	r := m.ToResult(e, d)
	require.Equal(t, "worker-1", r.Spec.Name, "cluster-scoped kinds carry bare name")
}

func TestToResult_FallsBackToReasonWhenNoteAbsent(t *testing.T) {
	m := New("k8sgpt-system")
	// Info-tier signal with no candidate-cause catalog entry, so the
	// note-absent fallback-to-Reason path is exercised. (Warning/critical
	// signals like failed_mount are now enriched and never fall back.)
	e := &event.Event{InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p", Reason: "Killing"}
	d := filter.Decision{Allow: true, Category: "pod_lifecycle", SignalType: "killing", Severity: "info"}
	r := m.ToResult(e, d)
	require.Equal(t, "Killing", r.Spec.Error[0].Text)
}

func TestCRDName_Deterministic(t *testing.T) {
	e := &event.Event{InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p1", Reason: "OOMKilled", InvolvedUID: "uid-1"}
	name := CRDName(e)
	require.Equal(t, CRDName(e), name, "same input must produce same name")
	require.True(t, strings.HasPrefix(name, "ev-oomkilled-p1-"), "got: %s", name)
	require.Regexp(t, `^ev-[a-z0-9\-]+-[a-f0-9]{8}$`, name)
}

func TestCRDName_DifferentUIDsProduceDifferentNames(t *testing.T) {
	e1 := &event.Event{InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p1", Reason: "BackOff", InvolvedUID: "uid-1"}
	e2 := &event.Event{InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p1", Reason: "BackOff", InvolvedUID: "uid-2"}
	require.NotEqual(t, CRDName(e1), CRDName(e2))
}

func TestToResult_AlwaysIncludesEmptyAutoRemediationStatus(t *testing.T) {
	m := New("k8sgpt-system")
	r := m.ToResult(
		&event.Event{InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p", Reason: "OOMKilled"},
		filter.Decision{Allow: true, Category: "pod_lifecycle", SignalType: "oom_kill", Severity: "critical"},
	)
	b, err := json.Marshal(r.Spec)
	require.NoError(t, err)
	require.Contains(t, string(b), `"autoRemediationStatus":{}`)
}

func TestToResult_NeverAppliesUpstreamLabels(t *testing.T) {
	m := New("k8sgpt-system")
	r := m.ToResult(
		&event.Event{InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p", Reason: "OOMKilled"},
		filter.Decision{Allow: true, Category: "pod_lifecycle", SignalType: "oom_kill", Severity: "critical"},
	)
	for k := range r.Labels {
		require.False(t, strings.HasPrefix(k, "k8sgpts.k8sgpt.ai/"), "must not apply upstream label: %s", k)
	}
}

// TestToResult_FaultClassLabelsForCatalogSignals validates that the new
// fault_class label appears on the well-bounded catalog signals (and that
// the leading hypothesis carries the 0.85 baseline confidence).
func TestToResult_FaultClassLabelsForCatalogSignals(t *testing.T) {
	cases := []struct {
		signalType     string
		reason         string
		wantFaultClass string
	}{
		{"oom_kill", "OOMKilled", FaultClassResourceExhaustion},
		{"evicted", "Evicted", FaultClassResourceExhaustion},
		{"image_pull_failure", "Failed", FaultClassBadDeployment},
		{"container_cannot_run", "ContainerCannotRun", FaultClassBadDeployment},
		{"failed_scheduling", "FailedScheduling", FaultClassSchedulingFailure},
		{"unschedulable", "Unschedulable", FaultClassSchedulingFailure},
		{"failed_mount", "FailedMount", FaultClassVolumeFailure},
		{"failed_attach_volume", "FailedAttachVolume", FaultClassVolumeFailure},
		{"provisioning_failed", "ProvisioningFailed", FaultClassVolumeFailure},
		{"back_off", "BackOff", FaultClassCrashLoop},
		{"container_error", "Error", FaultClassCrashLoop},
		{"cert_not_ready", "IssuerNotFound", FaultClassIndeterminate},
	}
	m := New("k8sgpt-system")
	for _, tc := range cases {
		t.Run(tc.signalType, func(t *testing.T) {
			e := &event.Event{
				InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p1",
				Reason: tc.reason, Note: "irrelevant", LastSeen: time.Now(),
			}
			d := filter.Decision{Allow: true, Category: "x", SignalType: tc.signalType, Severity: "warning"}
			r := m.ToResult(e, d)
			require.Equal(t, tc.wantFaultClass, r.Labels[labelFaultClass],
				"signal %s should label fault_class=%s", tc.signalType, tc.wantFaultClass)
			require.NotEmpty(t, r.Labels[labelEpisodeID], "episode_id label must be set when fault_class is set")

			// Confidence on the leading hypothesis in details.
			var details map[string]any
			require.NoError(t, json.Unmarshal([]byte(r.Spec.Details), &details))
			require.Equal(t, tc.wantFaultClass, details["fault_class"])
			require.Contains(t, details, "hypotheses")
			hyps := details["hypotheses"].([]any)
			require.NotEmpty(t, hyps)
			leading := hyps[0].(map[string]any)
			require.Equal(t, 0.85, leading["confidence"], "catalog signal %s leading confidence must be 0.85", tc.signalType)
		})
	}
}

// TestToResult_InfoTierSignalHasNoFaultClassOrEpisode preserves the
// graceful-degradation guarantee: info-tier signals (killing,
// volume_failed_delete) do NOT carry fault_class or episode_id labels.
func TestToResult_InfoTierSignalHasNoFaultClassOrEpisode(t *testing.T) {
	m := New("k8sgpt-system")
	e := &event.Event{InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p", Reason: "Killing"}
	d := filter.Decision{Allow: true, Category: "pod_lifecycle", SignalType: "killing", Severity: "info"}
	r := m.ToResult(e, d)
	_, hasFault := r.Labels[labelFaultClass]
	_, hasEpisode := r.Labels[labelEpisodeID]
	require.False(t, hasFault, "info-tier signals must not carry fault_class label")
	require.False(t, hasEpisode, "info-tier signals must not carry episode_id label")

	var details map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Spec.Details), &details))
	require.NotContains(t, details, "fault_class", "info-tier details must not include fault_class")
	require.NotContains(t, details, "episode_id", "info-tier details must not include episode_id")
}

// TestToResult_ProbeFailureWithCPUThrottleContextFlipsToResourceExhaustion
// validates the CPU-throttled probe timeout end-to-end through the mapper:
// the fault_class label flips from network_partition (legacy) to
// resource_exhaustion when the ProbeContext shows a tight CPU limit with
// observed throttle.
func TestToResult_ProbeFailureWithCPUThrottleContextFlipsToResourceExhaustion(t *testing.T) {
	lookup := func(ns, name string) probectx.ProbeContext {
		require.Equal(t, "checkout", ns)
		require.Equal(t, "checkout-7884b8b69f-gs68q", name)
		return probectx.ProbeContext{
			Available:          true,
			CPULimitMillicores: 50,
			CPUThrottleRate:    2.5,
		}
	}
	m := New("k8sgpt-system", WithProbeCtxLookup(lookup))
	e := &event.Event{
		InvolvedKind:      "Pod",
		InvolvedNamespace: "checkout",
		InvolvedName:      "checkout-7884b8b69f-gs68q",
		Reason:            "Unhealthy",
		Note:              `Readiness probe failed: Get "http://10.3.11.128:8080/health": timed out`,
		LastSeen:          time.Now(),
	}
	d := filter.Decision{Allow: true, Category: "pod_lifecycle", SignalType: "probe_failure", Severity: "warning"}
	r := m.ToResult(e, d)

	require.Equal(t, FaultClassResourceExhaustion, r.Labels[labelFaultClass])
	require.NotEmpty(t, r.Labels[labelEpisodeID])

	var details map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Spec.Details), &details))
	require.Equal(t, FaultClassResourceExhaustion, details["fault_class"])
	require.Equal(t, 0.85, details["confidence"])
	require.Contains(t, details, "probe")
	probe := details["probe"].(map[string]any)
	require.Equal(t, float64(50), probe["cpu_limit_millicores"])
	require.Equal(t, 2.5, probe["cpu_throttle_rate"])
}

func TestToResult_ProvisioningFailedPVCStorageFailure(t *testing.T) {
	m := New("k8sgpt-system")
	e := &event.Event{
		InvolvedKind:      "PersistentVolumeClaim",
		InvolvedNamespace: "storage-test",
		InvolvedName:      "unbound-pvc",
		InvolvedUID:       "uid-pvc-001",
		Reason:            "ProvisioningFailed",
		Note:              `storageclass.storage.k8s.io "missing-sc" not found`,
		Type:              "Warning",
		LastSeen:          time.Now(),
	}
	d := filter.Decision{Allow: true, Category: "storage", SignalType: "provisioning_failed", Severity: "warning"}

	r := m.ToResult(e, d)

	require.Equal(t, FaultClassVolumeFailure, r.Labels[labelFaultClass])
	require.NotEmpty(t, r.Labels[labelEpisodeID])
	require.Equal(t, "PersistentVolumeClaim", r.Spec.Kind)
	require.Equal(t, "storage-test/unbound-pvc", r.Spec.Name)

	// Enriched failure text includes the raw note and candidate causes.
	require.Contains(t, r.Spec.Error[0].Text, `"missing-sc" not found`)
	require.Contains(t, r.Spec.Error[0].Text, "Possible causes")
	require.Contains(t, r.Spec.Error[0].Text, "StorageClass that doesn't exist")

	var details map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Spec.Details), &details))
	require.Equal(t, FaultClassVolumeFailure, details["fault_class"])
	require.Contains(t, details, "hypotheses")
	hyps := details["hypotheses"].([]any)
	require.NotEmpty(t, hyps, "provisioning_failed must have candidate causes")
}
