// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package mapper

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/probectx"
)

// Probe-failure enrichment.
//
// A kubelet "Unhealthy" event (signal_type probe_failure) only tells us a
// symptom: a probe failed. This file parses what specifically failed out of
// the kubelet note, and attaches an ordered list of CANDIDATE causes — a
// differential, not a determination. The watcher is a detection layer; it
// does not assert a root cause (that's the downstream consumer's RCA job).
// The hypotheses exist so an operator or agent has the usual suspects in
// front of them and can confirm one via correlation (a recent
// NetworkPolicy/ConfigMap change, CoreDNS health, caller error rates, etc.).

// probeInfo is the structured form of a parsed probe-failure note.
type probeInfo struct {
	Kind        string // Readiness | Liveness | Startup | (empty if unknown)
	Mechanism   string // exec | http | tcp | grpc | (empty)
	Target      string // the command, URL, or address the probe checked
	FailureMode string // timeout | connection_refused | http_error | dns | unknown
	Timeout     string // e.g. "3s", when the note states it
	StatusCode  string // for http_error, e.g. "503"
}

type hypothesis struct {
	Cause      string  `json:"cause"`
	Detail     string  `json:"detail"`
	Confidence float64 `json:"confidence,omitempty"`
}

var (
	reProbeKind   = regexp.MustCompile(`(?i)\b(readiness|liveness|startup)\s+probe\s+(?:failed|errored)`)
	reQuotedCmd   = regexp.MustCompile(`(?:command(?:\s+timed\s+out)?:\s*)?"([^"]+)"`)
	reHTTPGet     = regexp.MustCompile(`(?i)Get\s+"([^"]+)"`)
	reTimedOutAft = regexp.MustCompile(`timed out after\s+(\S+)`)
	reStatusCode  = regexp.MustCompile(`statuscode:\s*(\d{3})`)
	reTCPDial     = regexp.MustCompile(`dial tcp\s+(\S+?):`)
)

// parseProbe extracts structured detail from a kubelet probe-failure note.
// Returns ok=false when the note doesn't look like a probe failure.
func parseProbe(note string) (probeInfo, bool) {
	n := strings.TrimSpace(note)
	if n == "" {
		return probeInfo{}, false
	}
	var p probeInfo
	if m := reProbeKind.FindStringSubmatch(n); m != nil {
		// Probe kinds are ASCII short words ("liveness", "readiness",
		// "startup"); avoid pulling in golang.org/x/text/cases just for
		// one upper-first-letter op. strings.Title was deprecated in Go 1.18.
		p.Kind = titleCaseASCII(m[1])
	} else if !strings.Contains(strings.ToLower(n), "probe") {
		return probeInfo{}, false
	}

	lower := strings.ToLower(n)

	// Mechanism + target.
	switch {
	case reHTTPGet.MatchString(n):
		p.Mechanism = "http"
		p.Target = reHTTPGet.FindStringSubmatch(n)[1]
	case strings.Contains(lower, "http probe failed"), reStatusCode.MatchString(n):
		// e.g. "HTTP probe failed with statuscode: 503" (no Get "..." form)
		p.Mechanism = "http"
	case strings.Contains(lower, "command"):
		p.Mechanism = "exec"
		if m := reQuotedCmd.FindStringSubmatch(n); m != nil {
			p.Target = m[1]
		}
	case strings.Contains(lower, "dial tcp"):
		p.Mechanism = "tcp"
		if m := reTCPDial.FindStringSubmatch(n); m != nil {
			p.Target = m[1]
		}
	case strings.Contains(lower, "grpc"):
		p.Mechanism = "grpc"
	}

	// Failure mode (order matters: check specific before generic).
	// NOTE: a timeout is classified as "timeout" even when the probe
	// command happens to be a DNS tool (e.g. `nslookup`) — "command timed
	// out" is a timeout; the dns_failure possibility is already carried in
	// the timeout hypotheses. The "dns" mode is reserved for the kubelet
	// note actually reporting a resolution error.
	switch {
	case reStatusCode.MatchString(n):
		p.FailureMode = "http_error"
		p.StatusCode = reStatusCode.FindStringSubmatch(n)[1]
	case strings.Contains(lower, "connection refused"):
		p.FailureMode = "connection_refused"
	case strings.Contains(lower, "eof"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "reset by peer"),
		strings.Contains(lower, "broken pipe"),
		strings.Contains(lower, "server closed"):
		// The probe completed the TCP handshake but the connection was torn
		// down before a full HTTP response — typically the container is
		// crashing/restarting (it accepts then dies) or its health handler
		// aborts because a backend it depends on is unreachable. Distinct
		// from "connection_refused" (nothing listening) and "timeout" (no
		// response in time).
		p.FailureMode = "connection_reset"
	case strings.Contains(lower, "timed out"),
		strings.Contains(lower, "context deadline exceeded"),
		strings.Contains(lower, "context canceled"),
		strings.Contains(lower, "context cancelled"),
		strings.Contains(lower, "i/o timeout"),
		strings.Contains(lower, "no route to host"):
		// "errored and resulted in unknown state: ... context canceled" is
		// what containerd emits when an exec/network probe doesn't complete
		// in time and the kubelet cancels it — treated as a non-completion
		// (timeout-class), so the network-partition differential applies.
		p.FailureMode = "timeout"
	case strings.Contains(lower, "no such host"),
		strings.Contains(lower, "server misbehaving"),
		strings.Contains(lower, "lookup ") && strings.Contains(lower, "no such"):
		p.FailureMode = "dns"
	default:
		p.FailureMode = "unknown"
	}
	if m := reTimedOutAft.FindStringSubmatch(n); m != nil {
		p.Timeout = m[1]
	}
	return p, true
}

// probeHypotheses returns the ordered candidate causes for a probe failure,
// most→least likely given the observed failure mode. Deliberately a
// differential: every entry is a possibility to confirm, not a verdict.
func probeHypotheses(p probeInfo) []hypothesis {
	switch p.FailureMode {
	case "timeout":
		return []hypothesis{
			{Cause: "network_partition", Detail: "Blocked or dropped egress (NetworkPolicy, CNI, or cloud security-group change) — the probe can't reach its target."},
			{Cause: "dns_failure", Detail: "DNS resolution failing or slow (CoreDNS unavailable/overloaded, or the queried name no longer resolves)."},
			{Cause: "dependency_saturation", Detail: "Target reachable but not responding in time — the dependency is overloaded or CPU/IO-starved."},
			{Cause: "application_stall", Detail: "Deadlock, GC pause, or event-loop stall inside the container."},
			{Cause: "probe_too_aggressive", Detail: "Probe timeout tighter than the workload's normal latency (misconfiguration, not a fault)."},
		}
	case "connection_refused":
		return []hypothesis{
			{Cause: "app_not_listening", Detail: "Process not bound to the probe port — still starting, crashed, or listening on a different port."},
			{Cause: "container_restarting", Detail: "Container restarted (crashloop/OOM) and hasn't re-bound the port yet."},
			{Cause: "probe_wrong_port", Detail: "Probe points at the wrong port/path (misconfiguration)."},
		}
	case "connection_reset":
		return []hypothesis{
			{Cause: "container_restarting", Detail: "Container is crashing/restarting (liveness kill, panic, or OOM) and drops the probe connection mid-response."},
			{Cause: "app_crash_on_path", Detail: "Process accepts the connection then exits or closes before replying — a panic or fatal error on the probed handler."},
			{Cause: "dependency_failure", Detail: "The health handler depends on a backend it can't reach (blocked egress/NetworkPolicy, DNS failure, or a down dependency such as a database) and aborts the request."},
			{Cause: "overload", Detail: "Application saturated and resetting connections under load."},
		}
	case "http_error":
		return []hypothesis{
			{Cause: "app_unhealthy", Detail: fmt.Sprintf("Application returned HTTP %s from its health endpoint — it considers itself unhealthy.", p.StatusCode)},
			{Cause: "dependency_failure", Detail: "A downstream dependency the health check exercises is failing."},
			{Cause: "overload", Detail: "Application overloaded and shedding load on the health path."},
		}
	case "dns":
		return []hypothesis{
			{Cause: "dns_partition", Detail: "Cluster DNS unreachable from this pod (CoreDNS down, or egress to kube-dns blocked by a NetworkPolicy)."},
			{Cause: "name_does_not_exist", Detail: "The probed name no longer resolves (Service deleted/renamed, or wrong FQDN)."},
			{Cause: "coredns_degraded", Detail: "CoreDNS up but erroring/slow — check the CoreDNS pods' logs and error rate."},
		}
	default:
		return []hypothesis{
			{Cause: "unclassified", Detail: "Probe failed for a reason not matched by known patterns — inspect the raw probe message and the container logs."},
		}
	}
}

// probeClassify is the context-aware projection from a parsed probe note
// + pod-resource snapshot to a fault_class + confidence + reordered
// differential. The decision tree is documented inline; the priority is
// important — earlier branches take precedence.
//
// The returned hyps slice is the (possibly reordered) candidate-cause
// list; the leading entry's Confidence is set to the value returned in
// the third return.
func probeClassify(p probeInfo, ctx probectx.ProbeContext) (hyps []hypothesis, faultClass string, confidence float64) {
	// 1. CPU-throttle-induced timeout — the misclassification the
	//    enrichment was added to fix. Small CPU limit + observed throttle
	//    rate is a strong signal that the workload is starved, not the
	//    network.
	if ctx.Available && ctx.CPULimitMillicores > 0 && ctx.CPULimitMillicores <= 150 && ctx.CPUThrottleRate > 0.3 {
		base := probeHypotheses(p)
		hyps = reorderLeading(base, "dependency_saturation")
		if len(hyps) > 0 && hyps[0].Cause == "dependency_saturation" {
			hyps[0].Detail = fmt.Sprintf(
				"CPU CFS throttle observed at %.2f/s with cpu_limit=%dm — the container is throttled before it can respond to the probe; raise the CPU limit or reduce work on the probed path.",
				ctx.CPUThrottleRate, ctx.CPULimitMillicores,
			)
		}
		return hyps, FaultClassResourceExhaustion, 0.85
	}

	// 2. DNS resolution failure — kubelet note explicitly reports a
	//    name-resolution failure.
	if p.FailureMode == "dns" {
		return probeHypotheses(p), FaultClassNetworkPartition, 0.8
	}

	// 3. connection_refused with concurrent restart activity — the
	//    container is crashing and the kubelet's probe arrives between
	//    crashes.
	if p.FailureMode == "connection_refused" && ctx.Available && ctx.RestartsLast15m >= 2 {
		base := probeHypotheses(p)
		return reorderLeading(base, "container_restarting"), FaultClassCrashLoop, 0.7
	}

	// 4. connection_reset / EOF with concurrent restarts — same crash
	//    pattern but the container made it far enough to accept the TCP
	//    handshake before falling over.
	if p.FailureMode == "connection_reset" && ctx.Available && ctx.RestartsLast15m >= 2 {
		base := probeHypotheses(p)
		return reorderLeading(base, "container_restarting"), FaultClassCrashLoop, 0.7
	}

	// 5. connection_refused, no restart info — could be a crash, but
	//    could also be "still warming up" or wrong-port; we don't have
	//    enough context to commit.
	if p.FailureMode == "connection_refused" {
		return probeHypotheses(p), FaultClassIndeterminate, 0.5
	}

	// 6. http_error — application returned a non-2xx; class depends on
	//    why the app considers itself unhealthy, which we don't see
	//    from the event.
	if p.FailureMode == "http_error" {
		return probeHypotheses(p), FaultClassIndeterminate, 0.4
	}

	// 7. timeout (no enrichment hit) — backward-compat behavior: the
	//    differential leads with network_partition (which IS the most
	//    common cause when we have nothing else to go on), but we mark
	//    the fault_class indeterminate so downstream consumers know
	//    this is a low-confidence call.
	if p.FailureMode == "timeout" {
		return probeHypotheses(p), FaultClassIndeterminate, 0.4
	}

	// 8. unknown failure mode.
	return probeHypotheses(p), FaultClassIndeterminate, 0.4
}

// reorderLeading promotes the first hypothesis whose Cause matches `cause`
// to the head of the slice. If no match, returns hyps unchanged. The
// promoted entry retains its original Detail (callers that want to
// customize the leading detail should mutate hyps[0] after the call).
func reorderLeading(hyps []hypothesis, cause string) []hypothesis {
	if len(hyps) == 0 {
		return hyps
	}
	for i, h := range hyps {
		if h.Cause != cause {
			continue
		}
		if i == 0 {
			return hyps
		}
		out := make([]hypothesis, 0, len(hyps))
		out = append(out, h)
		out = append(out, hyps[:i]...)
		out = append(out, hyps[i+1:]...)
		return out
	}
	return hyps
}

// probeHeader builds the "what specifically failed" first line for a
// probe_failure Result (the candidate-cause list + caveat are appended by
// the shared renderer in hypotheses.go).
func probeHeader(e *event.Event, p probeInfo) string {
	var b strings.Builder
	kind := p.Kind
	if kind == "" {
		kind = "A"
	}
	subject := specName(e)
	switch {
	case p.Mechanism == "exec" && p.Target != "":
		fmt.Fprintf(&b, "%s probe failed for %s: exec %q", kind, subject, p.Target)
	case p.Mechanism == "http" && p.Target != "":
		fmt.Fprintf(&b, "%s probe failed for %s: HTTP GET %s", kind, subject, p.Target)
	case p.Mechanism == "tcp" && p.Target != "":
		fmt.Fprintf(&b, "%s probe failed for %s: TCP dial %s", kind, subject, p.Target)
	default:
		fmt.Fprintf(&b, "%s probe failed for %s", kind, subject)
	}
	switch p.FailureMode {
	case "timeout":
		if p.Timeout != "" {
			fmt.Fprintf(&b, " — timed out after %s.", p.Timeout)
		} else {
			b.WriteString(" — timed out.")
		}
	case "connection_refused":
		b.WriteString(" — connection refused.")
	case "connection_reset":
		b.WriteString(" — connection closed (EOF/reset) before a response.")
	case "http_error":
		fmt.Fprintf(&b, " — returned HTTP %s.", p.StatusCode)
	case "dns":
		b.WriteString(" — DNS resolution failed.")
	default:
		b.WriteString(".")
	}
	return b.String()
}

// titleCaseASCII lowercases the input and uppercases its first byte. Used
// only on the constrained ASCII probe-kind set ("liveness", "readiness",
// "startup"); not safe for arbitrary Unicode. Replaces the deprecated
// strings.Title call (deprecated since Go 1.18 because its word-boundary
// handling doesn't match Unicode punctuation correctly).
func titleCaseASCII(s string) string {
	s = strings.ToLower(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
