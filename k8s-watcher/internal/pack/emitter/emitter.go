// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package emitter creates and updates K8sGPT Result CRDs for both pipelines,
// events-watcher (EV) and configmap-watcher (CM). The emitter does not
// delete Results: events and ConfigMap changes don't "resolve", so a Result
// persists as evidence until an operator cleans it up.
//
// Client writes through client-go's dynamic client; DryRun logs what it
// would write instead (used for local development and smoke tests without
// a cluster). BoundedEmitter puts a queue, a worker pool and a rate limit in
// front of either one.
package emitter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"

	corev1alpha1 "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/k8sgpt/v1alpha1"
)

// ErrNoChange signals that the in-cluster Result already describes what the
// pipeline wanted to emit (see SpecEqual). BoundedEmitter counts it as
// dropped{reason="no_change"}, separately from real apiserver writes:
// informer relists and process restarts deliver objects the watcher has
// already reported, and would otherwise inflate the emit metric and the
// apiserver write load with no semantic change. Same target, same signal,
// same evidence → no new work.
var ErrNoChange = errors.New("emitter: no change in result spec")

// Lifecycle annotation keys. Operators query these via
//
//	kubectl get result -o custom-columns=\
//	  NAME:.metadata.name,FIRST:.metadata.annotations.k8sgpt-detection-pack\.io/first-observed-at,\
//	  LAST:.metadata.annotations.k8sgpt-detection-pack\.io/last-observed-at,\
//	  COUNT:.metadata.annotations.k8sgpt-detection-pack\.io/observation-count
//
// to see repetition history on individual Results. Duration = now - first.
//
// Suppressed (ErrNoChange) emits do NOT bump these — they intentionally
// skip the apiserver write entirely. The aggregate suppression rate per
// signal_type is visible via k8sgpt_pack_emitter_suppressed_total, so the
// Result-level counters track only spec-changing observations.
const (
	annoFirstObserved = "k8sgpt-detection-pack.io/first-observed-at"
	annoLastObserved  = "k8sgpt-detection-pack.io/last-observed-at"
	annoCount         = "k8sgpt-detection-pack.io/observation-count"
)

// setInitialLifecycle stamps the annotations on a Result about to be Created.
func setInitialLifecycle(r *corev1alpha1.Result, now time.Time) {
	if r.Annotations == nil {
		r.Annotations = map[string]string{}
	}
	ts := now.UTC().Format(time.RFC3339)
	r.Annotations[annoFirstObserved] = ts
	r.Annotations[annoLastObserved] = ts
	r.Annotations[annoCount] = "1"
}

// bumpLifecycle updates last-observed + count on an existing Result about
// to be Updated. Preserves first-observed (and backfills it if absent, as
// on a Result some other writer created without these annotations).
func bumpLifecycle(r *corev1alpha1.Result, now time.Time) {
	if r.Annotations == nil {
		r.Annotations = map[string]string{}
	}
	ts := now.UTC().Format(time.RFC3339)
	r.Annotations[annoLastObserved] = ts
	if _, ok := r.Annotations[annoFirstObserved]; !ok {
		r.Annotations[annoFirstObserved] = ts
	}
	if cs, ok := r.Annotations[annoCount]; ok {
		if c, err := strconv.Atoi(cs); err == nil {
			r.Annotations[annoCount] = strconv.Itoa(c + 1)
			return
		}
	}
	r.Annotations[annoCount] = "1"
}

// Emitter is the contract the pipelines depend on.
type Emitter interface {
	Emit(ctx context.Context, result *corev1alpha1.Result) error
}

// SpecEqual reports whether existing, the spec of the Result already in the
// cluster, describes the same observation as incoming. Client.Emit returns
// ErrNoChange instead of writing when it does. Each pipeline passes its own:
// SameEvent for EV, SameChange for CM.
type SpecEqual func(existing, incoming corev1alpha1.ResultSpec) bool

// SameEvent compares Kind, Name, ParentObject, Backend and the Error slice,
// and skips Details. The EV mapper puts the Event series' lastSeen and count
// in Details, and both change every time the series repeats without
// anything new to report. Error[].Text carries the operator-facing
// description.
func SameEvent(a, b corev1alpha1.ResultSpec) bool {
	if a.Kind != b.Kind || a.Name != b.Name ||
		a.ParentObject != b.ParentObject || a.Backend != b.Backend {
		return false
	}
	return reflect.DeepEqual(a.Error, b.Error)
}

// SameChange is SameEvent plus Details without its volatileDetails fields.
//
// CM must compare Details: the Error text only counts keys ("ConfigMap
// default/foo modified: 0 added, 0 removed, 1 changed"), so a second edit of
// the same key has the same text, and only the diff in Details tells it
// apart. Without it that edit was dropped and the Result kept describing the
// first one.
func SameChange(a, b corev1alpha1.ResultSpec) bool {
	return SameEvent(a, b) && stableDetails(a.Details) == stableDetails(b.Details)
}

// volatileDetails are the Details fields the CM mapper sets from the
// delivery rather than the change: they differ every time, even when the
// same change is delivered again.
var volatileDetails = []string{"observedAt", "resourceVersion"}

// stableDetails returns Details without its volatileDetails fields, in a
// canonical form (json.Marshal sorts map keys). Details that are not a JSON
// object are returned unchanged and compared as is.
func stableDetails(details string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(details), &m); err != nil {
		return details
	}
	for _, k := range volatileDetails {
		delete(m, k)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return details
	}
	return string(b)
}

// Client is the live emitter; it issues real K8s API operations against the
// Result CRD.
type Client struct {
	results dynamic.NamespaceableResourceInterface
	equal   SpecEqual
	log     *slog.Logger
}

// New returns a Client that writes Results through c and treats a Result
// whose spec is equal (by equal) to the one in the cluster as no change.
func New(c dynamic.Interface, equal SpecEqual, log *slog.Logger) *Client {
	return &Client{results: c.Resource(corev1alpha1.GroupVersionResource), equal: equal, log: log}
}

// Emit creates the Result if it doesn't exist, or updates it if the in-
// cluster state differs. Idempotent. Returns ErrNoChange when the existing
// Result's spec is equal to the incoming one — callers should treat that as
// a suppressed no-op, not a failure.
//
// The Result goes through the typed struct both ways, as it did with the
// operator's typed client: an Update writes back the Result as read, with
// the new spec, labels and lifecycle annotations.
//
// When another write lands between the Get and the Create or Update (the
// operator updating the Result's status, or another emit of the same
// Result), the write fails with a conflict or "already exists". Emit then
// reads the Result again and tries once more, so the observation is not
// lost.
func (e *Client) Emit(ctx context.Context, result *corev1alpha1.Result) error {
	err := e.emit(ctx, result)
	if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
		err = e.emit(ctx, result)
	}
	return err
}

func (e *Client) emit(ctx context.Context, result *corev1alpha1.Result) error {
	now := time.Now()
	results := e.results.Namespace(result.Namespace)
	got, err := results.Get(ctx, result.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		setInitialLifecycle(result, now)
		obj, err := toUnstructured(result)
		if err != nil {
			return fmt.Errorf("create result: %w", err)
		}
		if _, err := results.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create result: %w", err)
		}
		e.log.Info("emitted result (created)", "name", result.Name, "namespace", result.Namespace, "kind", result.Spec.Kind, "spec_name", result.Spec.Name)
		return nil
	case err != nil:
		return fmt.Errorf("get result: %w", err)
	}
	var existing corev1alpha1.Result
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(got.Object, &existing); err != nil {
		return fmt.Errorf("get result: decode: %w", err)
	}
	// Suppression gate: nothing semantic changed → skip the apiserver Update.
	// Lifecycle annotations only track spec-changing observations (the
	// aggregate suppression rate per signal_type is visible via the
	// suppressed_total metric).
	if e.equal(existing.Spec, result.Spec) {
		return ErrNoChange
	}
	existing.Spec = result.Spec
	existing.Labels = result.Labels
	bumpLifecycle(&existing, now)
	obj, err := toUnstructured(&existing)
	if err != nil {
		return fmt.Errorf("update result: %w", err)
	}
	if _, err := results.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update result: %w", err)
	}
	e.log.Info("emitted result (updated)",
		"name", result.Name,
		"namespace", result.Namespace,
		"observation_count", existing.Annotations[annoCount],
	)
	return nil
}

// toUnstructured converts r for the dynamic client and sets its apiVersion
// and kind, which the mappers leave empty.
func toUnstructured(r *corev1alpha1.Result) (*unstructured.Unstructured, error) {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(r)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(corev1alpha1.GroupVersionKind)
	return u, nil
}

// DryRun is an Emitter that prints what it would do without touching the K8s
// API. Used for local development and smoke tests.
type DryRun struct {
	log *slog.Logger
}

// NewDryRun returns a DryRun emitter.
func NewDryRun(log *slog.Logger) *DryRun {
	return &DryRun{log: log}
}

// Emit prints a pretty-printed JSON of the Result that would be created.
func (d *DryRun) Emit(_ context.Context, result *corev1alpha1.Result) error {
	d.log.Info("dry-run emit", "name", result.Name, "namespace", result.Namespace, "kind", result.Spec.Kind, "spec_name", result.Spec.Name)
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	_, _ = fmt.Fprintln(os.Stdout, string(body))
	return nil
}

// Compile-time interface assertions.
var (
	_ Emitter = (*Client)(nil)
	_ Emitter = (*DryRun)(nil)
)
