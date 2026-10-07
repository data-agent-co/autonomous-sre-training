package informer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func TestResolveConfigMapFromDeleteObj_DirectConfigMap(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"}}
	got := resolveConfigMapFromDeleteObj(cm, testLogger(t))
	assert.NotNil(t, got)
	assert.Equal(t, "test", got.Name)
}

func TestResolveConfigMapFromDeleteObj_Tombstone(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"}}
	tombstone := cache.DeletedFinalStateUnknown{Key: "default/test", Obj: cm}
	got := resolveConfigMapFromDeleteObj(tombstone, testLogger(t))
	assert.NotNil(t, got)
	assert.Equal(t, "test", got.Name)
}

func TestResolveConfigMapFromDeleteObj_TombstoneUnexpectedType(t *testing.T) {
	tombstone := cache.DeletedFinalStateUnknown{Key: "default/test", Obj: "not-a-configmap"}
	got := resolveConfigMapFromDeleteObj(tombstone, testLogger(t))
	assert.Nil(t, got, "tombstone holding non-ConfigMap must return nil")
}

func TestResolveConfigMapFromDeleteObj_UnexpectedType(t *testing.T) {
	got := resolveConfigMapFromDeleteObj("unexpected-string", testLogger(t))
	assert.Nil(t, got, "non-ConfigMap non-tombstone must return nil")
}
