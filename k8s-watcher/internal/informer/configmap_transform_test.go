package informer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// TestStripConfigMapData_NonConfigMap verifies that stripConfigMapData passes
// non-ConfigMap objects through unchanged (TransformFunc contract: return as-is
// for types the transformer doesn't own).
func TestStripConfigMapData_NonConfigMap(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "default"},
	}
	out, err := stripConfigMapData(pod)
	require.NoError(t, err)
	assert.Same(t, pod, out.(*corev1.Pod), "non-CM objects must be returned unchanged")
}

// TestStripConfigMapData_ConfigMap verifies that every value is replaced by
// "<len>:<sha16>" while key names are preserved, and that the value itself
// appears nowhere in the cached copy.
func TestStripConfigMapData_ConfigMap(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
		Data:       map[string]string{"DB_PASSWORD": "super-secret", "HOST": "db.internal"},
		BinaryData: map[string][]byte{"cert.pem": []byte("cert-bytes")},
	}
	out, err := stripConfigMapData(cm)
	require.NoError(t, err)

	stripped, ok := out.(*corev1.ConfigMap)
	require.True(t, ok)

	assert.Equal(t, "12:"+sha16("super-secret"), stripped.Data["DB_PASSWORD"])
	assert.Equal(t, "11:"+sha16("db.internal"), stripped.Data["HOST"])
	assert.Equal(t, "10:"+sha16("cert-bytes"), string(stripped.BinaryData["cert.pem"]))
	assert.Len(t, stripped.Data, 2, "key set must be preserved")

	for _, v := range stripped.Data {
		assert.NotContains(t, v, "super-secret")
		assert.NotContains(t, v, "db.internal")
	}
	assert.NotEmpty(t, stripped.Annotations[DataDigestAnnotation], "whole-content digest recorded for drift comparison")

	// Original must not be mutated (DeepCopy).
	assert.Equal(t, "super-secret", cm.Data["DB_PASSWORD"], "original must not be mutated")
	assert.Empty(t, cm.Annotations, "original must not gain the cache-only annotation")
}

// TestStripConfigMapData_DropsAnnotations covers a ConfigMap created with
// kubectl apply, or another tool that stores the whole applied object,
// values included, in an annotation. The cached copy keeps only the digest
// annotation, so no value reaches the cache in any field.
func TestStripConfigMapData_DropsAnnotations(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-config",
			Namespace: "default",
			Annotations: map[string]string{
				corev1.LastAppliedConfigAnnotation: `{"apiVersion":"v1","data":{"DB_PASSWORD":"super-secret"},"kind":"ConfigMap","metadata":{"name":"app-config","namespace":"default"}}`,
				"example.com/applied":              `{"data":{"DB_PASSWORD":"super-secret"}}`,
			},
		},
		Data: map[string]string{"DB_PASSWORD": "super-secret"},
	}
	out, err := stripConfigMapData(cm)
	require.NoError(t, err)
	stripped := out.(*corev1.ConfigMap)

	assert.Len(t, stripped.Annotations, 1, "only the digest annotation is kept")
	assert.Contains(t, stripped.Annotations, DataDigestAnnotation)
	cached, err := json.Marshal(stripped)
	require.NoError(t, err)
	assert.NotContains(t, string(cached), "super-secret", "the value appears nowhere in the cached copy")
	assert.Contains(t, cm.Annotations, corev1.LastAppliedConfigAnnotation, "original must not be mutated")
}

// TestStripConfigMapData_Idempotent verifies that a second strip leaves an
// already-stripped copy alone. client-go's watch-list initial sync transforms
// each listed object twice.
func TestStripConfigMapData_Idempotent(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
		Data:       map[string]string{"retries": "3"},
		BinaryData: map[string][]byte{"cert.pem": []byte("cert-bytes")},
	}
	once, err := stripConfigMapData(cm)
	require.NoError(t, err)
	twice, err := stripConfigMapData(once)
	require.NoError(t, err)
	assert.Equal(t, once, twice)
	assert.Equal(t, "1:"+sha16("3"), twice.(*corev1.ConfigMap).Data["retries"])
}

// TestDataDigestAnnotation_NotSettableOnRealObject verifies the marker key is
// one the API server rejects, so a real object can never arrive looking
// already stripped.
func TestDataDigestAnnotation_NotSettableOnRealObject(t *testing.T) {
	assert.NotEmpty(t, validation.IsQualifiedName(DataDigestAnnotation))
}

func TestValueDigest_DistinguishesValues(t *testing.T) {
	assert.Equal(t, valueDigest([]byte("a")), valueDigest([]byte("a")))
	assert.NotEqual(t, valueDigest([]byte("a")), valueDigest([]byte("b")))
	assert.Equal(t, "0:"+sha16(""), valueDigest(nil))
}

// A plain SHA-256 of a short value gives the value away to anyone who can
// read the Result; the keyed digest must not match it.
func TestValueDigest_NotPlainSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("off"))
	assert.NotEqual(t, "3:"+hex.EncodeToString(sum[:])[:16], valueDigest([]byte("off")))
}

func sha16(v string) string {
	mac := hmac.New(sha256.New, digestKey)
	_, _ = mac.Write([]byte(v))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}
