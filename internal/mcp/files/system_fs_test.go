package files

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSystemNamespaceRejectsEmptyOwner asserts the §2.8 invariant that a system
// handle can never be widened back into the user namespace via the empty string.
func TestSystemNamespaceRejectsEmptyOwner(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	_, err := svc.SystemNamespace("")
	require.Error(t, err)
	_, err = svc.SystemNamespace("   ")
	require.Error(t, err)
}

// TestSystemNamespaceConstructorReturnsHandle verifies a non-empty owner yields
// a usable handle whose owner is captured by the closure.
func TestSystemNamespaceConstructorReturnsHandle(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	handle, err := svc.SystemNamespace("pageindex")
	require.NoError(t, err)
	require.NotNil(t, handle)

	concrete, ok := handle.(*systemFS)
	require.True(t, ok)
	require.Equal(t, "pageindex", concrete.owner)
	require.Same(t, svc, concrete.svc)
	require.Equal(t, "system:pageindex", concrete.systemAuth().APIKeyHash)
}
