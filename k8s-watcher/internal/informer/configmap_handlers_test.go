package informer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func labelled(name, value string, data map[string]string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app", Labels: map[string]string{CriticalLabel: value}},
		Data:       data,
	}
	out, _ := stripConfigMapData(cm)
	return out.(*corev1.ConfigMap)
}

func collect(t *testing.T) (cache.ResourceEventHandlerDetailedFuncs, *[]ConfigMapEvent) {
	var got []ConfigMapEvent
	return configMapHandlers(func(ev ConfigMapEvent) { got = append(got, ev) }, testLogger(t)), &got
}

func TestConfigMapHandlers_AddCriticalOnlyAfterInitialList(t *testing.T) {
	h, got := collect(t)
	h.AddFunc(labelled("cfg", "true", nil), true)
	h.AddFunc(labelled("cfg", "true", nil), false)
	require.Len(t, *got, 2)
	assert.False(t, (*got)[0].Critical, "initial-list add")
	assert.True(t, (*got)[1].Critical, "add after the initial list")
}

func TestConfigMapHandlers_UpdateCarriesOldRevision(t *testing.T) {
	h, got := collect(t)
	old := labelled("cfg", "true", map[string]string{"k": "a"})
	h.UpdateFunc(old, labelled("cfg", "true", map[string]string{"k": "b"}))
	require.Len(t, *got, 1)
	assert.True(t, (*got)[0].Critical)
	assert.Same(t, old, (*got)[0].Old)
}

// initialListCopy returns cm as the cache holds it after a watch-list initial
// sync: the reflector's temporary store transforms it, then RealFIFO.Replace
// transforms the listed copy again.
func initialListCopy(t *testing.T, cm *corev1.ConfigMap) *corev1.ConfigMap {
	t.Helper()
	tmp := cache.NewStore(cache.DeletionHandlingMetaNamespaceKeyFunc, cache.WithTransformer(stripConfigMapData))
	require.NoError(t, tmp.Add(cm))
	listed := tmp.List()
	require.Len(t, listed, 1)
	out, err := stripConfigMapData(listed[0])
	require.NoError(t, err)
	return out.(*corev1.ConfigMap)
}

func TestConfigMapHandlers_UpdateAfterInitialListChangesOnlyEditedKey(t *testing.T) {
	raw := func(timeout string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "payments-config", Namespace: "demo-apps", Labels: map[string]string{CriticalLabel: "true"}},
			Data:       map[string]string{"retries": "3", "timeout": timeout},
		}
	}
	h, got := collect(t)
	newObj, err := stripConfigMapData(raw("60s"))
	require.NoError(t, err)
	h.UpdateFunc(initialListCopy(t, raw("45s")), newObj)

	require.Len(t, *got, 1)
	ev := (*got)[0]
	assert.True(t, ev.Critical)
	assert.Equal(t, ev.Old.Data["retries"], ev.CM.Data["retries"], "untouched key must compare equal")
	assert.NotEqual(t, ev.Old.Data["timeout"], ev.CM.Data["timeout"])
	assert.Equal(t, "3:"+sha16("45s"), ev.Old.Data["timeout"])
}

func TestConfigMapHandlers_MetadataOnlyUpdateAfterInitialListIsNotCritical(t *testing.T) {
	cm := labelled("cfg", "true", nil)
	cm.Data = map[string]string{"k": "a"}
	delete(cm.Annotations, DataDigestAnnotation)
	edited := cm.DeepCopy()
	edited.Annotations = map[string]string{"owner": "payments"}

	h, got := collect(t)
	newObj, err := stripConfigMapData(edited)
	require.NoError(t, err)
	h.UpdateFunc(initialListCopy(t, cm), newObj)
	require.Len(t, *got, 1)
	assert.False(t, (*got)[0].Critical, "same content must give the same digest")
}

func TestConfigMapHandlers_LabelAddedWithContentChangeIsNotCritical(t *testing.T) {
	h, got := collect(t)
	unlabelled := labelled("cfg", "", map[string]string{"k": "a"})
	delete(unlabelled.Labels, CriticalLabel)
	h.UpdateFunc(unlabelled, labelled("cfg", "true", map[string]string{"k": "b"}))
	require.Len(t, *got, 1)
	assert.False(t, (*got)[0].Critical, "opting in is not itself drift, even with a content change in the same write")
}

func TestConfigMapHandlers_TombstoneDeleteIsCritical(t *testing.T) {
	h, got := collect(t)
	h.DeleteFunc(cache.DeletedFinalStateUnknown{Key: "app/cfg", Obj: labelled("cfg", "true", nil)})
	require.Len(t, *got, 1)
	assert.Equal(t, ChangeTypeDelete, (*got)[0].ChangeType)
	assert.True(t, (*got)[0].Critical)
}
