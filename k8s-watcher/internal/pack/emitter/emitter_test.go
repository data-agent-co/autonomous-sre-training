// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package emitter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	corev1alpha1 "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/k8sgpt/v1alpha1"
)

// newFakeClient returns a fake dynamic client that already holds initial.
func newFakeClient(t *testing.T, initial ...*corev1alpha1.Result) *dynamicfake.FakeDynamicClient {
	t.Helper()
	objs := make([]runtime.Object, 0, len(initial))
	for _, r := range initial {
		u, err := toUnstructured(r)
		require.NoError(t, err)
		objs = append(objs, u)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{corev1alpha1.GroupVersionResource: "ResultList"}, objs...)
}

// getResult reads a Result back from c.
func getResult(t *testing.T, c dynamic.Interface, ns, name string) *corev1alpha1.Result {
	t.Helper()
	u, err := c.Resource(corev1alpha1.GroupVersionResource).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "core.k8sgpt.ai/v1alpha1", u.GetAPIVersion())
	require.Equal(t, "Result", u.GetKind())
	var r corev1alpha1.Result
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &r))
	return &r
}

func mkResult(name, ns, specName string) *corev1alpha1.Result {
	return &corev1alpha1.Result{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"k8sgpt-detection-pack.io/component": "ev"}},
		Spec: corev1alpha1.ResultSpec{
			Backend:               "k8sgpt-detection-pack:ev",
			AutoRemediationStatus: corev1alpha1.AutoRemediationStatus{},
			Kind:                  "Pod",
			Name:                  specName,
			Error:                 []corev1alpha1.Failure{{Text: "OOMKilled"}},
			Details:               `{}`,
		},
	}
}

func TestClient_EmitCreatesNewResult(t *testing.T) {
	c := newFakeClient(t)
	e := New(c, SameEvent, quietLog())
	res := mkResult("ev-oomkilled-p1-abcdef12", "k8sgpt-system", "default/p1")
	require.NoError(t, e.Emit(context.Background(), res))

	got := getResult(t, c, res.Namespace, res.Name)
	require.Equal(t, "default/p1", got.Spec.Name)
	require.Equal(t, "1", got.Annotations[annoCount])
}

func TestClient_EmitUpdatesExistingResult(t *testing.T) {
	original := mkResult("ev-oomkilled-p1-abcdef12", "k8sgpt-system", "default/p1-old")
	c := newFakeClient(t, original)
	e := New(c, SameEvent, quietLog())

	updated := mkResult("ev-oomkilled-p1-abcdef12", "k8sgpt-system", "default/p1-new")
	require.NoError(t, e.Emit(context.Background(), updated))

	got := getResult(t, c, updated.Namespace, updated.Name)
	require.Equal(t, "default/p1-new", got.Spec.Name, "second Emit should update spec")
	require.Equal(t, "1", got.Annotations[annoCount], "original had no count; the update starts it at 1")
}

// TestClient_SameEventIgnoresDetails pins the EV gate: Details carries the
// Event series' lastSeen and count, so an event that repeats with the same
// Error text is no change.
func TestClient_SameEventIgnoresDetails(t *testing.T) {
	c := newFakeClient(t)
	e := New(c, SameEvent, quietLog())
	ctx := context.Background()

	first := mkResult("ev-oomkilled-p1-abcdef12", "k8sgpt-system", "default/p1")
	first.Spec.Details = `{"count":1,"lastSeen":"2026-05-21T10:00:00Z"}`
	require.NoError(t, e.Emit(ctx, first))

	again := mkResult("ev-oomkilled-p1-abcdef12", "k8sgpt-system", "default/p1")
	again.Spec.Details = `{"count":2,"lastSeen":"2026-05-21T10:07:00Z"}`
	require.ErrorIs(t, e.Emit(ctx, again), ErrNoChange)

	got := getResult(t, c, "k8sgpt-system", "ev-oomkilled-p1-abcdef12")
	require.Equal(t, first.Spec.Details, got.Spec.Details)
	require.Equal(t, "1", got.Annotations[annoCount])
}

// cmResult is a Result as the CM pipeline builds it.
func cmResult(name, ns, specName string) *corev1alpha1.Result {
	return &corev1alpha1.Result{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"k8sgpt-detection-pack.io/component": "cm"}},
		Spec: corev1alpha1.ResultSpec{
			Backend:               "k8sgpt-detection-pack:cm",
			AutoRemediationStatus: corev1alpha1.AutoRemediationStatus{},
			Kind:                  "ConfigMap",
			Name:                  specName,
			Error:                 []corev1alpha1.Failure{{Text: "mutation"}},
			Details:               `{}`,
		},
	}
}

func TestClient_EmitCreatesAndUpdates(t *testing.T) {
	c := newFakeClient(t)
	e := New(c, SameChange, quietLog())

	// First call: Create.
	require.NoError(t, e.Emit(context.Background(), cmResult("cm-cm-x-abc12345", "k8sgpt-system", "default/x")))

	// Second call: Update with new spec name.
	require.NoError(t, e.Emit(context.Background(), cmResult("cm-cm-x-abc12345", "k8sgpt-system", "default/x-updated")))

	got := getResult(t, c, "k8sgpt-system", "cm-cm-x-abc12345")
	require.Equal(t, "default/x-updated", got.Spec.Name)
	require.Equal(t, "2", got.Annotations[annoCount])
}

// changeResult is a cm_mutation Result for one edit of key "timeout", the
// way the mapper builds it: the Error text only counts keys, and Details
// carries the per-key diff plus fields that differ on every delivery.
func changeResult(oldDigest, newDigest, resourceVersion string) *corev1alpha1.Result {
	r := cmResult("cm-configmap-x-abc12345", "k8sgpt-system", "default/x")
	r.Spec.Error = []corev1alpha1.Failure{{Text: "ConfigMap default/x modified: 0 added, 0 removed, 1 changed"}}
	r.Spec.Details = fmt.Sprintf(`{"diff":{"entries":[{"key":"timeout","change":"changed","old_value":%q,"new_value":%q}],"added_count":0,"removed_count":0,"changed_count":1},"observedAt":%q,"resourceVersion":%q}`,
		oldDigest, newDigest, time.Now().UTC().Format(time.RFC3339Nano), resourceVersion)
	return r
}

// TestClient_SecondEditOfSameKeyUpdates edits the same key twice. Both edits
// read "1 changed", so a gate that compares only the Error text dropped the
// second as no-change and left the Result describing the first.
func TestClient_SecondEditOfSameKeyUpdates(t *testing.T) {
	c := newFakeClient(t)
	e := New(c, SameChange, quietLog())
	ctx := context.Background()

	require.NoError(t, e.Emit(ctx, changeResult("2:aaaaaaaaaaaaaaaa", "3:bbbbbbbbbbbbbbbb", "10")))
	require.NoError(t, e.Emit(ctx, changeResult("3:bbbbbbbbbbbbbbbb", "3:cccccccccccccccc", "11")),
		"a second edit of the same key is a change")

	got := getResult(t, c, "k8sgpt-system", "cm-configmap-x-abc12345")
	require.Contains(t, got.Spec.Details, "3:cccccccccccccccc", "Result shows the latest edit")
	require.Equal(t, "2", got.Annotations[annoCount])
}

// TestClient_RedeliveryIsNoChange keeps the suppression gate: the same diff
// delivered again differs only in observedAt and resourceVersion.
func TestClient_RedeliveryIsNoChange(t *testing.T) {
	c := newFakeClient(t)
	e := New(c, SameChange, quietLog())
	ctx := context.Background()

	require.NoError(t, e.Emit(ctx, changeResult("2:aaaaaaaaaaaaaaaa", "3:bbbbbbbbbbbbbbbb", "10")))
	require.ErrorIs(t, e.Emit(ctx, changeResult("2:aaaaaaaaaaaaaaaa", "3:bbbbbbbbbbbbbbbb", "12")), ErrNoChange)
}

// failOnce makes the first verb call on Results fail with err; later calls
// reach the fake's object tracker as usual.
func failOnce(c *dynamicfake.FakeDynamicClient, verb string, err error) {
	failed := false
	c.PrependReactor(verb, "results", func(k8stesting.Action) (bool, runtime.Object, error) {
		if failed {
			return false, nil, nil
		}
		failed = true
		return true, nil, err
	})
}

// TestClient_RetriesUpdateConflict: another write between the Get and the
// Update (the operator updating the Result's status) makes the Update fail
// with a conflict. Emit reads the Result again and writes the change.
func TestClient_RetriesUpdateConflict(t *testing.T) {
	c := newFakeClient(t, mkResult("ev-oomkilled-p1-abcdef12", "k8sgpt-system", "default/p1-old"))
	failOnce(c, "update", apierrors.NewConflict(corev1alpha1.GroupVersionResource.GroupResource(), "ev-oomkilled-p1-abcdef12", errors.New("object was modified")))
	e := New(c, SameEvent, quietLog())

	require.NoError(t, e.Emit(context.Background(), mkResult("ev-oomkilled-p1-abcdef12", "k8sgpt-system", "default/p1-new")))

	got := getResult(t, c, "k8sgpt-system", "ev-oomkilled-p1-abcdef12")
	require.Equal(t, "default/p1-new", got.Spec.Name)
	require.Equal(t, "1", got.Annotations[annoCount])
}

// TestClient_RetriesCreateAlreadyExists: the Result was created after the
// Get found none. Emit reads it again and updates it.
func TestClient_RetriesCreateAlreadyExists(t *testing.T) {
	c := newFakeClient(t, mkResult("ev-oomkilled-p1-abcdef12", "k8sgpt-system", "default/p1-old"))
	failOnce(c, "get", apierrors.NewNotFound(corev1alpha1.GroupVersionResource.GroupResource(), "ev-oomkilled-p1-abcdef12"))
	e := New(c, SameEvent, quietLog())

	require.NoError(t, e.Emit(context.Background(), mkResult("ev-oomkilled-p1-abcdef12", "k8sgpt-system", "default/p1-new")))

	got := getResult(t, c, "k8sgpt-system", "ev-oomkilled-p1-abcdef12")
	require.Equal(t, "default/p1-new", got.Spec.Name)
}

func TestDryRun_EmitLogsAndPrints(t *testing.T) {
	var buf bytes.Buffer
	d := NewDryRun(slog.New(slog.NewTextHandler(&buf, nil)))
	require.NoError(t, d.Emit(context.Background(), mkResult("ev-x-abc12345", "k8sgpt-system", "default/p")))
	require.Contains(t, buf.String(), "dry-run emit")
}
