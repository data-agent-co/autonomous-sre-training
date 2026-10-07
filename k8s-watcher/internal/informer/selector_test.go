package informer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The fake clientset ignores ListOptions.FieldSelector and returns every
// seeded object regardless, so no delivery test can show that only Warning
// events are watched (see event_informer_test.go). The selector string is the
// part that IS testable without an API server, and it is the whole mechanism:
// a wrong one syncs empty, reports HasSynced true, and goes ready with the
// fault invisible.
func TestWarningEventFieldSelector(t *testing.T) {
	assert.Equal(t, "type=Warning", warningEventFieldSelector())
}
