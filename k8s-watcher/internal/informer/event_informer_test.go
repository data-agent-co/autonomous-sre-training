package informer_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/informer"
)

func warningEvent(name, namespace, reason, message, podName string, count int32) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        message,
		Count:          count,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: podName},
	}
}

// TestEventInformer_CrashLoopBackOff verifies a BackOff Warning event (CrashLoopBackOff)
// is delivered to the handler with correct fields.
func TestEventInformer_CrashLoopBackOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ev := warningEvent("my-pod.1", "default", "BackOff",
		"Back-off restarting failed container web in pod my-pod_default", "my-pod", 3)
	client := fake.NewClientset(ev)

	received := make(chan *corev1.Event, 1)
	err := informer.StartEventInformer(ctx, client, func(e *corev1.Event) {
		received <- e
	}, 0, testLogger(t))
	require.NoError(t, err)

	select {
	case got := <-received:
		assert.Equal(t, "BackOff", got.Reason)
		assert.Equal(t, corev1.EventTypeWarning, got.Type)
		assert.Equal(t, "my-pod", got.InvolvedObject.Name)
		assert.Equal(t, int32(3), got.Count)
	case <-ctx.Done():
		t.Fatal("timed out: CrashLoopBackOff event not received")
	}
}

// TestEventInformer_OOMKilling verifies an OOMKilling Warning event is captured.
func TestEventInformer_OOMKilling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ev := warningEvent("oom-pod.1", "kube-system", "OOMKilling",
		"Memory cgroup out of memory: killed process 1234 (app)", "oom-pod", 1)
	client := fake.NewClientset(ev)

	received := make(chan *corev1.Event, 1)
	err := informer.StartEventInformer(ctx, client, func(e *corev1.Event) {
		received <- e
	}, 0, testLogger(t))
	require.NoError(t, err)

	select {
	case got := <-received:
		assert.Equal(t, "OOMKilling", got.Reason)
		assert.Equal(t, "oom-pod", got.InvolvedObject.Name)
		assert.Contains(t, got.Message, "killed process")
	case <-ctx.Done():
		t.Fatal("timed out: OOMKilling event not received")
	}
}

// TestEventInformer_UnhealthyProbe verifies a failing readiness probe Warning event is captured.
func TestEventInformer_UnhealthyProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ev := warningEvent("api.readiness", "production", "Unhealthy",
		"Readiness probe failed: Get \"http://10.0.0.1:8080/ready\": connection refused", "api-server", 5)
	client := fake.NewClientset(ev)

	received := make(chan *corev1.Event, 1)
	err := informer.StartEventInformer(ctx, client, func(e *corev1.Event) {
		received <- e
	}, 0, testLogger(t))
	require.NoError(t, err)

	select {
	case got := <-received:
		assert.Equal(t, "Unhealthy", got.Reason)
		assert.Equal(t, int32(5), got.Count)
	case <-ctx.Done():
		t.Fatal("timed out: Unhealthy probe event not received")
	}
}

// TestEventInformer_MultipleErrors verifies that distinct Warning events for different
// pods are all delivered to the handler.
func TestEventInformer_MultipleErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := fake.NewClientset(
		warningEvent("pod-a.1", "ns1", "BackOff", "back-off restarting", "pod-a", 2),
		warningEvent("pod-b.1", "ns2", "OOMKilling", "oom killed", "pod-b", 1),
		warningEvent("pod-c.1", "ns1", "Unhealthy", "liveness probe failed", "pod-c", 7),
	)

	received := make(chan *corev1.Event, 10)
	err := informer.StartEventInformer(ctx, client, func(e *corev1.Event) {
		received <- e
	}, 0, testLogger(t))
	require.NoError(t, err)

	seen := make(map[string]bool)
	deadline := time.After(5 * time.Second)
	for len(seen) < 3 {
		select {
		case got := <-received:
			seen[got.InvolvedObject.Name] = true
		case <-deadline:
			t.Fatalf("timed out: only received events for %v, expected pod-a pod-b pod-c", seen)
		}
	}
	assert.True(t, seen["pod-a"])
	assert.True(t, seen["pod-b"])
	assert.True(t, seen["pod-c"])
}

// TestEventInformer_NewEventAfterSync verifies that events created after the informer
// has synced are still delivered to the handler. This mirrors events that occur after
// the watcher container starts up.
func TestEventInformer_NewEventAfterSync(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := fake.NewClientset()

	received := make(chan *corev1.Event, 1)
	err := informer.StartEventInformer(ctx, client, func(e *corev1.Event) {
		received <- e
	}, 0, testLogger(t))
	require.NoError(t, err)

	// Create the event after sync returns — the informer must still watch for new events.
	ev := warningEvent("post-sync.1", "production", "BackOff",
		"Back-off restarting failed container", "late-crasher", 1)
	_, err = client.CoreV1().Events("production").Create(ctx, ev, metav1.CreateOptions{})
	require.NoError(t, err)

	select {
	case got := <-received:
		assert.Equal(t, "BackOff", got.Reason)
		assert.Equal(t, "late-crasher", got.InvolvedObject.Name)
	case <-ctx.Done():
		t.Fatal("timed out: post-sync event not received")
	}
}

// testLogger routes slog output through t.Log so it shows only for failing
// tests (or with -v).
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
