package adapt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
)

// forbidList makes every list of resource fail the way a missing RBAC rule
// does.
func forbidList(client *fake.Clientset, resource string) {
	client.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("missing RBAC rule"))
	})
}

// runWithin runs run and fails the test if it has not returned within
// limit, so a Run that waits forever fails fast instead of at the test
// binary's timeout.
func runWithin(t *testing.T, limit time.Duration, run func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("Run still waiting for the initial list after %s", limit)
		return nil
	}
}

func TestEventSource_HasSyncedAfterInitialList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	src := &EventSource{Client: fake.NewClientset(), WatchPods: true}
	assert.False(t, src.HasSynced(), "not synced before Run")

	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, func(context.Context, *event.Event) {}) }()
	require.Eventually(t, src.HasSynced, 5*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)
}

// TestEventSource_ForbiddenListEndsRun covers each informer the source
// starts: a list that never succeeds ends Run with the cause instead of
// leaving it waiting, and the source never reports synced.
func TestEventSource_ForbiddenListEndsRun(t *testing.T) {
	for _, resource := range []string{"pods", "events"} {
		t.Run(resource, func(t *testing.T) {
			client := fake.NewClientset()
			forbidList(client, resource)
			src := &EventSource{Client: client, WatchPods: true, SyncTimeout: 300 * time.Millisecond}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel() // stops the informers Run started

			err := runWithin(t, 5*time.Second, func() error {
				return src.Run(ctx, func(context.Context, *event.Event) {})
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), resource+" is forbidden")
			assert.False(t, src.HasSynced())
		})
	}
}

func TestConfigMapSource_ForbiddenListEndsRun(t *testing.T) {
	client := fake.NewClientset()
	forbidList(client, "configmaps")
	src := &ConfigMapSource{Client: client, SyncTimeout: 300 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the informer Run started

	err := runWithin(t, 5*time.Second, func() error {
		return src.Run(ctx, func(context.Context, *mutation.Mutation) {})
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configmaps is forbidden")
	assert.False(t, src.HasSynced())
}

func TestConfigMapSource_HasSyncedAfterInitialList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	src := &ConfigMapSource{Client: fake.NewClientset()}

	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, func(context.Context, *mutation.Mutation) {}) }()
	require.Eventually(t, src.HasSynced, 5*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)
}
