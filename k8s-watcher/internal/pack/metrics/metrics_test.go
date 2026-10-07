// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func scrape(t *testing.T, handler http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

func TestNew_ServesInfoGauge(t *testing.T) {
	for _, component := range []string{"events-watcher", "configmap-watcher"} {
		t.Run(component, func(t *testing.T) {
			_, handler := New(component, "v1.2.3")
			out := scrape(t, handler)

			require.Contains(t, out, "k8sgpt_pack_component_info")
			require.Contains(t, out, `component="`+component+`"`)
			require.Contains(t, out, `version="v1.2.3"`)
			// Info-style gauge: value is always 1, presence of the series is what matters.
			require.Contains(t, out, "} 1\n")
		})
	}
}

func TestNew_DoesNotLeakRuntimeMetrics(t *testing.T) {
	// promhttp.Handler() on the default registry exposes Go runtime + process
	// metrics by default. Our scoped registry must not: it carries only the
	// component's own series.
	_, handler := New("events-watcher", "v1.2.3")
	out := scrape(t, handler)

	for _, leaked := range []string{
		"go_goroutines",
		"process_cpu_seconds_total",
		"promhttp_metric_handler_requests_total",
	} {
		require.False(t, strings.Contains(out, leaked),
			"%s should not appear on the scoped registry", leaked)
	}
}
