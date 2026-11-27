package pageindex

import (
	"encoding/json"
	"strings"
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

// pageIndexWriteState publishes the tree of userPath under docID, the doc_id
// derived from its project and path (see pageIndexClaimDocID).
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
		previous := index[userPath].DocID
		pageIndexClaimDocID(index, userPath, docID)
		index[userPath] = entry
		state[treePath(docID)] = treeBody
		pageIndexDropUnreferencedTrees(state, index, []string{previous})
		return pageIndexWriteIndexState(state, index)
	}
}

// pageIndexClaimDocID enforces that a path owns the doc_id derived from its
// project and path. Releases that kept the doc_id on rename left entries
// using another path's doc_id; such an entry is dropped when that path is
// written or receives a move, because its tree is about to be replaced.
func pageIndexClaimDocID(index Index, userPath, docID string) {
	for path, entry := range index {
		if path != userPath && entry.DocID == docID {
			delete(index, path)
		}
	}
}

func pageIndexDeleteState(target string, recursive bool) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		prefix := strings.TrimSuffix(target, "/") + "/"
		var removed []string
		for userPath, entry := range index {
			if userPath != target && (!recursive || !strings.HasPrefix(userPath, prefix)) {
				continue
			}
			removed = append(removed, entry.DocID)
			delete(index, userPath)
		}
		pageIndexDropUnreferencedTrees(state, index, removed)
		return pageIndexWriteIndexState(state, index)
	}
}

// pageIndexReferencedDocIDs returns the doc_ids referenced by index entries.
func pageIndexReferencedDocIDs(index Index) map[string]struct{} {
	used := make(map[string]struct{}, len(index))
	for _, entry := range index {
		used[entry.DocID] = struct{}{}
	}
	return used
}

// pageIndexDropUnreferencedTrees deletes the trees of docIDs that no remaining
// entry of index still references.
func pageIndexDropUnreferencedTrees(state map[string][]byte, index Index, docIDs []string) {
	referenced := pageIndexReferencedDocIDs(index)
	for _, docID := range docIDs {
		if _, keep := referenced[docID]; !keep {
			delete(state, treePath(docID))
		}
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
	indexBody, metaBody, err := pageIndexEncodeIndex(index)
	if err != nil {
		return err
	}
	state[indexPath()] = indexBody
	state[metaPath()] = metaBody
	return nil
}

// pageIndexEncodeIndex encodes the catalog and its metadata.
func pageIndexEncodeIndex(index Index) (indexBody, metaBody []byte, err error) {
	indexBody, err = json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, nil, errors.Wrap(err, "encode pageindex index")
	}
	metaBody, err = json.MarshalIndent(Meta{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Count:     len(index),
	}, "", "  ")
	if err != nil {
		return nil, nil, errors.Wrap(err, "encode pageindex metadata")
	}
	return indexBody, metaBody, nil
}
