package mutation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWireValues pins the values that leave the watcher in every Result:
// Kind is the Result's spec.kind and, lowercased, part of its name and of
// its source label; Op is the "op" field of its Details. Renaming one would
// rename existing Results or break whatever reads them.
func TestWireValues(t *testing.T) {
	require.Equal(t, "ConfigMap", string(KindConfigMap))
	require.Equal(t, "Add", string(OpAdd))
	require.Equal(t, "Update", string(OpUpdate))
	require.Equal(t, "Delete", string(OpDelete))
}
