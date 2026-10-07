// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package mapper

// fault_class taxonomy: a coarse, cause-class label that downstream
// consumers can route on without having to memorize every
// K8s-event-shaped signal_type. signal_type still answers "what specifically
// was observed" (e.g. probe_failure); fault_class answers "which category
// of cause is the leading hypothesis" (e.g. network_partition).
//
// Most catalog signals have a well-bounded mapping (an OOMKilled is
// resource_exhaustion regardless of context). probe_failure is the
// exception — its fault_class depends on the parsed probe note PLUS pod-
// resource context (CPU limit + throttle rate + recent restarts), so it's
// derived dynamically in probe.go's probeClassify, NOT in the static map
// below.

// labelFaultClass is the Pack-wide label key for the cause-class
// projection. Set on Result CRDs whenever a fault_class is assignable.
const labelFaultClass = "k8sgpt-detection-pack.io/fault_class"

// The fault_class values the EV pipeline assigns. Kept as a closed set so
// downstream consumers can switch on it without surprises:
//
//	resource_exhaustion   — OOMKilled, Evicted, CPU CFS throttle-driven
//	                        probe timeouts.
//	network_partition     — probe failures whose note reports a DNS
//	                        resolution error.
//	crash_loop            — BackOff, container errors, and probe failures
//	                        with concurrent restart activity (>=2 in the
//	                        last 15m).
//	bad_deployment        — image-pull failures, ContainerCannotRun.
//	scheduling_failure    — FailedScheduling, Unschedulable.
//	volume_failure        — FailedMount, FailedAttachVolume,
//	                        ProvisioningFailed.
//	indeterminate         — we have a symptom but not enough context to
//	                        commit to a class: probe failures the note and
//	                        pod context do not pin to one class, and
//	                        cert-manager failures.
//
// The CM pipeline labels its Results config_drift (see the cm mapper
// package). Anything else is a bug in the mapper.
const (
	FaultClassResourceExhaustion = "resource_exhaustion"
	FaultClassNetworkPartition   = "network_partition"
	FaultClassCrashLoop          = "crash_loop"
	FaultClassBadDeployment      = "bad_deployment"
	FaultClassSchedulingFailure  = "scheduling_failure"
	FaultClassVolumeFailure      = "volume_failure"
	FaultClassIndeterminate      = "indeterminate"
)

// signalToFaultClass maps catalog signals (everything except probe_failure)
// to their cause-class. Info-tier signals (killing, volume_failed_delete)
// are intentionally absent — those Results carry no fault_class label and
// no episode_id either.
//
// probe_failure is dynamic — see probe.go's probeClassify.
var signalToFaultClass = map[string]string{
	"oom_kill":             FaultClassResourceExhaustion,
	"evicted":              FaultClassResourceExhaustion,
	"failed_scheduling":    FaultClassSchedulingFailure,
	"unschedulable":        FaultClassSchedulingFailure,
	"failed_mount":         FaultClassVolumeFailure,
	"failed_attach_volume": FaultClassVolumeFailure,
	"provisioning_failed":  FaultClassVolumeFailure,
	"image_pull_failure":   FaultClassBadDeployment,
	"container_cannot_run": FaultClassBadDeployment,
	"back_off":             FaultClassCrashLoop,
	"container_error":      FaultClassCrashLoop,
	// No class is specific to certificates, so a not-Ready certificate
	// stays indeterminate.
	"cert_not_ready": FaultClassIndeterminate,
}
