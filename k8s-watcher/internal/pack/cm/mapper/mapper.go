// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package mapper translates a Mutation into a K8sGPT *corev1alpha1.Result.
package mapper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/k8sgpt/v1alpha1"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/diff"
	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
)

const (
	BackendIdentifier = "k8sgpt-detection-pack:cm"

	LabelComponent = "k8sgpt-detection-pack.io/component"
	ComponentName  = "cm"

	labelCategory   = "k8sgpt-detection-pack.io/category"
	labelSource     = "k8sgpt-detection-pack.io/source"
	labelSignalType = "k8sgpt-detection-pack.io/signal_type"
	labelSeverity   = "k8sgpt-detection-pack.io/severity"
	labelFaultClass = "k8sgpt-detection-pack.io/fault_class"
)

// Mapper builds Result CRDs.
type Mapper struct {
	Namespace string
}

// New returns a Mapper that places Results into the given namespace.
func New(namespace string) *Mapper {
	return &Mapper{Namespace: namespace}
}

// ToResult builds a Result for the mutation. Caller must ensure the filter
// already admitted this mutation. The diff is computed here.
func (m *Mapper) ToResult(mut *mutation.Mutation) *corev1alpha1.Result {
	d := diff.Compute(mut)

	signalType := signalForOp(mut.Op)

	return &corev1alpha1.Result{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CRDName(mut),
			Namespace: m.Namespace,
			Labels:    crdLabels(mut, signalType),
		},
		Spec: corev1alpha1.ResultSpec{
			Backend:               BackendIdentifier,
			AutoRemediationStatus: corev1alpha1.AutoRemediationStatus{},
			Kind:                  string(mut.Kind),
			Name:                  specName(mut),
			ParentObject:          "",
			Error:                 []corev1alpha1.Failure{{Text: failureText(mut, d)}},
			Details:               buildDetails(mut, d, signalType),
		},
	}
}

// CRDName returns the deterministic metadata.name for a mutation's Result.
// Format: "cm-<kind>-<sanitized-name>-<uid-hash>"
func CRDName(mut *mutation.Mutation) string {
	kind := strings.ToLower(string(mut.Kind))
	name := sanitize(strings.ToLower(mut.Name))
	if name == "" {
		name = "unknown"
	}
	combined := fmt.Sprintf("cm-%s-%s", kind, name)
	if len(combined) > 240 {
		combined = combined[:240]
	}
	return fmt.Sprintf("%s-%s", combined, hashSuffix(mut))
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}

func hashSuffix(mut *mutation.Mutation) string {
	src := mut.UID
	if src == "" {
		src = string(mut.Kind) + "/" + mut.Namespace + "/" + mut.Name
	}
	h := sha256.Sum256([]byte(src))
	return hex.EncodeToString(h[:])[:8]
}

func signalForOp(op mutation.Op) string {
	if op == mutation.OpDelete {
		return "cm_mutation_deleted"
	}
	return "cm_mutation"
}

func crdLabels(mut *mutation.Mutation, signalType string) map[string]string {
	return map[string]string{
		LabelComponent:  ComponentName,
		labelSource:     strings.ToLower(string(mut.Kind)),
		labelCategory:   "configuration_fault",
		labelSignalType: signalType,
		labelSeverity:   "warning",
		labelFaultClass: "config_drift",
	}
}

func specName(mut *mutation.Mutation) string {
	if mut.Namespace == "" {
		return mut.Name
	}
	return mut.Namespace + "/" + mut.Name
}

func failureText(mut *mutation.Mutation, d diff.Result) string {
	verb := "modified"
	switch mut.Op {
	case mutation.OpAdd:
		verb = "added"
	case mutation.OpDelete:
		verb = "deleted"
	}
	return fmt.Sprintf("%s %s/%s %s: %d added, %d removed, %d changed",
		mut.Kind, mut.Namespace, mut.Name, verb,
		d.AddedCount, d.RemovedCount, d.ChangedCount)
}

func buildDetails(mut *mutation.Mutation, d diff.Result, signalType string) string {
	payload := map[string]any{
		"kind":            string(mut.Kind),
		"op":              string(mut.Op),
		"namespace":       mut.Namespace,
		"name":            mut.Name,
		"uid":             mut.UID,
		"resourceVersion": mut.ResourceVersion,
		"observedAt":      mut.ObservedAt,
		"signal_type":     signalType,
		"category":        "configuration_fault",
		"severity":        "warning",
		"diff":            d,
		// Always false: this watcher reports ConfigMaps only (their values
		// arrive as digests), never Secrets. Kept so the Details shape
		// matches the Pack's.
		"redacted": false,
	}
	b, _ := json.Marshal(payload)
	return string(b)
}
