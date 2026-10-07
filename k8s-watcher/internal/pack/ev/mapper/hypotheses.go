// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package mapper

import (
	"fmt"
	"strings"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/filter"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/probectx"
)

// Candidate-cause enrichment for ALL events-watcher signal_types.
//
// Every EV signal is a symptom (a kubelet/scheduler/kubelet-volume event),
// not a root cause. For each signal_type we attach an ordered differential
// of the usual causes — most→least likely — so an operator or agent has the
// suspects in front of them. This is explicitly NOT a determination: the
// watcher is a detection layer and leaves root-cause attribution to the
// downstream consumer's correlation/RCA.
//
// probe_failure is special-cased (probe.go parses the kubelet note to pick
// a failure-mode-specific list). Every other signal_type draws from the
// static catalog below, which is keyed on the fault class rather than a
// parsed message because those events' causes are well-bounded.

// signalHypotheses maps a signal_type to its ordered candidate causes.
// probe_failure is intentionally absent here — it's handled by probe.go.
var signalHypotheses = map[string][]hypothesis{
	"back_off": {
		{Cause: "app_crash_on_start", Detail: "Process exits non-zero almost immediately — bad config, a missing env var/Secret, or a failed startup migration."},
		{Cause: "oom_at_start", Detail: "Container OOMKilled before it became ready — memory limit too low for startup footprint."},
		{Cause: "missing_dependency", Detail: "A required dependency (DB, Service, ConfigMap/Secret) isn't reachable at boot, so the process aborts."},
		{Cause: "bad_command_or_image", Detail: "Entrypoint/args wrong, or the image lacks the binary it tries to run."},
		{Cause: "liveness_killing", Detail: "A liveness probe is restarting an app that's actually just slow to warm up."},
	},
	"oom_kill": {
		{Cause: "limit_too_low", Detail: "Working set legitimately exceeds the memory limit — raise the limit/request to match real usage."},
		{Cause: "memory_leak", Detail: "Usage grows unbounded over time (heap or cache leak) until it hits the limit."},
		{Cause: "load_spike", Detail: "A transient burst (large request, batch job, fan-in) briefly exceeded the limit."},
		{Cause: "sidecar_contention", Detail: "Another container in the same pod consumed the shared memory budget."},
	},
	"image_pull_failure": {
		{Cause: "image_or_tag_missing", Detail: "Wrong repository/name/tag, or the tag was deleted from the registry."},
		{Cause: "registry_auth", Detail: "Missing/expired imagePullSecret, or registry credentials are wrong."},
		{Cause: "registry_unreachable", Detail: "Egress to the registry is blocked (NetworkPolicy/firewall) or the registry is down."},
		{Cause: "rate_limited", Detail: "Registry pull rate-limit hit (e.g. Docker Hub anonymous limits)."},
	},
	"failed_scheduling": {
		{Cause: "insufficient_resources", Detail: "No node has enough free CPU/memory to fit the pod's requests."},
		{Cause: "taints_affinity", Detail: "Node taints, nodeSelector, affinity/anti-affinity, or topology-spread exclude every node."},
		{Cause: "no_nodes_available", Detail: "Autoscaler can't add nodes, or all candidate nodes are cordoned/NotReady."},
		{Cause: "pvc_unbound", Detail: "The pod needs a volume that can't be provisioned/bound (e.g. wrong zone or storageclass)."},
	},
	"unschedulable": {
		{Cause: "insufficient_resources", Detail: "No node satisfies the pod's resource requests."},
		{Cause: "taints_affinity", Detail: "Scheduling constraints (taints / affinity / topology spread) leave no eligible node."},
		{Cause: "capacity_exhausted", Detail: "Cluster at capacity and autoscaling is disabled or capped."},
	},
	"provisioning_failed": {
		{Cause: "missing_storageclass", Detail: "PVC references a StorageClass that doesn't exist — create it or update the PVC to reference an existing one."},
		{Cause: "provisioner_not_running", Detail: "StorageClass exists but its provisioner pod is not running — check the CSI driver deployment."},
		{Cause: "quota_exceeded", Detail: "Namespace or cluster storage quota prevented a new PV from being created."},
		{Cause: "provisioner_error", Detail: "Provisioner ran but returned an error — check provisioner logs for zone, capacity, or credentials issues."},
	},
	"failed_mount": {
		{Cause: "pvc_not_bound", Detail: "The PVC is still Pending — no matching PV, or dynamic provisioning is failing."},
		{Cause: "attach_failed", Detail: "CSI attach failed — volume in another zone/node, or the node hit its attach limit."},
		{Cause: "source_missing", Detail: "A referenced Secret/ConfigMap used as a volume doesn't exist."},
		{Cause: "csi_node_not_ready", Detail: "The CSI node plugin isn't running/ready on the scheduled node."},
	},
	"failed_attach_volume": {
		{Cause: "in_use_elsewhere", Detail: "Volume still attached to another node (RWO not detached after the previous pod moved)."},
		{Cause: "attach_limit", Detail: "Node reached its maximum attached-volume count."},
		{Cause: "zone_mismatch", Detail: "Volume is in a different availability zone than the scheduled node."},
		{Cause: "csi_controller", Detail: "The CSI attacher/controller is failing."},
	},
	"evicted": {
		{Cause: "node_resource_pressure", Detail: "Node under memory/disk/PID pressure — kubelet is evicting pods to reclaim it."},
		{Cause: "ephemeral_storage_exceeded", Detail: "Pod exceeded its ephemeral-storage limit (logs/temp files filled the disk)."},
		{Cause: "node_drain", Detail: "Node cordoned/drained for maintenance or scale-down."},
	},
	"container_error": {
		{Cause: "nonzero_exit", Detail: "Process exited with an error code — application bug or invalid configuration."},
		{Cause: "missing_dependency", Detail: "A dependency required at runtime became unavailable."},
		{Cause: "permission_denied", Detail: "Filesystem/securityContext permission prevented the container from doing its work."},
	},
	"container_cannot_run": {
		{Cause: "bad_command", Detail: "Entrypoint/args reference a binary not present (or not executable) in the image."},
		{Cause: "bad_working_dir", Detail: "Configured workingDir doesn't exist in the image."},
		{Cause: "securitycontext_block", Detail: "securityContext (runAsUser / readOnlyRootFilesystem / caps) prevents exec."},
	},
	"cert_not_ready": {
		{Cause: "issuer_not_found", Detail: "The Issuer or ClusterIssuer referenced by the Certificate does not exist — create it or fix the issuerRef name."},
		{Cause: "issuer_not_ready", Detail: "Issuer exists but its Ready condition is False — check the issuer's credentials and webhook connectivity."},
		{Cause: "acme_challenge_failed", Detail: "ACME DNS-01 or HTTP-01 challenge failed — DNS propagation lag, rate limit hit, or misconfigured solver."},
		{Cause: "cert_renewal_stuck", Detail: "Certificate near expiry and renewal is failing — check cert-manager logs and the CertificateRequest status for details."},
	},
}

// catalogConfidence is the confidence set on the leading entry of a
// catalog list. Catalog signals are well-bounded, so downstream consumers
// can compare it against probe_failure's dynamic confidence.
const catalogConfidence = 0.85

// hypothesesFor returns the candidate-cause list for an event's signal_type,
// or nil when the signal isn't enriched (e.g. info-tier signals like
// `killing`). probe_failure is parsed from the note; the rest come from the
// static catalog. The leading entry carries the confidence.
//
// The returned slice is the caller's own: catalog entries are copied, so
// setting the confidence never writes to the shared signalHypotheses table
// (the mapper runs on more than one goroutine).
//
// ctx is the pod-resource snapshot (CPU limit + throttle rate + recent
// restarts) used by probe_failure classification. Pass a zero-value
// ProbeContext for callers that don't have one — those probe_failure
// signals fall back to the legacy kubelet-note-only differential.
func hypothesesFor(e *event.Event, d filter.Decision, ctx probectx.ProbeContext) []hypothesis {
	if d.SignalType == "probe_failure" {
		p, ok := parseProbe(e.Note)
		if !ok {
			return nil
		}
		hyps, _, conf := probeClassify(p, ctx)
		if len(hyps) > 0 && conf > 0 {
			hyps[0].Confidence = conf
		}
		return hyps
	}
	catalog := signalHypotheses[d.SignalType]
	if len(catalog) == 0 {
		return nil
	}
	hyps := append([]hypothesis(nil), catalog...)
	hyps[0].Confidence = catalogConfidence
	return hyps
}

// enrichedFailureText renders the spec.error[].text for any enriched signal:
// a "what happened" header, the numbered candidate causes, and a
// symptom-not-verdict caveat. Returns ok=false when the signal isn't
// enriched, so the caller can fall back to the raw note.
//
// ctx flows through to probe_failure classification; pass a zero-value
// ProbeContext when no enrichment is available.
func enrichedFailureText(e *event.Event, d filter.Decision, ctx probectx.ProbeContext) (string, bool) {
	hyps := hypothesesFor(e, d, ctx)
	if len(hyps) == 0 {
		return "", false
	}

	var b strings.Builder
	// Header: probe failures get the parsed "what specifically failed" line;
	// everything else gets reason + the kubelet note's first line.
	if d.SignalType == "probe_failure" {
		p, _ := parseProbe(e.Note)
		b.WriteString(probeHeader(e, p))
	} else {
		b.WriteString(genericHeader(e, d))
	}

	b.WriteString("\n\nPossible causes (most → least likely):")
	for i, h := range hyps {
		fmt.Fprintf(&b, "\n  %d. %s", i+1, h.Detail)
	}
	b.WriteString("\n\nThis is a symptom signal — confirming the cause requires correlation " +
		"(workload spec, recent config/RBAC changes, node conditions, dependency health).")
	if raw := strings.TrimSpace(e.Note); raw != "" {
		b.WriteString(" Raw event: " + firstLine(raw))
	}
	return b.String(), true
}

// genericHeader builds the "what happened" line for non-probe signals.
func genericHeader(e *event.Event, d filter.Decision) string {
	subject := specName(e)
	note := firstLine(strings.TrimSpace(e.Note))
	if note != "" {
		return fmt.Sprintf("%s (%s) on %s: %s", e.Reason, d.SignalType, subject, note)
	}
	return fmt.Sprintf("%s (%s) on %s.", e.Reason, d.SignalType, subject)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
