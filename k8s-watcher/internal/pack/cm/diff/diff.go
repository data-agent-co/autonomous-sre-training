// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package diff computes structural diffs between two ConfigMap data maps.
// The diff identifies added / removed / changed keys.
package diff

import (
	"sort"
	"strconv"
	"strings"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/mutation"
)

// ChangeType is one of "added", "removed", "changed".
type ChangeType string

const (
	Added   ChangeType = "added"
	Removed ChangeType = "removed"
	Changed ChangeType = "changed"
)

// Entry describes one key-level change. OldValue and NewValue are what the
// Mutation carried: in this watcher, the per-key "<len>:<sha16>" digests.
type Entry struct {
	Key      string     `json:"key"`
	Change   ChangeType `json:"change"`
	OldValue string     `json:"old_value,omitempty"`
	NewValue string     `json:"new_value,omitempty"`
	// Lengths are the lengths of the values, so consumers can see size
	// changes (see valueLen).
	OldLen int `json:"old_len,omitempty"`
	NewLen int `json:"new_len,omitempty"`
}

// Result is the full diff for a mutation.
type Result struct {
	Entries []Entry `json:"entries"`
	// Summary counts.
	AddedCount   int `json:"added_count"`
	RemovedCount int `json:"removed_count"`
	ChangedCount int `json:"changed_count"`
}

// Compute returns the diff between the mutation's PrevData/PrevBinaryData
// and Data/BinaryData.
func Compute(m *mutation.Mutation) Result {
	if m == nil {
		return Result{}
	}
	// Normalize: Data and BinaryData are diffed as one map.
	prev := normalizedMap(m.PrevData, m.PrevBinaryData)
	curr := normalizedMap(m.Data, m.BinaryData)

	keys := unionKeys(prev, curr)
	res := Result{}
	for _, k := range keys {
		pv, pOK := prev[k]
		cv, cOK := curr[k]
		switch {
		case !pOK && cOK:
			res.Entries = append(res.Entries, makeEntry(k, Added, "", cv))
			res.AddedCount++
		case pOK && !cOK:
			res.Entries = append(res.Entries, makeEntry(k, Removed, pv, ""))
			res.RemovedCount++
		case pOK && cOK && pv != cv:
			res.Entries = append(res.Entries, makeEntry(k, Changed, pv, cv))
			res.ChangedCount++
		}
	}
	return res
}

func makeEntry(key string, change ChangeType, oldV, newV string) Entry {
	return Entry{
		Key:      key,
		Change:   change,
		OldValue: oldV,
		NewValue: newV,
		OldLen:   valueLen(oldV),
		NewLen:   valueLen(newV),
	}
}

// valueLen returns the length of the value v stands for. The watcher's
// informer never passes values on: it caches each one as "<len>:<sha16>",
// the value's length and the first 16 hex digits of its SHA-256, so the
// length is read from that prefix. Anything else is taken to be the value
// itself.
func valueLen(v string) int {
	n, sum, ok := strings.Cut(v, ":")
	if !ok || len(sum) != 16 || strings.Trim(sum, "0123456789abcdef") != "" {
		return len(v)
	}
	l, err := strconv.Atoi(n)
	if err != nil || l < 0 {
		return len(v)
	}
	return l
}

// normalizedMap returns Data and BinaryData as a single string map. Binary
// data is included as the raw byte string. A key is never in both maps (the
// API server rejects a ConfigMap whose Data and BinaryData keys overlap).
func normalizedMap(data map[string]string, binary map[string][]byte) map[string]string {
	out := make(map[string]string, len(data)+len(binary))
	for k, v := range data {
		out[k] = v
	}
	for k, v := range binary {
		out[k] = string(v)
	}
	return out
}

func unionKeys(a, b map[string]string) []string {
	set := map[string]struct{}{}
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
