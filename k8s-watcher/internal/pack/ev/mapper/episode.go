// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

package mapper

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/event"
)

// labelEpisodeID is the Pack-wide label key that groups Results belonging
// to the same fault episode. A downstream consumer can group on this label
// to fold N raw Results into one incident.
const labelEpisodeID = "k8sgpt-detection-pack.io/episode_id"

// episodeBucket is the time-bucket granularity used when constructing the
// episode key. 5 minutes is short enough to keep episodes per-incident
// (not per-day) and long enough to absorb the multi-second drift between
// related events from kubelet + scheduler + restart noise.
const episodeBucket = 5 * time.Minute

// episodeID returns a stable identifier for the fault episode this event
// belongs to. The key components are:
//
//   - involved namespace
//   - workload root (Pod → strip ReplicaSet+pod hash; StatefulSet → strip
//     ordinal). Same hash group for all pods of a Deployment.
//   - fault_class (so two different fault classes on the same workload at
//     the same time are TWO episodes, not one).
//   - the 5-minute time bucket containing the event's LastSeen.
//
// Returns "" when faultClass is empty — info-tier signals don't get an
// episode_id.
func episodeID(e *event.Event, faultClass string, t time.Time) string {
	if faultClass == "" {
		return ""
	}
	if t.IsZero() {
		t = time.Now()
	}
	key := fmt.Sprintf("%s:%s:%s:%d",
		e.InvolvedNamespace,
		workloadRoot(e),
		faultClass,
		t.Truncate(episodeBucket).Unix(),
	)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

// workloadRoot strips per-Pod identity suffixes to surface the owning
// Deployment / StatefulSet name. For non-Pod kinds it returns the involved
// name unchanged.
//
// Algorithm (best-effort; no apiserver lookup):
//   - Pod kind only.
//   - "checkout-7884b8b69f-gs68q" → "checkout"
//     (last seg is 5 alphanum chars, second-to-last is 5-10 alphanum chars
//     = ReplicaSet pod-template-hash + per-pod random suffix; drop both)
//   - "checkout-gs68q" → "checkout"
//     (last seg is 5 alphanum chars; drop it)
//   - "postgres-0", "postgres-1", … → "postgres"
//     (StatefulSet ordinal; drop trailing -<digits>)
//   - "checkout" → "checkout"
func workloadRoot(e *event.Event) string {
	if e.InvolvedKind != "Pod" {
		return e.InvolvedName
	}
	name := e.InvolvedName
	if name == "" {
		return name
	}
	parts := strings.Split(name, "-")
	if len(parts) < 2 {
		return name
	}

	last := parts[len(parts)-1]

	// StatefulSet ordinal: "-<digits>".
	if isAllDigits(last) {
		return strings.Join(parts[:len(parts)-1], "-")
	}

	// Deployment pod (ReplicaSet) — last seg is the 5-char per-pod random
	// suffix. If the segment before it is the 5-10 char pod-template-hash,
	// strip both.
	if isAlnum(last) && len(last) == 5 {
		if len(parts) >= 3 {
			second := parts[len(parts)-2]
			if isAlnum(second) && len(second) >= 5 && len(second) <= 10 {
				return strings.Join(parts[:len(parts)-2], "-")
			}
		}
		return strings.Join(parts[:len(parts)-1], "-")
	}

	return name
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
