// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package probectx supplies the pod-resource context the mapper needs to
// classify probe failures correctly.
//
// Without context, the mapper can only read the kubelet's probe note and
// fall back to a generic differential (e.g. "timeout → network_partition
// leads"). That misclassifies CPU-throttle-induced probe timeouts as
// network problems. The Lookup returned here lets the mapper consult the
// pod's CPU limit, observed CPU CFS throttle rate, and recent restart
// count so the differential can lead with the right cause.
//
// Two implementations:
//
//	NewKubeLookup        — reads the Pod via a SharedInformer lister.
//	                       Cheap (cache hit, no apiserver call). Available
//	                       for everything in-cluster.
//
//	NewPrometheusLookup  — wraps a kubeLookup and additionally queries
//	                       Prometheus for the CPU CFS throttle rate. The
//	                       kubelet does not surface this on the Pod
//	                       object, so a metrics backend is required for
//	                       the full enrichment. Graceful: if Prometheus
//	                       is unreachable or slow, throttle stays 0 and
//	                       the rest of the ProbeContext is still returned.
package probectx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	listerscorev1 "k8s.io/client-go/listers/core/v1"
)

// ProbeContext is the pod-resource snapshot the mapper consumes.
type ProbeContext struct {
	// Available is true when the lookup populated at least one of the
	// fields below. False = couldn't find the pod (lister cold) or there
	// was a non-fatal error. The mapper treats Available=false the same
	// as "no enrichment" and falls back to the kubelet-note-only
	// classification.
	Available bool

	// CPULimitMillicores is the sum of container CPU limits on the pod,
	// in millicores. Zero = no CPU limit set (or unable to read). Used
	// alongside CPUThrottleRate to detect throttle-tight workloads.
	CPULimitMillicores int

	// CPUThrottleRate is the rate of CFS-throttled seconds per second
	// observed for the pod's containers, summed. >0.3 with a small CPU
	// limit is the classic "CPU starvation looks like a network timeout"
	// signature. Zero when no metrics backend is configured or the
	// query failed.
	CPUThrottleRate float64

	// RestartsLast15m is the number of container restarts observed on
	// the pod in the last 15 minutes (best-effort: counts total restarts
	// if any container's lastState.terminated.finishedAt is within the
	// window). >=2 alongside a connection_refused/reset probe failure
	// suggests crash_loop rather than a network problem.
	RestartsLast15m int
}

// Lookup is the mapper-facing surface. Pass a (namespace, name) and get
// the pod's resource snapshot — or an empty ProbeContext when the lookup
// can't proceed. Implementations MUST NOT block longer than a couple of
// seconds; each lookup holds one of the events-watcher's few probe workers.
type Lookup func(podNamespace, podName string) ProbeContext

// NewKubeLookup reads from the pod lister. The lister must have been
// started by the caller's SharedInformerFactory; until cache.WaitForCacheSync
// completes the lister returns NotFound and this Lookup returns
// Available=false. The returned Lookup is safe to call concurrently.
//
// If lister is nil, every call returns an empty ProbeContext — caller-
// friendly behavior for unit tests and the "Prometheus URL set, no kube
// access" combination.
func NewKubeLookup(lister listerscorev1.PodLister) Lookup {
	return func(ns, name string) ProbeContext {
		if lister == nil || ns == "" || name == "" {
			return ProbeContext{}
		}
		pod, err := lister.Pods(ns).Get(name)
		if err != nil || pod == nil {
			return ProbeContext{}
		}

		ctx := ProbeContext{Available: true}

		// CPU limit — sum across containers. Treat missing limits as
		// "no limit" (== 0) rather than skipping the pod entirely.
		for _, c := range pod.Spec.Containers {
			if c.Resources.Limits == nil {
				continue
			}
			q, ok := c.Resources.Limits["cpu"]
			if !ok {
				continue
			}
			ctx.CPULimitMillicores += int(q.MilliValue())
		}

		// Recent restarts — only sum if at least one container reports a
		// recent termination. This is a best-effort guard against
		// double-counting long-ago restart history on a stable pod.
		now := time.Now()
		cutoff := now.Add(-15 * time.Minute)
		recent := false
		var restartTotal int
		for _, cs := range pod.Status.ContainerStatuses {
			restartTotal += int(cs.RestartCount)
			if cs.LastTerminationState.Terminated != nil &&
				!cs.LastTerminationState.Terminated.FinishedAt.IsZero() &&
				cs.LastTerminationState.Terminated.FinishedAt.After(cutoff) {
				recent = true
			}
		}
		if recent {
			ctx.RestartsLast15m = restartTotal
		}
		return ctx
	}
}

// NewPrometheusLookup wraps a kubeLookup and additionally queries
// Prometheus for the CPU CFS throttle rate. promURL is the base
// Prometheus URL (e.g. "http://prometheus.monitoring:9090"); empty falls
// back to the PROMETHEUS_URL env var.
//
// httpTimeout is the hard per-request deadline — keep it short (2-3s)
// because each lookup holds a probe worker. Any error (timeout,
// connection refused, non-200, malformed JSON) is swallowed: the
// kubeLookup's ProbeContext is returned unchanged so the caller still
// gets CPU limit + restart info.
func NewPrometheusLookup(promURL string, kubeLookup Lookup, httpTimeout time.Duration) Lookup {
	if promURL == "" {
		promURL = os.Getenv("PROMETHEUS_URL")
	}
	if httpTimeout <= 0 {
		httpTimeout = 2 * time.Second
	}
	client := &http.Client{Timeout: httpTimeout}

	return func(ns, name string) ProbeContext {
		ctx := kubeLookup(ns, name)
		if promURL == "" || ns == "" || name == "" {
			return ctx
		}
		rate, err := queryThrottleRate(client, promURL, ns, name, httpTimeout)
		if err != nil {
			return ctx
		}
		ctx.Available = true
		ctx.CPUThrottleRate = rate
		return ctx
	}
}

// queryThrottleRate runs sum(rate(container_cpu_cfs_throttled_seconds_total[1m]))
// against Prometheus for the named pod. Returns the scalar throttle rate
// in seconds-per-second, or an error.
func queryThrottleRate(client *http.Client, baseURL, ns, name string, timeout time.Duration) (float64, error) {
	q := fmt.Sprintf(`sum(rate(container_cpu_cfs_throttled_seconds_total{namespace="%s",pod="%s"}[1m]))`, ns, name)
	u := baseURL + "/api/v1/query?query=" + url.QueryEscape(q)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return 0, fmt.Errorf("prometheus status %d", resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				// Prom vector sample: [<timestamp:number>, "<value:string>"].
				Value [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	if body.Status != "success" || len(body.Data.Result) == 0 {
		return 0, nil
	}

	var raw string
	if err := json.Unmarshal(body.Data.Result[0].Value[1], &raw); err != nil {
		return 0, err
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	return val, nil
}
