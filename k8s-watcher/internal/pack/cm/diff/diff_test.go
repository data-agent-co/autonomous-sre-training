// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package diff

import (
	"testing"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
	"github.com/stretchr/testify/require"
)

func TestCompute_ConfigMapAddedRemovedChanged(t *testing.T) {
	m := &mutation.Mutation{
		Kind: mutation.KindConfigMap,
		PrevData: map[string]string{
			"unchanged": "same",
			"removed":   "gone",
			"updated":   "old",
		},
		Data: map[string]string{
			"unchanged": "same",
			"updated":   "new",
			"added":     "fresh",
		},
	}
	r := Compute(m)
	require.Equal(t, 1, r.AddedCount)
	require.Equal(t, 1, r.RemovedCount)
	require.Equal(t, 1, r.ChangedCount)
	require.Len(t, r.Entries, 3)

	byKey := map[string]Entry{}
	for _, e := range r.Entries {
		byKey[e.Key] = e
	}
	require.Equal(t, Added, byKey["added"].Change)
	require.Equal(t, "fresh", byKey["added"].NewValue)
	require.Equal(t, Removed, byKey["removed"].Change)
	require.Equal(t, "gone", byKey["removed"].OldValue)
	require.Equal(t, Changed, byKey["updated"].Change)
	require.Equal(t, "old", byKey["updated"].OldValue)
	require.Equal(t, "new", byKey["updated"].NewValue)
}

func TestCompute_AddOpHasNoPrevData(t *testing.T) {
	m := &mutation.Mutation{
		Kind: mutation.KindConfigMap,
		Op:   mutation.OpAdd,
		Data: map[string]string{
			"key1": "v1",
			"key2": "v2",
		},
	}
	r := Compute(m)
	require.Equal(t, 2, r.AddedCount)
	require.Equal(t, 0, r.RemovedCount)
	require.Equal(t, 0, r.ChangedCount)
}

func TestCompute_NoOpReturnsEmpty(t *testing.T) {
	m := &mutation.Mutation{
		Kind:     mutation.KindConfigMap,
		PrevData: map[string]string{"k": "v"},
		Data:     map[string]string{"k": "v"},
	}
	r := Compute(m)
	require.Empty(t, r.Entries)
	require.Zero(t, r.AddedCount)
}

func TestCompute_NilMutationReturnsZero(t *testing.T) {
	require.Empty(t, Compute(nil).Entries)
}

// TestCompute_BinaryDataAlongsideData changes a BinaryData key on a
// ConfigMap that also has Data. Both maps are diffed: a ConfigMap may use
// both, and their keys never overlap.
func TestCompute_BinaryDataAlongsideData(t *testing.T) {
	m := &mutation.Mutation{
		Kind:           mutation.KindConfigMap,
		PrevData:       map[string]string{"mode": "1:aaaaaaaaaaaaaaaa"},
		PrevBinaryData: map[string][]byte{"cert.der": []byte("4:bbbbbbbbbbbbbbbb"), "old.bin": []byte("2:cccccccccccccccc")},
		Data:           map[string]string{"mode": "1:aaaaaaaaaaaaaaaa"},
		BinaryData:     map[string][]byte{"cert.der": []byte("5:dddddddddddddddd"), "new.bin": []byte("3:eeeeeeeeeeeeeeee")},
	}
	r := Compute(m)
	require.Equal(t, 1, r.AddedCount)
	require.Equal(t, 1, r.RemovedCount)
	require.Equal(t, 1, r.ChangedCount)

	byKey := map[string]Entry{}
	for _, e := range r.Entries {
		byKey[e.Key] = e
	}
	require.Equal(t, Changed, byKey["cert.der"].Change)
	require.Equal(t, Added, byKey["new.bin"].Change)
	require.Equal(t, Removed, byKey["old.bin"].Change)
	require.NotContains(t, byKey, "mode")
}

// TestCompute_LengthsAreValueLengths checks that OldLen / NewLen give the
// length of the value, not of the "<len>:<sha16>" digest that stands in for
// it. A value that is not a digest is measured as is.
func TestCompute_LengthsAreValueLengths(t *testing.T) {
	m := &mutation.Mutation{
		Kind:           mutation.KindConfigMap,
		PrevData:       map[string]string{"timeout": "2:0123456789abcdef", "raw": "abc"},
		Data:           map[string]string{"timeout": "1024:fedcba9876543210", "raw": "abcdef"},
		PrevBinaryData: map[string][]byte{"blob": []byte("70000:0011223344556677")},
	}
	byKey := map[string]Entry{}
	for _, e := range Compute(m).Entries {
		byKey[e.Key] = e
	}
	require.Equal(t, 2, byKey["timeout"].OldLen)
	require.Equal(t, 1024, byKey["timeout"].NewLen)
	require.Equal(t, 3, byKey["raw"].OldLen)
	require.Equal(t, 6, byKey["raw"].NewLen)
	require.Equal(t, 70000, byKey["blob"].OldLen)
	require.Equal(t, Removed, byKey["blob"].Change)
}
