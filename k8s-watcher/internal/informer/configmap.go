package informer

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// ChangeType constants name the three ConfigMap changes the informer reports.
const (
	ChangeTypeAdd    = "add"
	ChangeTypeUpdate = "update"
	ChangeTypeDelete = "delete"
)

// CriticalLabel marks a ConfigMap whose changes are operationally
// significant. It is the Detection Pack's key, so the same label an operator
// already sets for the Pack's configmap-watcher keeps working here.
const CriticalLabel = "k8sgpt-detection-pack.io/critical"

// criticalOptIn reports whether cm is opted in to change reporting: its
// CriticalLabel must be exactly "true". That is the Detection Pack's rule
// (the Pack's configmap-watcher filter compares the value, not presence), so
// a ConfigMap the Pack would have reported is reported here and no other.
func criticalOptIn(cm *corev1.ConfigMap) bool {
	return cm != nil && cm.Labels[CriticalLabel] == "true"
}

// ConfigMapEvent carries a ConfigMap change notification.
//
// CM is the object after the change (for a delete, the last state the cache
// held). Old is the object before an update, and nil otherwise. Both carry
// per-key digests in place of values (see stripConfigMapData).
//
// Critical reports that the change should be reported. That is the case for:
//
//   - an add that arrives after the initial list, of a ConfigMap already
//     opted in. The initial list replays every existing ConfigMap as an add,
//     so reporting those would re-report the whole cluster on every restart;
//     a create after that is a real event (and is what the chart's CM lane
//     helm test produces);
//   - an update that changed the CONTENT of a ConfigMap opted in on both
//     revisions (see criticalDrift);
//   - a delete of an opted-in ConfigMap.
type ConfigMapEvent struct {
	CM         *corev1.ConfigMap
	Old        *corev1.ConfigMap
	ChangeType string
	Critical   bool
}

// ConfigMapHandler is called for each ConfigMap add/update/delete.
type ConfigMapHandler func(ev ConfigMapEvent)

// DataDigestAnnotation carries the content digest computed in stripConfigMapData.
//
// Written onto the CACHED copy only and never sent to the API server, so it does
// not appear on the real object. It exists because the strip is irreversible: the
// digest has to be taken while the values are still in hand, and this is the only
// field that carries it forward to the UpdateFunc comparison.
//
// Its presence also marks a copy as already stripped (see stripConfigMapData),
// so the key must be one no real object can carry. The ':' makes it an invalid
// annotation key, which the API server rejects: a user cannot set it, either to
// collide with it or to skip the strip and pin the digest.
const DataDigestAnnotation = "k8sgpt-detection-pack.io/cache:configmap-data-digest"

// configMapDataDigest digests the ConfigMap's content: every Data and BinaryData
// key with its value.
//
// Keys are sorted so the digest is stable across map iteration order — an
// unsorted digest would differ on every re-delivery and report drift constantly.
// Lengths are written before each key and value so no pair of distinct contents
// can concatenate to the same byte stream ({"ab": "c"} vs {"a": "bc"}).
//
// The two maps are digested under separate prefixes because a key may legally
// appear in only one of them, and the digest must distinguish which.
func configMapDataDigest(cm *corev1.ConfigMap) string {
	h := sha256.New()
	keys := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := cm.Data[k]
		_, _ = fmt.Fprintf(h, "d|%d|%s|%d|%s", len(k), k, len(v), v)
	}
	bkeys := make([]string, 0, len(cm.BinaryData))
	for k := range cm.BinaryData {
		bkeys = append(bkeys, k)
	}
	sort.Strings(bkeys)
	for _, k := range bkeys {
		v := cm.BinaryData[k]
		_, _ = fmt.Fprintf(h, "b|%d|%s|%d|", len(k), k, len(v))
		_, _ = h.Write(v)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// digestKey keys valueDigest. It is random per process, so a digest that
// reaches a Result cannot be matched against the SHA-256 of guessed values
// ("on", "off", "true"): a plain hash of a short value gives the value away.
// Digests only need to agree within one process: the cache comparisons that
// use them never span a restart.
var digestKey = newDigestKey()

func newDigestKey() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(fmt.Sprintf("informer: read random digest key: %v", err))
	}
	return k
}

// valueDigest stands in for one ConfigMap value in the cache: its length and
// the first 16 hex digits of its HMAC-SHA-256 under digestKey, as
// "<len>:<sha16>".
//
// The Pack's diff compares values per key to count added / removed / changed
// keys and prints both sides. Storing a digest per key keeps those counts
// right without the value itself ever reaching the cache, a Result or Slack.
// Sixteen hex digits (64 bits) are plenty to tell two revisions of one key
// apart; they are not meant to resist a deliberate collision.
func valueDigest(v []byte) string {
	mac := hmac.New(sha256.New, digestKey)
	_, _ = mac.Write(v)
	return fmt.Sprintf("%d:%s", len(v), hex.EncodeToString(mac.Sum(nil))[:16])
}

// stripConfigMapData returns a copy of the ConfigMap with every .Data and
// .BinaryData value replaced by its valueDigest, retaining the key names.
// This keeps values out of the cache, which bounds memory and keeps
// potentially sensitive content out of everything downstream.
//
// It also drops every annotation: tools that apply objects store the whole
// applied object, values included, in one (kubectl's
// last-applied-configuration, kapp's original, wrangler's applied), and
// nothing downstream reads annotations.
//
// Records configMapDataDigest in DataDigestAnnotation, the only annotation
// the copy keeps: this is the last point at which the values exist, and the
// drift comparison needs to tell a real content change from a metadata-only
// write.
//
// Idempotent: a copy that already carries DataDigestAnnotation is returned as
// is. client-go may transform an object twice. With the watch-list initial sync
// (WatchListClient, on by default since 1.35) the reflector's temporary store
// transforms each listed object and RealFIFO.Replace transforms it again. A
// second strip would digest the digests, so the cached copy of every object from
// the initial list would differ from its next watch revision in every key and in
// the whole-content digest.
func stripConfigMapData(obj interface{}) (interface{}, error) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return obj, nil
	}
	if _, stripped := cm.Annotations[DataDigestAnnotation]; stripped {
		return cm, nil
	}
	out := cm.DeepCopy()
	digest := configMapDataDigest(cm)
	for k, v := range out.Data {
		out.Data[k] = valueDigest([]byte(v))
	}
	for k, v := range out.BinaryData {
		out.BinaryData[k] = []byte(valueDigest(v))
	}
	out.Annotations = map[string]string{DataDigestAnnotation: digest}
	return out, nil
}

// criticalDrift reports whether this update changed the CONTENT of a ConfigMap
// that was ALREADY opted in via CriticalLabel before the update.
//
// Both revisions must carry the opt-in. Requiring it on newCM alone would make
// turning the feature on for an object its own first drift event — the label add
// is a write like any other — reporting a change, and alerting someone, for an
// administrative act. Requiring it on oldCM makes the opt-in inert on arrival
// and effective from the next real change.
//
// Losing the label is not drift either: that is how to opt out in place, and
// reporting it would mean detection could not be turned off without firing one
// last report.
//
// The digest comparison is what separates a content change both from a
// re-delivery and from a metadata-only write. A relist after a watch drop refills
// the DeltaFIFO via Replace, which reaches UpdateFunc with an unchanged object;
// periodic resync would do the same, and is only not a factor here because this
// informer runs with resyncPeriod=0 (see StartConfigMapInformer). Either way the
// digest is identical and nothing happened. A ResourceVersion comparison could
// not tell those apart from a real edit.
//
// Fails CLOSED on an unusable oldObj (wrong type, or nil) and on a missing digest
// on either side: with no comparable prior content there is no evidence anything
// changed, and answering true would report drift for whatever re-delivery or
// bypassed TransformFunc produced the incomparable pair.
func criticalDrift(oldObj interface{}, newCM *corev1.ConfigMap) bool {
	if !criticalOptIn(newCM) {
		return false
	}
	oldCM, ok := oldObj.(*corev1.ConfigMap)
	if !ok || !criticalOptIn(oldCM) {
		return false
	}
	oldDigest := oldCM.Annotations[DataDigestAnnotation]
	newDigest := newCM.Annotations[DataDigestAnnotation]
	if oldDigest == "" || newDigest == "" {
		return false
	}
	return oldDigest != newDigest
}

// resolveConfigMapFromDeleteObj extracts a ConfigMap from a DeleteFunc object,
// unwrapping a cache.DeletedFinalStateUnknown tombstone if necessary.
// Returns nil and logs a warning when the type is unexpected.
func resolveConfigMapFromDeleteObj(obj interface{}, logger *slog.Logger) *corev1.ConfigMap {
	if cm, ok := obj.(*corev1.ConfigMap); ok {
		return cm
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		if cm, ok := tombstone.Obj.(*corev1.ConfigMap); ok {
			return cm
		}
		logger.Warn("configmap delete: tombstone holds unexpected type, signal lost",
			"type", fmt.Sprintf("%T", tombstone.Obj))
		return nil
	}
	logger.Warn("configmap delete: received unexpected type, signal lost",
		"type", fmt.Sprintf("%T", obj))
	return nil
}

// configMapHandlers builds the informer callbacks: it classifies each
// notification (see ConfigMapEvent.Critical) and forwards it to handler.
func configMapHandlers(handler ConfigMapHandler, logger *slog.Logger) cache.ResourceEventHandlerDetailedFuncs {
	return cache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(obj interface{}, isInInitialList bool) {
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				handler(ConfigMapEvent{
					CM:         cm,
					ChangeType: ChangeTypeAdd,
					Critical:   !isInInitialList && criticalOptIn(cm),
				})
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if cm, ok := newObj.(*corev1.ConfigMap); ok {
				old, _ := oldObj.(*corev1.ConfigMap)
				handler(ConfigMapEvent{
					CM:         cm,
					Old:        old,
					ChangeType: ChangeTypeUpdate,
					Critical:   criticalDrift(oldObj, cm),
				})
			}
		},
		DeleteFunc: func(obj interface{}) {
			if cm := resolveConfigMapFromDeleteObj(obj, logger); cm != nil {
				// Deleting an opted-in ConfigMap is the most extreme change it can
				// undergo, so the label alone decides: there is no old/new pair to
				// compare, and none is needed.
				handler(ConfigMapEvent{CM: cm, ChangeType: ChangeTypeDelete, Critical: criticalOptIn(cm)})
			}
		},
	}
}

// StartConfigMapInformer starts an informer watching all ConfigMaps cluster-wide
// and returns after the initial cache sync. A TransformFunc replaces values with
// per-key digests before objects enter the cache. resyncPeriod=0 (no periodic
// resync). List and watch failures are logged; client-go's reflector keeps
// retrying them. If the initial list has not completed within syncTimeout
// (DefaultSyncTimeout when 0), it returns an error naming the last failure.
func StartConfigMapInformer(ctx context.Context, client kubernetes.Interface, handler ConfigMapHandler, syncTimeout time.Duration, logger *slog.Logger) error {
	factory := informers.NewSharedInformerFactoryWithOptions(
		client,
		0, // resyncPeriod=0: no periodic resync
		informers.WithTransform(stripConfigMapData),
	)
	inf := factory.Core().V1().ConfigMaps().Informer()
	last, err := trackWatchErrors(inf, logger, "configmap informer watch error")
	if err != nil {
		return fmt.Errorf("set configmap watch error handler: %w", err)
	}
	if _, err := inf.AddEventHandler(configMapHandlers(handler, logger)); err != nil {
		return fmt.Errorf("add configmap handler: %w", err)
	}
	if err := startAndWait(ctx, factory, inf, syncTimeout, last); err != nil {
		return fmt.Errorf("configmap informer: %w", err)
	}
	logger.Info("configmap informer synced")
	return nil
}
