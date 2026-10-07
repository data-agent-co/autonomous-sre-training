package adapt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
)

var (
	t0 = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC) // creationTimestamp
	t1 = t0.Add(1 * time.Minute)                      // eventTime
	t2 = t0.Add(2 * time.Minute)                      // firstTimestamp
	t3 = t0.Add(3 * time.Minute)                      // lastTimestamp
	t4 = t0.Add(4 * time.Minute)                      // series.lastObservedTime
)

// stamped builds an event carrying only the timestamps whose flags are set.
func stamped(eventTime, first, last, series, created bool) *corev1.Event {
	e := &corev1.Event{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p"}}
	if created {
		e.CreationTimestamp = metav1.NewTime(t0)
	}
	if eventTime {
		e.EventTime = metav1.NewMicroTime(t1)
	}
	if first {
		e.FirstTimestamp = metav1.NewTime(t2)
	}
	if last {
		e.LastTimestamp = metav1.NewTime(t3)
	}
	if series {
		e.Series = &corev1.EventSeries{Count: 7, LastObservedTime: metav1.NewMicroTime(t4)}
	}
	return e
}

func TestEventFromCore_TimestampFallbacks(t *testing.T) {
	tests := []struct {
		name          string
		event         *corev1.Event
		wantFirstSeen time.Time
		wantLastSeen  time.Time
	}{
		{"all fields set", stamped(true, true, true, true, true), t1, t4},
		{"no eventTime (legacy writer)", stamped(false, true, true, true, true), t2, t4},
		{"no series", stamped(true, true, true, false, true), t1, t3},
		{"legacy timestamps only", stamped(false, true, true, false, true), t2, t3},
		{"eventTime only", stamped(true, false, false, false, true), t1, t1},
		{"series only", stamped(false, false, false, true, true), t4, t4},
		{"lastTimestamp only", stamped(false, false, true, false, true), t3, t3},
		{"firstTimestamp only", stamped(false, true, false, false, true), t2, t2},
		{"creationTimestamp only", stamped(false, false, false, false, true), t0, t0},
		{"nothing at all", stamped(false, false, false, false, false), time.Time{}, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EventFromCore(tt.event)
			require.NotNil(t, got)
			assert.True(t, got.FirstSeen.Equal(tt.wantFirstSeen), "FirstSeen = %v, want %v", got.FirstSeen, tt.wantFirstSeen)
			assert.True(t, got.LastSeen.Equal(tt.wantLastSeen), "LastSeen = %v, want %v", got.LastSeen, tt.wantLastSeen)
		})
	}
}

func TestEventFromCore_Fields(t *testing.T) {
	e := &corev1.Event{
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "shop", Name: "cart-1", UID: "uid-9"},
		Reason:         "BackOff",
		Message:        "Back-off restarting failed container",
		Type:           corev1.EventTypeWarning,
		Source:         corev1.EventSource{Component: "kubelet"},
		Count:          3,
	}
	got := EventFromCore(e)
	require.NotNil(t, got)
	assert.Equal(t, event.Event{
		InvolvedNamespace:   "shop",
		InvolvedName:        "cart-1",
		InvolvedKind:        "Pod",
		InvolvedUID:         "uid-9",
		Reason:              "BackOff",
		Note:                "Back-off restarting failed container",
		Type:                "Warning",
		ReportingController: "kubelet",
		Count:               3,
	}, *got)

	t.Run("reportingComponent wins over source.component", func(t *testing.T) {
		e := e.DeepCopy()
		e.ReportingController = "default-scheduler"
		assert.Equal(t, "default-scheduler", EventFromCore(e).ReportingController)
	})
	t.Run("series count wins over count", func(t *testing.T) {
		e := e.DeepCopy()
		e.Series = &corev1.EventSeries{Count: 12}
		assert.Equal(t, int32(12), EventFromCore(e).Count)
	})
	t.Run("no involved object name", func(t *testing.T) {
		e := e.DeepCopy()
		e.InvolvedObject.Name = ""
		assert.Nil(t, EventFromCore(e))
	})
	t.Run("nil", func(t *testing.T) {
		assert.Nil(t, EventFromCore(nil))
	})
}

// warningEvents is a sample of the Warning events the events-watcher
// filter maps, one per involved kind it sees.
var warningEvents = []struct{ reason, kind string }{
	{"BackOff", "Pod"},
	{"VolumeFailedDelete", "PersistentVolume"},
	{"ProvisioningFailed", "PersistentVolumeClaim"},
	{"Failed", "Certificate"},
}

// TestEventSourceDeliversToHandler drives the source against a fake
// clientset. The fake ignores field selectors, so this proves delivery and
// conversion, not the Warning-only watch (the informer package's selector
// test covers the selector itself).
func TestEventSourceDeliversToHandler(t *testing.T) {
	client := fake.NewClientset()
	for i, tc := range warningEvents {
		_, err := client.CoreV1().Events("ns").Create(context.Background(), &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "ev-" + string(rune('a'+i)), Namespace: "ns"},
			InvolvedObject: corev1.ObjectReference{Kind: tc.kind, Namespace: "ns", Name: "obj"},
			Reason:         tc.reason,
			Type:           corev1.EventTypeWarning,
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var mu sync.Mutex
	got := map[string]bool{}
	src := &EventSource{Client: client, WatchPods: true}
	done := make(chan error, 1)
	go func() {
		done <- src.Run(ctx, func(_ context.Context, e *event.Event) {
			mu.Lock()
			got[e.Reason] = true
			mu.Unlock()
		})
	}()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, tc := range warningEvents {
			if !got[tc.reason] {
				return false
			}
		}
		return true
	}, 5*time.Second, 20*time.Millisecond)
	assert.NotNil(t, src.PodLister(), "the pod cache syncs before the event watch starts")

	cancel()
	require.NoError(t, <-done)
}
