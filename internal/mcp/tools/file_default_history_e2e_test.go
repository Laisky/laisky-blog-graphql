package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// historyIDWithContent returns the one history ID of project:path whose snapshot holds content.
func (h *overlayToolHarness) historyIDWithContent(ctx context.Context, project, path, content string) string {
	h.t.Helper()
	var found []string
	for _, id := range h.historyIDs(ctx, project, path) {
		snapshot := h.call(ctx, h.readVersionTool, map[string]any{"project": project, "path": path, "history_id": id})
		if snapshot["content"] == content {
			found = append(found, id)
		}
	}
	require.Len(h.t, found, 1, "%s:%s has exactly one snapshot %q", project, path, content)
	return found[0]
}

// TestFileToolsDefaultProjectHistoryIsPerFile drives the history tools over an
// override and the default it hides: each path shows the history of the file it
// resolves to, and a history ID of the other layer is refused without effect
// (docs/proposals/fileio_default_project.md §6).
func TestFileToolsDefaultProjectHistoryIsPerFile(t *testing.T) {
	h := newOverlayToolHarness(t)
	ctx, foreign := h.tenant, h.foreign
	first := h.create(ctx, files.DefaultProject, "/h.md", "needle h default v1")
	h.update(ctx, files.DefaultProject, "/h.md", "needle h default v2", first)
	h.override(ctx, "alpha", "/h.md", "needle h alpha v1")
	h.update(ctx, "alpha", "/h.md", "needle h alpha v2", h.read(ctx, "alpha", "/h.md")["version"].(string))

	alphaIDs, defaultIDs := h.historyIDs(ctx, "alpha", "/h.md"), h.historyIDs(ctx, files.DefaultProject, "/h.md")
	require.NotEmpty(t, alphaIDs)
	require.NotEmpty(t, defaultIDs)
	for _, id := range alphaIDs {
		require.NotContains(t, defaultIDs, id, "two independent /h.md files never share one list")
	}
	require.Equal(t, defaultIDs, h.historyIDs(ctx, "beta", "/h.md"), "beta resolves to the default")
	alphaV1 := h.historyIDWithContent(ctx, "alpha", "/h.md", "needle h alpha v1")
	defaultV1 := h.historyIDWithContent(ctx, "beta", "/h.md", "needle h default v1")

	// Another layer's ID is NOT_FOUND for read and restore, and nothing changes.
	alpha, inherited := h.read(ctx, "alpha", "/h.md"), h.read(ctx, "beta", "/h.md")
	readVersion := func(project, id string) string {
		return h.fail(ctx, h.readVersionTool, map[string]any{"project": project, "path": "/h.md", "history_id": id})
	}
	notFound := string(files.ErrCodeNotFound)
	require.Equal(t, notFound, readVersion("alpha", defaultV1))
	require.Equal(t, notFound, readVersion(files.DefaultProject, alphaV1))
	require.Equal(t, notFound, readVersion("beta", alphaV1))
	require.Equal(t, notFound, h.fail(ctx, h.restoreVersionTool, map[string]any{
		"project": "alpha", "path": "/h.md", "history_id": defaultV1, "expected_version": alpha["version"],
	}))
	require.Equal(t, notFound, h.fail(ctx, h.restoreVersionTool, map[string]any{
		"project": "beta", "path": "/h.md", "history_id": alphaV1, "expected_version": inherited["version"],
	}))
	require.Equal(t, alpha, h.read(ctx, "alpha", "/h.md"))
	require.Equal(t, inherited, h.read(ctx, files.DefaultProject, "/h.md"))
	require.Equal(t, alphaIDs, h.historyIDs(ctx, "alpha", "/h.md"))
	require.Equal(t, defaultIDs, h.historyIDs(ctx, files.DefaultProject, "/h.md"))

	// The project's own snapshot restores into the project only.
	h.call(ctx, h.restoreVersionTool, map[string]any{"project": "alpha", "path": "/h.md", "history_id": alphaV1, "expected_version": alpha["version"]})
	require.Equal(t, "needle h alpha v1", h.content(ctx, "alpha", "/h.md"))
	require.Equal(t, "needle h default v2", h.content(ctx, "beta", "/h.md"))

	// Resetting the override reveals the default history only.
	h.call(ctx, h.deleteTool, map[string]any{"project": "alpha", "path": "/h.md", "expected_version": h.read(ctx, "alpha", "/h.md")["version"]})
	require.Equal(t, h.historyIDs(ctx, files.DefaultProject, "/h.md"), h.historyIDs(ctx, "alpha", "/h.md"))
	require.Equal(t, notFound, readVersion("alpha", alphaV1))

	// Another API key sees no history at all.
	for _, project := range []string{"alpha", "beta", files.DefaultProject} {
		require.Empty(t, h.historyIDs(foreign, project, "/h.md"))
		require.Equal(t, notFound, h.fail(foreign, h.readVersionTool, map[string]any{"project": project, "path": "/h.md", "history_id": defaultV1}))
	}
}

// resettingHistory delegates history reads, then runs reset once: a change that
// commits between a restore's history read and its conditional write.
type resettingHistory struct {
	files.HistoryReader
	reset func()
}

// ReadVersion reads the snapshot and then runs the pending reset.
func (r *resettingHistory) ReadVersion(ctx context.Context, auth files.AuthContext, project, path string, id uint64) (files.FileVersion, error) {
	version, err := r.HistoryReader.ReadVersion(ctx, auth, project, path, id)
	if reset := r.reset; reset != nil {
		r.reset = nil
		reset()
	}
	return version, err
}

// TestFileToolsRestoreNeverMovesASnapshotAcrossLayers reproduces a two-step
// restore whose write lands in another layer: the caller passes the hidden
// default's version with the override's snapshot ID, and the override is
// deleted between the history read and the write. The write's condition then
// matches the default, so without checking the condition against the file the
// snapshot was read from, the override's bytes would replace the shared default.
func TestFileToolsRestoreNeverMovesASnapshotAcrossLayers(t *testing.T) {
	h := newOverlayToolHarness(t)
	ctx := h.tenant
	h.create(ctx, files.DefaultProject, "/r.md", "needle r default")
	h.override(ctx, "alpha", "/r.md", "needle r alpha v1")
	h.update(ctx, "alpha", "/r.md", "needle r alpha v2", h.read(ctx, "alpha", "/r.md")["version"].(string))
	snapshot := h.historyIDWithContent(ctx, "alpha", "/r.md", "needle r alpha v1")
	hidden := h.read(ctx, files.DefaultProject, "/r.md")
	override := h.read(ctx, "alpha", "/r.md")
	defaultHistory := h.historyIDs(ctx, files.DefaultProject, "/r.md")

	racing := &resettingHistory{HistoryReader: h.svc, reset: func() {
		h.call(ctx, h.deleteTool, map[string]any{"project": "alpha", "path": "/r.md", "expected_version": override["version"]})
	}}
	tools, err := NewFileHistoryTools(h.manager, racing)
	require.NoError(t, err)
	var restore Tool
	for _, candidate := range tools {
		if candidate.Definition().Name == FileRestoreVersionToolName {
			restore = candidate
		}
	}
	require.NotNil(t, restore)

	require.Equal(t, string(files.ErrCodeVersionConflict), h.fail(ctx, restore, map[string]any{
		"project": "alpha", "path": "/r.md", "history_id": snapshot, "expected_version": hidden["version"],
	}))
	require.Nil(t, racing.reset, "precondition: the override was deleted between the read and the write")
	require.Equal(t, hidden, h.read(ctx, files.DefaultProject, "/r.md"), "the shared default is untouched")
	require.Equal(t, hidden, h.read(ctx, "alpha", "/r.md"))
	require.Equal(t, defaultHistory, h.historyIDs(ctx, files.DefaultProject, "/r.md"))
}
