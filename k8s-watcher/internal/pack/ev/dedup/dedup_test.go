// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package dedup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShouldEmit_FirstSightingReturnsTrue(t *testing.T) {
	c := New(5 * time.Minute)
	require.True(t, c.ShouldEmit(Key{InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "p1", Reason: "OOMKilled"}))
}

func TestShouldEmit_DuplicateWithinWindowReturnsFalse(t *testing.T) {
	c := New(5 * time.Minute)
	k := Key{InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "p1", Reason: "OOMKilled"}
	require.True(t, c.ShouldEmit(k))
	require.False(t, c.ShouldEmit(k), "second call within window must be suppressed")
	require.False(t, c.ShouldEmit(k))
}

func TestShouldEmit_ReEmitsAfterWindow(t *testing.T) {
	c := New(5 * time.Minute)
	now := time.Now()
	c.now = func() time.Time { return now }

	k := Key{InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "p1", Reason: "BackOff"}
	require.True(t, c.ShouldEmit(k))

	// Advance time past the window.
	now = now.Add(6 * time.Minute)
	require.True(t, c.ShouldEmit(k), "after window expiry, re-emission must be allowed")
}

func TestShouldEmit_DifferentKeysAreIndependent(t *testing.T) {
	c := New(5 * time.Minute)
	k1 := Key{InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "p1", Reason: "BackOff"}
	k2 := Key{InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "p1", Reason: "OOMKilled"}
	k3 := Key{InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "p2", Reason: "BackOff"}

	require.True(t, c.ShouldEmit(k1))
	require.True(t, c.ShouldEmit(k2), "same pod, different reason — not a dup")
	require.True(t, c.ShouldEmit(k3), "different pod — not a dup")
}

func TestSweepStale_RemovesAgedEntries(t *testing.T) {
	c := New(5 * time.Minute)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	c.ShouldEmit(Key{InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "fresh", Reason: "BackOff"})

	// Advance time and add a fresh one.
	now = now.Add(10 * time.Minute)
	c.ShouldEmit(Key{InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "newer", Reason: "BackOff"})

	require.Len(t, c.seen, 2)
	removed := c.SweepStale()
	require.Len(t, removed, 1, "the 10-minute-old entry should be swept")
	require.Equal(t, "fresh", removed[0].InvolvedName)
	require.Len(t, c.seen, 1)
}

func TestKeyString_StableFormat(t *testing.T) {
	k := Key{InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "p1", Reason: "OOMKilled"}
	require.Equal(t, "Pod/default/p1/OOMKilled", k.String())
}
