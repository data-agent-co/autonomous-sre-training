package app_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/adapt"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/filter"
)

// mappedEvents has one Warning event per reason the events-watcher filter
// maps to a Result, shaped the way the cluster reports it. "Failed" appears
// twice because the filter routes it by note (image pull) and by involved
// kind (cert-manager).
var mappedEvents = []struct {
	reason, kind, note, wantSignal string
}{
	{"BackOff", "Pod", "Back-off restarting failed container", "back_off"},
	{"Killing", "Pod", "Stopping container web", "killing"},
	{"Unhealthy", "Pod", "Liveness probe failed", "probe_failure"},
	{"OOMKilled", "Pod", "Memory limit exceeded", "oom_kill"},
	{"Error", "Pod", "Error: container exited", "container_error"},
	{"ContainerCannotRun", "Pod", "exec format error", "container_cannot_run"},
	{"FailedScheduling", "Pod", "0/3 nodes are available", "failed_scheduling"},
	{"Unschedulable", "Pod", "pod is unschedulable", "unschedulable"},
	{"FailedMount", "Pod", "MountVolume.SetUp failed", "failed_mount"},
	{"FailedAttachVolume", "Pod", "AttachVolume.Attach failed", "failed_attach_volume"},
	{"VolumeFailedDelete", "PersistentVolume", "delete failed", "volume_failed_delete"},
	{"ProvisioningFailed", "PersistentVolumeClaim", "failed to provision volume", "provisioning_failed"},
	{"Evicted", "Pod", "The node was low on resource: memory", "evicted"},
	{"Failed", "Pod", "Failed to pull image \"nope\": ErrImagePull", "image_pull_failure"},
	{"Failed", "Certificate", "issuer unreachable", "cert_not_ready"},
}

// TestMappedReasonsReachTheFilter builds each mapped event as a core/v1
// Event two minutes old (past the FailedScheduling grace period, which is
// measured from FirstSeen), converts it the way the event source does, and
// runs the pipeline's filter on the result.
func TestMappedReasonsReachTheFilter(t *testing.T) {
	firstSeen := time.Now().Add(-2 * time.Minute)
	for _, tc := range mappedEvents {
		t.Run(tc.reason+"/"+tc.kind, func(t *testing.T) {
			core := &corev1.Event{
				InvolvedObject: corev1.ObjectReference{Kind: tc.kind, Namespace: "ns", Name: "obj"},
				Reason:         tc.reason,
				Message:        tc.note,
				Type:           corev1.EventTypeWarning,
				FirstTimestamp: metav1.NewTime(firstSeen),
				LastTimestamp:  metav1.NewTime(time.Now()),
			}
			d := filter.New().Apply(adapt.EventFromCore(core))
			assert.True(t, d.Allow, "the filter must admit %s on %s", tc.reason, tc.kind)
			assert.Equal(t, tc.wantSignal, d.SignalType)
		})
	}
}

// TestFailedSchedulingGraceUsesFirstTimestamp pins why FirstSeen must not
// fall back to the latest occurrence: a FailedScheduling series that started
// long ago but repeated just now must still clear the grace period.
func TestFailedSchedulingGraceUsesFirstTimestamp(t *testing.T) {
	core := &corev1.Event{
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "ns", Name: "pending"},
		Reason:         "FailedScheduling",
		Type:           corev1.EventTypeWarning,
		FirstTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
		LastTimestamp:  metav1.NewTime(time.Now()),
		Count:          9,
	}
	assert.True(t, filter.New().Apply(adapt.EventFromCore(core)).Allow)

	core.FirstTimestamp = metav1.NewTime(time.Now().Add(-5 * time.Second))
	assert.False(t, filter.New().Apply(adapt.EventFromCore(core)).Allow,
		"a 5s-old FailedScheduling is inside the grace period")
}
