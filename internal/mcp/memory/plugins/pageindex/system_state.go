package pageindex

import (
	"encoding/json"
	"strconv"
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

func pageIndexDeleteState(target string, recursive bool) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		prefix := strings.TrimSuffix(target, "/") + "/"
		for userPath, entry := range index {
			if userPath != target && (!recursive || !strings.HasPrefix(userPath, prefix)) {
				continue
			}
			delete(state, treePath(entry.DocID))
			delete(index, userPath)
		}
		return pageIndexWriteIndexState(state, index)
	}
}

// pageIndexMove is one catalog entry moved by a rename.
type pageIndexMove struct {
	from string
	to   string
	row  IndexEntry
}

// pageIndexRenameMoves returns the catalog entries at src or under it, mapped
// onto dst the same way the user-file rename maps their paths.
func pageIndexRenameMoves(index Index, src, dst string) []pageIndexMove {
	prefix := strings.TrimSuffix(src, "/") + "/"
	moves := make([]pageIndexMove, 0)
	for userPath, entry := range index {
		if userPath == src {
			moves = append(moves, pageIndexMove{from: userPath, to: dst, row: entry})
			continue
		}
		if strings.HasPrefix(userPath, prefix) {
			moves = append(moves, pageIndexMove{from: userPath, to: dst + strings.TrimPrefix(userPath, src), row: entry})
		}
	}
	return moves
}

// pageIndexOverwritten reports whether userPath is replaced by a move destination.
func pageIndexOverwritten(userPath string, moves []pageIndexMove) bool {
	for _, candidate := range moves {
		if userPath == candidate.to || strings.HasPrefix(userPath, strings.TrimSuffix(candidate.to, "/")+"/") {
			return true
		}
	}
	return false
}

func pageIndexRenameState(src, dst string, overwrite bool) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		moves := pageIndexRenameMoves(index, src, dst)
		if overwrite {
			for userPath, entry := range index {
				if pageIndexOverwritten(userPath, moves) {
					delete(state, treePath(entry.DocID))
					delete(index, userPath)
				}
			}
		}
		for _, item := range moves {
			delete(index, item.from)
		}
		for _, item := range moves {
			index[item.to] = item.row
		}
		return pageIndexWriteIndexState(state, index)
	}
}

// pageIndexMoveDocIDAttempts bounds the search for a free target doc_id.
const pageIndexMoveDocIDAttempts = 16

// pageIndexMoveState moves the catalog entries and trees of a cross-project
// rename from the source tenant namespace into the target one. Each moved tree
// is re-keyed to targetDocID(destination), the doc_id a later write of that
// path in the target project derives, unless a remaining target entry already
// uses it; then the original doc_id or a salted derivation is used. A tree is
// removed from either namespace only when no remaining entry references it.
func pageIndexMoveState(src, dst string, overwrite bool, targetDocID func(userPath string) string) files.SystemStateMoveMutator {
	return func(source, target map[string][]byte) error {
		sourceIndex, err := pageIndexDecodeState(source)
		if err != nil {
			return err
		}
		targetIndex, err := pageIndexDecodeState(target)
		if err != nil {
			return err
		}
		moves := pageIndexRenameMoves(sourceIndex, src, dst)
		var overwritten []string
		if overwrite {
			for userPath, entry := range targetIndex {
				if pageIndexOverwritten(userPath, moves) {
					overwritten = append(overwritten, entry.DocID)
					delete(targetIndex, userPath)
				}
			}
		}
		if len(moves) == 0 && len(overwritten) == 0 {
			return nil
		}
		pageIndexDropUnreferencedTrees(target, targetIndex, overwritten)

		used := pageIndexReferencedDocIDs(targetIndex)
		movedDocIDs := make([]string, 0, len(moves))
		for _, item := range moves {
			delete(sourceIndex, item.from)
			movedDocIDs = append(movedDocIDs, item.row.DocID)
			docID, err := pageIndexAllocateDocID(item, used, targetDocID)
			if err != nil {
				return err
			}
			used[docID] = struct{}{}
			if body, ok := source[treePath(item.row.DocID)]; ok {
				rekeyed, err := pageIndexRekeyTree(body, item.row.DocID, docID)
				if err != nil {
					return err
				}
				target[treePath(docID)] = rekeyed
			}
			item.row.DocID = docID
			targetIndex[item.to] = item.row
		}
		pageIndexDropUnreferencedTrees(source, sourceIndex, movedDocIDs)
		if err := pageIndexWriteIndexState(source, sourceIndex); err != nil {
			return err
		}
		return pageIndexWriteIndexState(target, targetIndex)
	}
}

// pageIndexAllocateDocID picks the first candidate doc_id no remaining target
// entry references.
func pageIndexAllocateDocID(item pageIndexMove, used map[string]struct{}, targetDocID func(string) string) (string, error) {
	candidates := []string{targetDocID(item.to), item.row.DocID}
	for attempt := 1; attempt < pageIndexMoveDocIDAttempts; attempt++ {
		candidates = append(candidates, targetDocID(item.to+"\x00"+strconv.Itoa(attempt)))
	}
	for _, candidate := range candidates {
		if _, taken := used[candidate]; !taken {
			return candidate, nil
		}
	}
	return "", errors.Errorf("no free pageindex doc_id for %s", item.to)
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

// pageIndexRekeyTree returns the tree body stored under a new doc_id.
func pageIndexRekeyTree(body []byte, fromDocID, toDocID string) ([]byte, error) {
	if fromDocID == toDocID {
		return body, nil
	}
	var tree Tree
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, errors.Wrap(err, "decode pageindex tree for move")
	}
	tree.DocID = toDocID
	rekeyed, err := json.MarshalIndent(&tree, "", "  ")
	if err != nil {
		return nil, errors.Wrap(err, "encode moved pageindex tree")
	}
	return rekeyed, nil
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
