package pageindex

import (
	"encoding/json"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// pageIndexSummaryInput builds the user-row summary publication. A deterministic
// fallback, or a tree whose LLM work was cut short (degradedReason non-empty), is
// published as degraded with the reason as its safe error code (§4.6).
func pageIndexSummaryInput(summary string, source files.SummarySource, contentHash, degradedReason string) files.PluginSummaryInput {
	status := files.SummaryStatusReady
	if source == files.SummarySourceDeterministicFallback || degradedReason != "" {
		status = files.SummaryStatusDegraded
	}
	return files.PluginSummaryInput{
		ExpectedContentHash: contentHash,
		Summary:             summary,
		WordCount:           files.SummaryWordCount(summary),
		Source:              source,
		Status:              status,
		PromptVersion:       AlgorithmVersion,
		GenerationKey:       contentHash,
		ErrorCode:           degradedReason,
	}
}

func pageIndexWriteState(docID, userPath string, tree *Tree, entry IndexEntry) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		treeBody, err := json.MarshalIndent(tree, "", "  ")
		if err != nil {
			return errors.Wrap(err, "encode pageindex tree")
		}
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		index[userPath] = entry
		indexBody, err := json.MarshalIndent(index, "", "  ")
		if err != nil {
			return errors.Wrap(err, "encode pageindex index")
		}
		metaBody, err := json.MarshalIndent(Meta{
			UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Count:     len(index),
		}, "", "  ")
		if err != nil {
			return errors.Wrap(err, "encode pageindex metadata")
		}
		state[treePath(docID)] = treeBody
		state[indexPath()] = indexBody
		state[metaPath()] = metaBody
		return nil
	}
}

// pageIndexStateHooks keeps each physical project's catalog in step with the
// user files a transactional delete or rename changed in that project.
func pageIndexStateHooks(auth files.AuthContext) files.SystemStateHooks {
	return files.SystemStateHooks{
		SystemProject: func(userProject string) string { return PageIndexStorageProject(auth, userProject) },
		Deleted:       pageIndexDeletePathsState,
		Moved:         pageIndexMovePathsState,
	}
}

// pageIndexDeletePathsState removes the catalog entries and trees of deleted user files.
func pageIndexDeletePathsState(paths []string) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		for _, userPath := range paths {
			entry, ok := index[userPath]
			if !ok {
				continue
			}
			delete(state, treePath(entry.DocID))
			delete(index, userPath)
		}
		return pageIndexWriteIndexState(state, index)
	}
}

// pageIndexMovePathsState re-keys the catalog entries of renamed user files. A
// stale entry already at a destination is replaced together with its tree.
func pageIndexMovePathsState(moves []files.PathMove) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		moved := make(map[string]IndexEntry, len(moves))
		for _, move := range moves {
			if entry, ok := index[move.From]; ok {
				moved[move.To] = entry
				delete(index, move.From)
			}
		}
		for userPath, entry := range moved {
			if stale, ok := index[userPath]; ok && stale.DocID != entry.DocID {
				delete(state, treePath(stale.DocID))
			}
			index[userPath] = entry
		}
		return pageIndexWriteIndexState(state, index)
	}
}

func pageIndexDecodeState(state map[string][]byte) (Index, error) {
	body := state[indexPath()]
	if len(body) == 0 {
		return Index{}, nil
	}
	var index Index
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, errors.Wrap(err, "decode pageindex index")
	}
	if index == nil {
		index = Index{}
	}
	return index, nil
}

func pageIndexWriteIndexState(state map[string][]byte, index Index) error {
	indexBody, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode pageindex index")
	}
	metaBody, err := json.MarshalIndent(Meta{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Count:     len(index),
	}, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode pageindex metadata")
	}
	state[indexPath()] = indexBody
	state[metaPath()] = metaBody
	return nil
}
