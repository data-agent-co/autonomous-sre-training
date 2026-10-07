package probectx

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// podLister returns a lister serving the given pods, as a synced informer
// cache would.
func podLister(t *testing.T, pods ...*corev1.Pod) listerscorev1.PodLister {
	t.Helper()
	idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, p := range pods {
		require.NoError(t, idx.Add(p))
	}
	return listerscorev1.NewPodLister(idx)
}

func container(cpuLimit string) corev1.Container {
	c := corev1.Container{Name: "c"}
	if cpuLimit != "" {
		c.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpuLimit)}
	}
	return c
}

// terminated is a container status whose last termination finished at.
func terminated(restarts int32, at time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		RestartCount: restarts,
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(at)},
		},
	}
}

func pod(name string, containers []corev1.Container, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name},
		Spec:       corev1.PodSpec{Containers: containers},
		Status:     corev1.PodStatus{ContainerStatuses: statuses},
	}
}

func TestKubeLookup_SumsCPULimits(t *testing.T) {
	lookup := NewKubeLookup(podLister(t,
		pod("api", []corev1.Container{container("100m"), container("1"), container("")}),
	))
	got := lookup("shop", "api")
	assert.True(t, got.Available)
	assert.Equal(t, 1100, got.CPULimitMillicores, "a container without a limit adds nothing")
	assert.Zero(t, got.CPUThrottleRate, "the kube lookup never sets the throttle rate")
}

func TestKubeLookup_CountsRestartsOnlyWithRecentTermination(t *testing.T) {
	now := time.Now()
	lookup := NewKubeLookup(podLister(t,
		pod("crashing", nil, terminated(3, now.Add(-2*time.Minute)), corev1.ContainerStatus{RestartCount: 1}),
		pod("settled", nil, terminated(9, now.Add(-2*time.Hour))),
	))

	crashing := lookup("shop", "crashing")
	assert.True(t, crashing.Available)
	assert.Equal(t, 4, crashing.RestartsLast15m, "all restarts count once one container terminated recently")

	settled := lookup("shop", "settled")
	assert.True(t, settled.Available)
	assert.Zero(t, settled.RestartsLast15m, "old restart history does not count")
}

func TestKubeLookup_UnavailableWithoutPod(t *testing.T) {
	lister := podLister(t, pod("api", nil))
	assert.Equal(t, ProbeContext{}, NewKubeLookup(lister)("shop", "missing"), "pod not in cache")
	assert.Equal(t, ProbeContext{}, NewKubeLookup(lister)("", "api"), "no namespace")
	assert.Equal(t, ProbeContext{}, NewKubeLookup(nil)("shop", "api"), "no lister yet")
}

// fakePrometheus serves body for /api/v1/query and records the last query.
type fakePrometheus struct {
	*httptest.Server
	mu    sync.Mutex
	query string
}

func prometheus(t *testing.T, status int, body string) *fakePrometheus {
	t.Helper()
	p := &fakePrometheus{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			http.NotFound(w, r)
			return
		}
		p.mu.Lock()
		p.query = r.URL.Query().Get("query")
		p.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *fakePrometheus) lastQuery() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.query
}

// kubeOnly is a kube lookup that always finds the pod with a 100m limit.
func kubeOnly(_, _ string) ProbeContext {
	return ProbeContext{Available: true, CPULimitMillicores: 100}
}

func TestPrometheusLookup_AddsThrottleRate(t *testing.T) {
	srv := prometheus(t, http.StatusOK,
		`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1759560000.123,"0.42"]}]}}`)

	got := NewPrometheusLookup(srv.URL, kubeOnly, time.Second)("shop", "api")
	assert.Equal(t, ProbeContext{Available: true, CPULimitMillicores: 100, CPUThrottleRate: 0.42}, got)
	assert.Equal(t, `sum(rate(container_cpu_cfs_throttled_seconds_total{namespace="shop",pod="api"}[1m]))`, srv.lastQuery())
}

func TestPrometheusLookup_AvailableFromPrometheusAlone(t *testing.T) {
	srv := prometheus(t, http.StatusOK,
		`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1759560000,"0.5"]}]}}`)

	got := NewPrometheusLookup(srv.URL, NewKubeLookup(nil), time.Second)("shop", "api")
	assert.True(t, got.Available, "a throttle rate alone is enrichment")
	assert.InDelta(t, 0.5, got.CPUThrottleRate, 1e-9)
}

func TestPrometheusLookup_EmptyResultMeansNoThrottling(t *testing.T) {
	srv := prometheus(t, http.StatusOK, `{"status":"success","data":{"resultType":"vector","result":[]}}`)

	got := NewPrometheusLookup(srv.URL, kubeOnly, time.Second)("shop", "api")
	assert.True(t, got.Available)
	assert.Zero(t, got.CPUThrottleRate)
}

// TestPrometheusLookup_FailuresKeepKubeContext covers every failure the
// lookup swallows: the kube side's answer comes back unchanged.
func TestPrometheusLookup_FailuresKeepKubeContext(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"server error":    {http.StatusInternalServerError, `{"status":"error"}`},
		"malformed json":  {http.StatusOK, `{"status":`},
		"value not float": {http.StatusOK, `{"status":"success","data":{"result":[{"value":[1,"NaN-ish"]}]}}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := prometheus(t, tc.status, tc.body)
			got := NewPrometheusLookup(srv.URL, kubeOnly, time.Second)("shop", "api")
			assert.Equal(t, kubeOnly("shop", "api"), got)
		})
	}
}

func TestPrometheusLookup_TimeoutKeepsKubeContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	got := NewPrometheusLookup(srv.URL, kubeOnly, 200*time.Millisecond)("shop", "api")
	assert.Less(t, time.Since(start), 2*time.Second, "the lookup is bounded by its timeout")
	assert.Equal(t, kubeOnly("shop", "api"), got)
}

func TestPrometheusLookup_NoURLSkipsPrometheus(t *testing.T) {
	t.Setenv("PROMETHEUS_URL", "")
	got := NewPrometheusLookup("", kubeOnly, time.Second)("shop", "api")
	assert.Equal(t, kubeOnly("shop", "api"), got)
}

func TestPrometheusLookup_FallsBackToEnvURL(t *testing.T) {
	srv := prometheus(t, http.StatusOK,
		`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1759560000,"0.25"]}]}}`)
	t.Setenv("PROMETHEUS_URL", srv.URL)

	got := NewPrometheusLookup("", kubeOnly, time.Second)("shop", "api")
	assert.InDelta(t, 0.25, got.CPUThrottleRate, 1e-9)
}
