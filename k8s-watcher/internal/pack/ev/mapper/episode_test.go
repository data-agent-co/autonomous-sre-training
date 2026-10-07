// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package mapper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
)

func TestEpisodeID_StableForSameInput(t *testing.T) {
	e := &event.Event{
		InvolvedKind:      "Pod",
		InvolvedNamespace: "checkout",
		InvolvedName:      "checkout-7884b8b69f-gs68q",
	}
	now := time.Date(2026, 5, 28, 10, 13, 0, 0, time.UTC)
	a := episodeID(e, FaultClassResourceExhaustion, now)
	b := episodeID(e, FaultClassResourceExhaustion, now)
	require.Equal(t, a, b)
	require.Len(t, a, 16)
}

// Same workload + time bucket but different fault_class → distinct
// episodes. This is the rule that lets a consumer grouping by episode_id
// keep two concurrent fault classes on the same workload as two incidents.
func TestEpisodeID_DiffersAcrossFaultClass(t *testing.T) {
	e := &event.Event{
		InvolvedKind:      "Pod",
		InvolvedNamespace: "checkout",
		InvolvedName:      "checkout-7884b8b69f-gs68q",
	}
	now := time.Date(2026, 5, 28, 10, 13, 0, 0, time.UTC)
	a := episodeID(e, FaultClassResourceExhaustion, now)
	b := episodeID(e, FaultClassCrashLoop, now)
	require.NotEqual(t, a, b)
}

// Events within the same 5-minute bucket hash to the same episode;
// events across the bucket boundary do not. The exact bucket size is
// part of the contract: a consumer that closes out stale episodes
// depends on it.
func TestEpisodeID_TimeBucketing(t *testing.T) {
	e := &event.Event{InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p1"}
	t1 := time.Date(2026, 5, 28, 10, 13, 12, 0, time.UTC)
	t2 := time.Date(2026, 5, 28, 10, 14, 59, 0, time.UTC)
	t3 := time.Date(2026, 5, 28, 10, 16, 0, 0, time.UTC) // next bucket
	a := episodeID(e, FaultClassCrashLoop, t1)
	b := episodeID(e, FaultClassCrashLoop, t2)
	c := episodeID(e, FaultClassCrashLoop, t3)
	require.Equal(t, a, b, "events in the same 5m bucket must share an episode")
	require.NotEqual(t, a, c, "events in different 5m buckets must NOT share an episode")
}

func TestEpisodeID_EmptyFaultClassReturnsEmpty(t *testing.T) {
	e := &event.Event{InvolvedKind: "Pod", InvolvedNamespace: "ns", InvolvedName: "p1"}
	require.Equal(t, "", episodeID(e, "", time.Now()))
}

func TestWorkloadRoot(t *testing.T) {
	cases := []struct {
		kind string
		name string
		want string
	}{
		// Deployment pod (ReplicaSet pod-template-hash + per-pod suffix).
		{"Pod", "checkout-7884b8b69f-gs68q", "checkout"},
		// StatefulSet ordinal.
		{"Pod", "postgres-0", "postgres"},
		{"Pod", "postgres-12", "postgres"},
		// Plain pod name (no recognizable suffix).
		{"Pod", "checkout", "checkout"},
		// Pod with only the per-pod suffix (e.g. Job pods).
		{"Pod", "checkout-gs68q", "checkout"},
		// Non-Pod kinds pass through unchanged.
		{"Deployment", "checkout", "checkout"},
		{"Node", "ip-10-0-0-1", "ip-10-0-0-1"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.name, func(t *testing.T) {
			e := &event.Event{InvolvedKind: tc.kind, InvolvedName: tc.name}
			require.Equal(t, tc.want, workloadRoot(e))
		})
	}
}
