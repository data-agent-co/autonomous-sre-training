package app_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	corev1alpha1 "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/k8sgpt/v1alpha1"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/app"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
)

// TestRun_WritesResultsThroughResultClient runs the pipeline without DryRun
// against a fake dynamic client and checks that the event becomes a
// core.k8sgpt.ai/v1alpha1 Result in ResultNamespace.
func TestRun_WritesResultsThroughResultClient(t *testing.T) {
	results := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{corev1alpha1.GroupVersionResource: "ResultList"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Now()
	source := fakeSource{events: []*event.Event{{
		InvolvedNamespace: "shop",
		InvolvedName:      "cart-7d9f",
		InvolvedKind:      "Pod",
		InvolvedUID:       "uid-1",
		Reason:            "OOMKilled",
		Note:              "Memory limit exceeded",
		Type:              "Warning",
		FirstSeen:         now,
		LastSeen:          now,
		Count:             1,
	}}}

	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.Options{
			ResultNamespace: "results",
			Source:          source,
			ResultClient:    results,
			HealthAddr:      "127.0.0.1:0",
			Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	var items []unstructured.Unstructured
	deadline := time.Now().Add(5 * time.Second)
	for len(items) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no Result written within 5s")
		}
		time.Sleep(20 * time.Millisecond)
		list, err := results.Resource(corev1alpha1.GroupVersionResource).Namespace("results").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list results: %v", err)
		}
		items = list.Items
	}
	r := items[0]
	if r.GetAPIVersion() != "core.k8sgpt.ai/v1alpha1" || r.GetKind() != "Result" {
		t.Errorf("apiVersion, kind = %q, %q; want core.k8sgpt.ai/v1alpha1, Result", r.GetAPIVersion(), r.GetKind())
	}
	if name, _, _ := unstructured.NestedString(r.Object, "spec", "name"); name != "shop/cart-7d9f" {
		t.Errorf("spec.name = %q, want shop/cart-7d9f", name)
	}
	if backend, _, _ := unstructured.NestedString(r.Object, "spec", "backend"); backend != "k8sgpt-detection-pack:ev" {
		t.Errorf("spec.backend = %q, want k8sgpt-detection-pack:ev", backend)
	}
	if got := r.GetLabels()["k8sgpt-detection-pack.io/signal_type"]; got != "oom_kill" {
		t.Errorf("signal_type label = %q, want oom_kill", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestRun_RequiresResultClient(t *testing.T) {
	err := app.Run(context.Background(), app.Options{Source: fakeSource{}})
	if err == nil || !strings.Contains(err.Error(), "ResultClient is required") {
		t.Fatalf("want a missing-ResultClient error, got %v", err)
	}
}
