// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package filter

import (
	"testing"
	"time"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/stretchr/testify/require"
)

func TestApply_WarningOOMKilledAllowed(t *testing.T) {
	f := New()
	d := f.Apply(&event.Event{
		Type:      "Warning",
		Reason:    "OOMKilled",
		FirstSeen: time.Now(),
	})
	require.True(t, d.Allow)
	require.Equal(t, "pod_lifecycle", d.Category)
	require.Equal(t, "oom_kill", d.SignalType)
	require.Equal(t, "critical", d.Severity)
}

func TestApply_NormalEventRejected(t *testing.T) {
	f := New()
	d := f.Apply(&event.Event{
		Type:   "Normal",
		Reason: "Pulled",
	})
	require.False(t, d.Allow, "Normal events must never produce Results")
}

func TestApply_UnknownReasonRejected(t *testing.T) {
	f := New()
	d := f.Apply(&event.Event{
		Type:   "Warning",
		Reason: "SomeOperatorSpecificReason",
	})
	require.False(t, d.Allow, "unknown reasons must not produce Results")
}

func TestApply_FailedSchedulingGraceWindow(t *testing.T) {
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	f := New()
	f.Now = func() time.Time { return now }

	// FailedScheduling within the grace window — suppressed.
	d := f.Apply(&event.Event{
		Type:      "Warning",
		Reason:    "FailedScheduling",
		FirstSeen: now.Add(-30 * time.Second),
	})
	require.False(t, d.Allow, "FailedScheduling within grace window must be suppressed")

	// Same event past the grace window — allowed.
	d = f.Apply(&event.Event{
		Type:      "Warning",
		Reason:    "FailedScheduling",
		FirstSeen: now.Add(-2 * time.Minute),
	})
	require.True(t, d.Allow)
	require.Equal(t, "scheduling", d.Category)
	require.Equal(t, "failed_scheduling", d.SignalType)
}

func TestApply_FailedReason_ImagePullDetection(t *testing.T) {
	f := New()

	// "Failed" event with image-pull-related note → allowed as
	// image_pull_failure.
	d := f.Apply(&event.Event{
		Type:   "Warning",
		Reason: "Failed",
		Note:   "Failed to pull image quay.io/example/nope: ErrImagePull",
	})
	require.True(t, d.Allow)
	require.Equal(t, "image_pull_failure", d.SignalType)

	// "Failed" without image-pull markers → not allowed (the generic
	// "Failed" reason is ambiguous; we only act on shapes we can attribute).
	d = f.Apply(&event.Event{
		Type:   "Warning",
		Reason: "Failed",
		Note:   "some other reason for failure",
	})
	require.False(t, d.Allow)
}

func TestApply_StorageReasonAllowed(t *testing.T) {
	f := New()
	d := f.Apply(&event.Event{
		Type:   "Warning",
		Reason: "FailedMount",
	})
	require.True(t, d.Allow)
	require.Equal(t, "storage", d.Category)
	require.Equal(t, "failed_mount", d.SignalType)
}

func TestApply_ProvisioningFailedAllowed(t *testing.T) {
	f := New()
	d := f.Apply(&event.Event{
		Type:   "Warning",
		Reason: "ProvisioningFailed",
		Note:   `storageclass.storage.k8s.io "missing-sc" not found`,
	})
	require.True(t, d.Allow)
	require.Equal(t, "storage", d.Category)
	require.Equal(t, "provisioning_failed", d.SignalType)
	require.Equal(t, "warning", d.Severity)
}

func TestApply_EvictedAllowed(t *testing.T) {
	f := New()
	d := f.Apply(&event.Event{
		Type:   "Warning",
		Reason: "Evicted",
	})
	require.True(t, d.Allow)
	require.Equal(t, "eviction", d.Category)
}

func TestApply_IssuerNotFoundAllowed(t *testing.T) {
	// cert-manager emits IssuerNotFound as Normal type, not Warning.
	f := New()
	d := f.Apply(&event.Event{
		Type:         "Normal",
		Reason:       "IssuerNotFound",
		InvolvedKind: "CertificateRequest",
		Note:         `issuer.cert-manager.io "missing-issuer" not found`,
	})
	require.True(t, d.Allow)
	require.Equal(t, "tls_certs", d.Category)
	require.Equal(t, "cert_not_ready", d.SignalType)
	require.Equal(t, "warning", d.Severity)
}

func TestApply_FailedOnCertificateAllowed(t *testing.T) {
	f := New()
	d := f.Apply(&event.Event{
		Type:         "Warning",
		Reason:       "Failed",
		InvolvedKind: "Certificate",
		Note:         `error issuing certificate: issuer not ready`,
	})
	require.True(t, d.Allow)
	require.Equal(t, "tls_certs", d.Category)
	require.Equal(t, "cert_not_ready", d.SignalType)
	require.Equal(t, "warning", d.Severity)
}

func TestApply_FailedOnCertificateRequestAllowed(t *testing.T) {
	f := New()
	d := f.Apply(&event.Event{
		Type:         "Warning",
		Reason:       "Failed",
		InvolvedKind: "CertificateRequest",
		Note:         `issuer not ready`,
	})
	require.True(t, d.Allow)
	require.Equal(t, "cert_not_ready", d.SignalType)
}

func TestApply_FailedOnPodStillRoutesToImagePull(t *testing.T) {
	f := New()
	d := f.Apply(&event.Event{
		Type:         "Warning",
		Reason:       "Failed",
		InvolvedKind: "Pod",
		Note:         `Failed to pull image "gcr.io/bad/image:latest": ErrImagePull`,
	})
	require.True(t, d.Allow)
	require.Equal(t, "image_pull_failure", d.SignalType, "Pod+Failed+image-pull note must still map to image_pull_failure")
}

func TestApply_NilEventReturnsZeroDecision(t *testing.T) {
	f := New()
	require.False(t, f.Apply(nil).Allow)
}
