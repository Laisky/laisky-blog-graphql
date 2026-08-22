package files

import (
	"context"
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

// TestSystemFSWriteUsesCanonicalUTF8Encoding reproduces the PageIndex persistence
// failure where SystemFS passed the unsupported "utf8" alias to the production
// file service. A successful write/read round trip keeps that contract covered.
func TestSystemFSWriteUsesCanonicalUTF8Encoding(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, nil, &memoryCredentialStore{})
	systemFS, err := svc.SystemNamespace("pageindex")
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, systemFS.Write(ctx, "project", "/pageindex/index.json", []byte(`{"/manual.md":{"doc_id":"doc-1"}}`)))

	content, err := systemFS.Read(ctx, "project", "/pageindex/index.json")
	require.NoError(t, err)
	require.JSONEq(t, `{"/manual.md":{"doc_id":"doc-1"}}`, string(content))
}
