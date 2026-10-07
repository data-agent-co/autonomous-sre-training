// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package mapper

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/filter"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/probectx"
)

func TestParseProbe(t *testing.T) {
	cases := []struct {
		name          string
		note          string
		wantKind      string
		wantMech      string
		wantMode      string
		wantTimeout   string
		wantStatus    string
		wantTargetSub string
	}{
		{
			name:          "exec timeout (the network-partition demo case)",
			note:          `Readiness probe failed: command timed out: "sh -c nslookup kubernetes.default.svc.cluster.local" timed out after 3s`,
			wantKind:      "Readiness",
			wantMech:      "exec",
			wantMode:      "timeout",
			wantTimeout:   "3s",
			wantTargetSub: "nslookup",
		},
		{
			name:          "http connection refused",
			note:          `Liveness probe failed: Get "http://10.0.0.1:8080/healthz": dial tcp 10.0.0.1:8080: connect: connection refused`,
			wantKind:      "Liveness",
			wantMech:      "http",
			wantMode:      "connection_refused",
			wantTargetSub: "10.0.0.1:8080/healthz",
		},
		{
			name:       "http 5xx",
			note:       `Liveness probe failed: HTTP probe failed with statuscode: 503`,
			wantKind:   "Liveness",
			wantMech:   "http",
			wantMode:   "http_error",
			wantStatus: "503",
		},
		{
			name:          "http probe EOF — backend partition crashes the app",
			note:          `Readiness probe failed: Get "http://10.3.11.144:8080/health": EOF`,
			wantKind:      "Readiness",
			wantMech:      "http",
			wantMode:      "connection_reset",
			wantTargetSub: "8080/health",
		},
		{
			name:     "startup http context deadline (timeout)",
			note:     `Startup probe failed: Get "http://10.0.0.2:80/": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
			wantKind: "Startup",
			wantMech: "http",
			wantMode: "timeout",
		},
		{
			name:     "tcp refused",
			note:     `Readiness probe failed: dial tcp 10.0.0.3:5432: connect: connection refused`,
			wantKind: "Readiness",
			wantMech: "tcp",
			wantMode: "connection_refused",
		},
		{
			name:     "containerd errored/context-canceled",
			note:     `Readiness probe errored and resulted in unknown state: rpc error: code = Canceled desc = context canceled`,
			wantKind: "Readiness",
			wantMode: "timeout",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := parseProbe(tc.note)
			require.True(t, ok)
			require.Equal(t, tc.wantKind, p.Kind)
			require.Equal(t, tc.wantMech, p.Mechanism)
			require.Equal(t, tc.wantMode, p.FailureMode)
			if tc.wantTimeout != "" {
				require.Equal(t, tc.wantTimeout, p.Timeout)
			}
			if tc.wantStatus != "" {
				require.Equal(t, tc.wantStatus, p.StatusCode)
			}
			if tc.wantTargetSub != "" {
				require.Contains(t, p.Target, tc.wantTargetSub)
			}
		})
	}
}

func TestParseProbe_NonProbeNote(t *testing.T) {
	_, ok := parseProbe("Back-off restarting failed container")
	require.False(t, ok)
}

func TestProbeHypotheses_TimeoutListsNetworkPartitionFirst(t *testing.T) {
	hyps := probeHypotheses(probeInfo{FailureMode: "timeout"})
	require.NotEmpty(t, hyps)
	require.Equal(t, "network_partition", hyps[0].Cause,
		"a probe timeout should surface network partition as the leading candidate cause")
}

func TestFailureText_ProbeFailureEnriched(t *testing.T) {
	e := &event.Event{
		InvolvedNamespace: "net-test",
		InvolvedName:      "net-victim-abc",
		InvolvedKind:      "Pod",
		Reason:            "Unhealthy",
		Note:              `Readiness probe failed: command timed out: "sh -c nslookup kubernetes.default.svc.cluster.local" timed out after 3s`,
	}
	d := filter.Decision{SignalType: "probe_failure", Category: "pod_lifecycle", Severity: "warning"}

	txt := failureText(e, d, probectx.ProbeContext{})
	// states what specifically timed out
	require.Contains(t, txt, "timed out after 3s")
	require.Contains(t, txt, "nslookup")
	require.Contains(t, txt, "net-test/net-victim-abc")
	// includes the hypothesis section with network partition
	require.Contains(t, txt, "Possible causes")
	require.Contains(t, strings.ToLower(txt), "network")
	// makes clear it's a symptom, not a verdict
	require.Contains(t, strings.ToLower(txt), "symptom signal")

	// structured details carry the probe + hypotheses for the agent
	details := buildDetails(e, d, probectx.ProbeContext{}, "indeterminate", "deadbeef00000000", 0.4)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(details), &payload))
	require.Contains(t, payload, "probe")
	require.Contains(t, payload, "hypotheses")
	probe := payload["probe"].(map[string]any)
	require.Equal(t, "timeout", probe["failureMode"])
	require.Equal(t, "3s", probe["timeout"])
}

func TestFailureText_InfoTierSignalUnenriched(t *testing.T) {
	// `killing` has no catalog entry (info-tier) → raw note passthrough.
	e := &event.Event{Reason: "Killing", Note: "Stopping container app"}
	d := filter.Decision{SignalType: "killing"}
	require.Equal(t, "Stopping container app", failureText(e, d, probectx.ProbeContext{}))
}

func TestFailureText_CatalogSignalsEnriched(t *testing.T) {
	cases := []struct {
		signalType string
		reason     string
		note       string
		wantCause  string // leading candidate cause's detail substring
	}{
		{"back_off", "BackOff", "Back-off restarting failed container", "exits non-zero"},
		{"oom_kill", "OOMKilled", "Container exceeded memory limit", "exceeds the memory limit"},
		{"image_pull_failure", "Failed", `Failed to pull image "x/y:z": not found`, "repository/name/tag"},
		{"failed_scheduling", "FailedScheduling", "0/3 nodes are available: insufficient cpu", "enough free CPU/memory"},
		{"failed_mount", "FailedMount", "Unable to attach or mount volumes", "PVC is still Pending"},
		{"evicted", "Evicted", "The node was low on resource: memory", "pressure"},
	}
	for _, tc := range cases {
		t.Run(tc.signalType, func(t *testing.T) {
			e := &event.Event{
				InvolvedNamespace: "ns", InvolvedName: "pod-1", InvolvedKind: "Pod",
				Reason: tc.reason, Note: tc.note,
			}
			d := filter.Decision{SignalType: tc.signalType, Category: "x", Severity: "warning"}
			txt := failureText(e, d, probectx.ProbeContext{})
			require.Contains(t, txt, "Possible causes", "signal %s should be enriched", tc.signalType)
			require.Contains(t, txt, tc.wantCause, "signal %s missing its leading cause", tc.signalType)
			require.Contains(t, strings.ToLower(txt), "symptom signal")
			require.Contains(t, txt, "ns/pod-1")

			// structured hypotheses present in details
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(buildDetails(e, d, probectx.ProbeContext{}, "resource_exhaustion", "ep0000000000000a", 0.85)), &payload))
			require.Contains(t, payload, "hypotheses")
		})
	}
}

// TestProbeClassify_CPUThrottledTimeout reproduces the misclassification
// the probe-context enrichment was added to fix: a Readiness probe HTTP
// timeout on a pod with a tight CPU limit (50m) and observed CFS throttle.
// Without the context the mapper would lead with network_partition; with
// it, the leading hypothesis flips to dependency_saturation under
// fault_class=resource_exhaustion.
func TestProbeClassify_CPUThrottledTimeout(t *testing.T) {
	p, ok := parseProbe(`Readiness probe failed: Get "http://10.3.11.128:8080/health": timed out`)
	require.True(t, ok)
	require.Equal(t, "timeout", p.FailureMode)

	ctx := probectx.ProbeContext{
		Available:          true,
		CPULimitMillicores: 50,
		CPUThrottleRate:    2.5,
		RestartsLast15m:    0,
	}
	hyps, faultClass, conf := probeClassify(p, ctx)
	require.Equal(t, FaultClassResourceExhaustion, faultClass)
	require.Equal(t, 0.85, conf)
	require.NotEmpty(t, hyps)
	require.Equal(t, "dependency_saturation", hyps[0].Cause)
	require.Contains(t, hyps[0].Detail, "CPU CFS throttle")
}

// Same kubelet note, no probe context — backward-compatible behavior:
// fault_class indeterminate, network_partition still leads (the legacy
// "timeout → network" hypothesis from before the enrichment landed).
func TestProbeClassify_TimeoutNoContext_BackwardCompat(t *testing.T) {
	p, ok := parseProbe(`Readiness probe failed: Get "http://10.3.11.128:8080/health": timed out`)
	require.True(t, ok)
	hyps, faultClass, conf := probeClassify(p, probectx.ProbeContext{})
	require.Equal(t, FaultClassIndeterminate, faultClass)
	require.Equal(t, 0.4, conf)
	require.NotEmpty(t, hyps)
	require.Equal(t, "network_partition", hyps[0].Cause)
}

func TestProbeClassify_ConnectionRefusedWithRestarts_CrashLoop(t *testing.T) {
	p, ok := parseProbe(`Liveness probe failed: Get "http://10.0.0.1:8080/healthz": dial tcp 10.0.0.1:8080: connect: connection refused`)
	require.True(t, ok)
	ctx := probectx.ProbeContext{Available: true, RestartsLast15m: 3}
	hyps, faultClass, conf := probeClassify(p, ctx)
	require.Equal(t, FaultClassCrashLoop, faultClass)
	require.Equal(t, 0.7, conf)
	require.NotEmpty(t, hyps)
	require.Equal(t, "container_restarting", hyps[0].Cause)
}

func TestProbeClassify_ConnectionResetWithRestarts_CrashLoop(t *testing.T) {
	p, ok := parseProbe(`Readiness probe failed: Get "http://10.3.11.144:8080/health": EOF`)
	require.True(t, ok)
	ctx := probectx.ProbeContext{Available: true, RestartsLast15m: 3}
	hyps, faultClass, conf := probeClassify(p, ctx)
	require.Equal(t, FaultClassCrashLoop, faultClass)
	require.Equal(t, 0.7, conf)
	require.NotEmpty(t, hyps)
	require.Equal(t, "container_restarting", hyps[0].Cause)
}

func TestProbeClassify_DNS_NetworkPartition(t *testing.T) {
	p, ok := parseProbe(`Readiness probe failed: lookup foo.svc no such host`)
	require.True(t, ok)
	require.Equal(t, "dns", p.FailureMode)
	hyps, faultClass, conf := probeClassify(p, probectx.ProbeContext{})
	require.Equal(t, FaultClassNetworkPartition, faultClass)
	require.Equal(t, 0.8, conf)
	require.NotEmpty(t, hyps)
}

func TestProbeClassify_HTTPError_Indeterminate(t *testing.T) {
	p, ok := parseProbe(`Liveness probe failed: HTTP probe failed with statuscode: 503`)
	require.True(t, ok)
	hyps, faultClass, conf := probeClassify(p, probectx.ProbeContext{})
	require.Equal(t, FaultClassIndeterminate, faultClass)
	require.Equal(t, 0.4, conf)
	require.NotEmpty(t, hyps)
	require.Equal(t, "app_unhealthy", hyps[0].Cause)
}

func TestEveryReasonMapSignalIsEnrichedOrInfo(t *testing.T) {
	// Guard: each warning/critical EV signal_type must have a candidate-cause
	// catalog entry (probe_failure is parsed, not cataloged). info-tier ones
	// may be unenriched.
	infoTier := map[string]bool{"killing": true, "volume_failed_delete": true}
	for _, st := range []string{
		"back_off", "probe_failure", "oom_kill", "container_error", "container_cannot_run",
		"failed_scheduling", "unschedulable", "failed_mount", "failed_attach_volume",
		"evicted", "image_pull_failure",
	} {
		if st == "probe_failure" {
			continue // handled by parseProbe
		}
		_, ok := signalHypotheses[st]
		require.True(t, ok, "warning/critical signal %q must have a candidate-cause catalog entry", st)
	}
	_ = infoTier
}
