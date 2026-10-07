package adapt

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/informer"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
)

const critical = "k8sgpt-detection-pack.io/critical"

func configMap(name string, labels map[string]string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app", Labels: labels, UID: types.UID("uid-" + name)},
		Data:       data,
	}
}

// recorder collects what the source hands the pipeline, in order.
type recorder struct {
	mu   sync.Mutex
	seen []*mutation.Mutation
}

func (r *recorder) handle(_ context.Context, m *mutation.Mutation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, m)
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.seen))
	for _, m := range r.seen {
		out = append(out, m.Name+"/"+string(m.Op))
	}
	return out
}

func (r *recorder) last() *mutation.Mutation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[len(r.seen)-1]
}

// harness starts a ConfigMapSource over a fake clientset seeded with the
// given objects and waits until the initial list has been delivered.
type harness struct {
	t      *testing.T
	client *fake.Clientset
	rec    *recorder
	ctx    context.Context

	sentinels int
}

func startSource(t *testing.T, seeded ...*corev1.ConfigMap) *harness {
	t.Helper()
	client := fake.NewClientset()
	for _, cm := range seeded {
		_, err := client.CoreV1().ConfigMaps(cm.Namespace).Create(context.Background(), cm, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	rec := &recorder{}
	src := &ConfigMapSource{Client: client}
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, rec.handle) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	h := &harness{t: t, client: client, rec: rec, ctx: ctx}
	// The informer is past its initial list once a create made now is
	// delivered as an add. A sentinel created too early lands in the initial
	// list (or, with the fake clientset, between its list and its watch) and
	// is never delivered, so keep creating fresh ones until one arrives.
	require.Eventually(t, func() bool {
		h.sentinels++
		h.create(configMap(fmt.Sprintf("sync-sentinel-%d", h.sentinels), map[string]string{critical: "true"}, nil))
		time.Sleep(50 * time.Millisecond)
		for _, n := range rec.names() {
			if strings.HasPrefix(n, "sync-sentinel-") {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "informer never delivered a post-sync add")
	return h
}

func (h *harness) create(cm *corev1.ConfigMap) {
	h.t.Helper()
	_, err := h.client.CoreV1().ConfigMaps(cm.Namespace).Create(h.ctx, cm, metav1.CreateOptions{})
	require.NoError(h.t, err)
}

func (h *harness) update(cm *corev1.ConfigMap) {
	h.t.Helper()
	_, err := h.client.CoreV1().ConfigMaps(cm.Namespace).Update(h.ctx, cm, metav1.UpdateOptions{})
	require.NoError(h.t, err)
}

func (h *harness) delete(name string) {
	h.t.Helper()
	require.NoError(h.t, h.client.CoreV1().ConfigMaps("app").Delete(h.ctx, name, metav1.DeleteOptions{}))
}

func (h *harness) waitFor(entry string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		for _, n := range h.rec.names() {
			if n == entry {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "never saw %s; saw %v", entry, h.rec.names())
}

// barrier makes a reportable change on a fresh object and waits for it. The
// informer delivers in watch order, so anything done before the barrier
// that was going to reach the handler has done so by the time it returns.
func (h *harness) barrier(name string) {
	h.t.Helper()
	h.create(configMap(name, map[string]string{critical: "true"}, nil))
	h.waitFor(name + "/Add")
}

func (h *harness) assertNotSeen(name string) {
	h.t.Helper()
	for _, n := range h.rec.names() {
		assert.False(h.t, strings.HasPrefix(n, name+"/"), "%s reached the pipeline: %v", name, h.rec.names())
	}
}

func TestConfigMapSource_InitialListDoesNotEmit(t *testing.T) {
	h := startSource(t, configMap("pre-existing", map[string]string{critical: "true"}, map[string]string{"k": "v"}))
	h.assertNotSeen("pre-existing")
}

func TestConfigMapSource_AddAfterInitialListEmits(t *testing.T) {
	h := startSource(t)
	h.create(configMap("fresh", map[string]string{critical: "true"}, map[string]string{"example-key": "example-value"}))
	h.waitFor("fresh/Add")

	m := h.rec.last()
	assert.Equal(t, mutation.KindConfigMap, m.Kind)
	assert.Equal(t, "app", m.Namespace)
	assert.Equal(t, "13:", m.Data["example-key"][:3], "a digest of a 13-byte value")
	assert.NotContains(t, m.Data["example-key"], "example-value")
	assert.Nil(t, m.PrevData)
	assert.NotContains(t, m.Annotations, informer.DataDigestAnnotation, "cache-only annotation must not reach the pipeline")
}

func TestConfigMapSource_DigestChangedUpdateEmits(t *testing.T) {
	h := startSource(t, configMap("cfg", map[string]string{critical: "true"}, map[string]string{"timeout": "5s", "gone": "x"}))
	h.update(configMap("cfg", map[string]string{critical: "true"}, map[string]string{"timeout": "30s", "new": "y"}))
	h.waitFor("cfg/Update")

	m := h.rec.last()
	require.NotNil(t, m.PrevData)
	assert.NotEqual(t, m.PrevData["timeout"], m.Data["timeout"])
	assert.Equal(t, "2:", m.PrevData["timeout"][:2])
	assert.Equal(t, "3:", m.Data["timeout"][:2])
	assert.Contains(t, m.PrevData, "gone")
	assert.Contains(t, m.Data, "new")
}

func TestConfigMapSource_MetadataOnlyUpdateDoesNotEmit(t *testing.T) {
	h := startSource(t, configMap("cfg", map[string]string{critical: "true"}, map[string]string{"timeout": "5s"}))
	touched := configMap("cfg", map[string]string{critical: "true", "helm.sh/chart": "app-1.2.3"}, map[string]string{"timeout": "5s"})
	touched.Annotations = map[string]string{"meta.helm.sh/release-name": "app"}
	h.update(touched)
	h.barrier("after-metadata-update")
	h.assertNotSeen("cfg")
}

func TestConfigMapSource_DeleteEmits(t *testing.T) {
	h := startSource(t, configMap("cfg", map[string]string{critical: "true"}, map[string]string{"k": "v"}))
	h.delete("cfg")
	h.waitFor("cfg/Delete")
	m := h.rec.last()
	assert.True(t, strings.HasPrefix(m.PrevData["k"], "1:"), "a delete describes the last cached state as the previous one, as digests; got %v", m.PrevData)
	assert.Empty(t, m.Data, "nothing is left after a delete")
}

func TestConfigMapSource_UnlabelledDoesNotEmit(t *testing.T) {
	h := startSource(t)
	h.create(configMap("plain", nil, map[string]string{"k": "v"}))
	h.update(configMap("plain", nil, map[string]string{"k": "changed"}))
	h.delete("plain")
	h.barrier("after-unlabelled")
	h.assertNotSeen("plain")
}

func TestConfigMapSource_WrongLabelValueDoesNotEmit(t *testing.T) {
	for _, value := range []string{"false", "yes", "True", ""} {
		t.Run("value="+value, func(t *testing.T) {
			h := startSource(t)
			h.create(configMap("wrong", map[string]string{critical: value}, map[string]string{"k": "v"}))
			h.update(configMap("wrong", map[string]string{critical: value}, map[string]string{"k": "changed"}))
			h.delete("wrong")
			h.barrier("after-wrong-value")
			h.assertNotSeen("wrong")
		})
	}
}

// TestMutationFromConfigMapEvent_Delete covers the delete mapping for both
// a plain delete and one recovered from a tombstone; the informer package
// tests that a tombstone reaches the handler as a critical delete.
//
// The last cached state is the previous content and nothing is current, so
// the Pack's diff reports every key as removed. Mapping it as current
// content made a delete read "2 added, 0 removed".
func TestMutationFromConfigMapEvent_Delete(t *testing.T) {
	cm := configMap("cfg", map[string]string{critical: "true"}, map[string]string{"k": "1:6b86b273ff34fce1", "j": "1:d4735e3a265e16ee"})
	cm.BinaryData = map[string][]byte{"b": []byte("1:4e07408562bedb8b")}
	m := MutationFromConfigMapEvent(informer.ConfigMapEvent{CM: cm, ChangeType: informer.ChangeTypeDelete, Critical: true}, time.Now())
	require.NotNil(t, m)
	assert.Equal(t, mutation.OpDelete, m.Op)
	assert.Equal(t, cm.Data, m.PrevData)
	assert.Equal(t, cm.BinaryData, m.PrevBinaryData)
	assert.Nil(t, m.Data)
	assert.Nil(t, m.BinaryData)
	assert.Equal(t, cm.Labels, m.Labels, "the filter still sees the opt-in label")

	assert.Nil(t, MutationFromConfigMapEvent(informer.ConfigMapEvent{CM: cm, ChangeType: informer.ChangeTypeDelete}, time.Now()),
		"a non-critical event maps to nothing")
}
