// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package mapper

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
	"github.com/stretchr/testify/require"
)

func TestToResult_ConfigMapMutationPopulatesAllFields(t *testing.T) {
	m := New("k8sgpt-system")
	mut := &mutation.Mutation{
		Kind:            mutation.KindConfigMap,
		Op:              mutation.OpUpdate,
		Namespace:       "payments",
		Name:            "app-config",
		UID:             "uid-cm-abc",
		ResourceVersion: "1234",
		Labels:          map[string]string{"k8sgpt-detection-pack.io/critical": "true"},
		PrevData:        map[string]string{"db": "old", "keep": "same"},
		Data:            map[string]string{"db": "new", "keep": "same"},
		ObservedAt:      time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC),
	}
	r := m.ToResult(mut)
	require.Equal(t, "k8sgpt-system", r.Namespace)
	require.Equal(t, "ConfigMap", r.Spec.Kind)
	require.Equal(t, "payments/app-config", r.Spec.Name)
	require.Equal(t, BackendIdentifier, r.Spec.Backend)
	require.Equal(t, "cm", r.Labels[LabelComponent])
	require.Equal(t, "configmap", r.Labels["k8sgpt-detection-pack.io/source"])
	require.Equal(t, "configuration_fault", r.Labels["k8sgpt-detection-pack.io/category"])
	require.Equal(t, "cm_mutation", r.Labels["k8sgpt-detection-pack.io/signal_type"])
	require.Equal(t, "config_drift", r.Labels["k8sgpt-detection-pack.io/fault_class"],
		"CM mutations are the canonical config_drift signal — downstream RCA routes on this label")

	var details map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Spec.Details), &details))
	require.Equal(t, false, details["redacted"])
}

func TestToResult_DeleteOpUsesDeletedSignalType(t *testing.T) {
	m := New("k8sgpt-system")
	mut := &mutation.Mutation{
		Kind:      mutation.KindConfigMap,
		Op:        mutation.OpDelete,
		Namespace: "default",
		Name:      "removed-config",
		Labels:    map[string]string{"k8sgpt-detection-pack.io/critical": "true"},
	}
	r := m.ToResult(mut)
	require.Equal(t, "cm_mutation_deleted", r.Labels["k8sgpt-detection-pack.io/signal_type"])
}

func TestToResult_DeleteReportsEveryKeyRemoved(t *testing.T) {
	m := New("k8sgpt-system")
	r := m.ToResult(&mutation.Mutation{
		Kind:      mutation.KindConfigMap,
		Op:        mutation.OpDelete,
		Namespace: "default",
		Name:      "removed-config",
		Labels:    map[string]string{"k8sgpt-detection-pack.io/critical": "true"},
		PrevData:  map[string]string{"a": "1:6b86b273ff34fce1", "b": "1:d4735e3a265e16ee"},
	})
	require.Equal(t, "ConfigMap default/removed-config deleted: 0 added, 2 removed, 0 changed", r.Spec.Error[0].Text)
}

func TestCRDName_Deterministic(t *testing.T) {
	mut := &mutation.Mutation{
		Kind: mutation.KindConfigMap, Namespace: "ns", Name: "cfg-1", UID: "uid-1",
	}
	require.Equal(t, CRDName(mut), CRDName(mut))
	require.Regexp(t, `^cm-configmap-cfg-1-[a-f0-9]{8}$`, CRDName(mut))
}

func TestToResult_AlwaysIncludesEmptyAutoRemediationStatus(t *testing.T) {
	m := New("k8sgpt-system")
	r := m.ToResult(&mutation.Mutation{
		Kind: mutation.KindConfigMap, Namespace: "ns", Name: "x", Labels: map[string]string{"k8sgpt-detection-pack.io/critical": "true"},
	})
	b, _ := json.Marshal(r.Spec)
	require.Contains(t, string(b), `"autoRemediationStatus":{}`)
}

func TestToResult_NeverAppliesUpstreamLabels(t *testing.T) {
	m := New("k8sgpt-system")
	r := m.ToResult(&mutation.Mutation{
		Kind: mutation.KindConfigMap, Namespace: "ns", Name: "x", Labels: map[string]string{"k8sgpt-detection-pack.io/critical": "true"},
	})
	for k := range r.Labels {
		require.False(t, strings.HasPrefix(k, "k8sgpts.k8sgpt.ai/"), "must not apply upstream label: %s", k)
	}
}
