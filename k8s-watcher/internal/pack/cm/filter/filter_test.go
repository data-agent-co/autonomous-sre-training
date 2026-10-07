// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package filter

import (
	"testing"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
	"github.com/stretchr/testify/require"
)

func TestApply_AllowsCriticalLabel(t *testing.T) {
	f := New()
	d := f.Apply(&mutation.Mutation{
		Labels: map[string]string{"k8sgpt-detection-pack.io/critical": "true"},
	})
	require.True(t, d.Allow)
}

func TestApply_RejectsMissingLabel(t *testing.T) {
	f := New()
	d := f.Apply(&mutation.Mutation{
		Labels: map[string]string{"app": "test"},
	})
	require.False(t, d.Allow)
}

func TestApply_RejectsWrongLabelValue(t *testing.T) {
	f := New()
	d := f.Apply(&mutation.Mutation{
		Labels: map[string]string{"k8sgpt-detection-pack.io/critical": "false"},
	})
	require.False(t, d.Allow)
}

func TestApply_NilMutationReturnsFalse(t *testing.T) {
	f := New()
	require.False(t, f.Apply(nil).Allow)
}
