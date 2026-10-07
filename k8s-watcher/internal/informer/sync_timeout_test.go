package informer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/informer"
)

// forbidList makes every list of resource fail the way a missing RBAC rule
// does.
func forbidList(client *fake.Clientset, resource string) {
	client.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "",
			errors.New(`User "system:serviceaccount:k8sgpt-system:k8s-watcher" cannot list resource "`+resource+`" at the cluster scope`))
	})
}

// returnsWithin runs start and fails the test if it has not returned within
// limit. Before the sync wait was bounded, a forbidden list kept these calls
// waiting until ctx ended.
func returnsWithin(t *testing.T, limit time.Duration, start func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- start() }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("still waiting for the initial list after %s", limit)
		return nil
	}
}

func TestStartEventInformer_ForbiddenListTimesOut(t *testing.T) {
	client := fake.NewClientset()
	forbidList(client, "events")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := returnsWithin(t, 5*time.Second, func() error {
		return informer.StartEventInformer(ctx, client, func(*corev1.Event) {}, 300*time.Millisecond, testLogger(t))
	})
	require.Error(t, err)
	t.Log(err)
	assert.Contains(t, err.Error(), "did not complete within 300ms")
	assert.Contains(t, err.Error(), `cannot list resource "events"`, "the error names the cause")
}

func TestStartConfigMapInformer_ForbiddenListTimesOut(t *testing.T) {
	client := fake.NewClientset()
	forbidList(client, "configmaps")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := returnsWithin(t, 5*time.Second, func() error {
		return informer.StartConfigMapInformer(ctx, client, func(informer.ConfigMapEvent) {}, 300*time.Millisecond, testLogger(t))
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not complete within 300ms")
	assert.Contains(t, err.Error(), `cannot list resource "configmaps"`)
}

func TestStartPodInformer_ForbiddenListTimesOut(t *testing.T) {
	client := fake.NewClientset()
	forbidList(client, "pods")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := returnsWithin(t, 5*time.Second, func() error {
		_, err := informer.StartPodInformer(ctx, client, 300*time.Millisecond, testLogger(t))
		return err
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot list resource "pods"`)
}

func TestStartPodInformer_ListerServesPods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "cart-1"}})

	lister, err := informer.StartPodInformer(ctx, client, 0, testLogger(t))
	require.NoError(t, err)
	pod, err := lister.Pods("shop").Get("cart-1")
	require.NoError(t, err)
	assert.Equal(t, "cart-1", pod.Name)
}

// TestStartEventInformer_CancelWhileWaitingReturnsCtxErr keeps shutdown
// quiet: a cancel during the initial list is not reported as a timeout.
func TestStartEventInformer_CancelWhileWaitingReturnsCtxErr(t *testing.T) {
	client := fake.NewClientset()
	forbidList(client, "events")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	err := returnsWithin(t, 5*time.Second, func() error {
		return informer.StartEventInformer(ctx, client, func(*corev1.Event) {}, time.Minute, testLogger(t))
	})
	require.ErrorIs(t, err, context.Canceled)
}
