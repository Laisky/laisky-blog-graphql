package pageindex

import (
	"encoding/json"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

func pageIndexSummaryInput(summary string, source files.SummarySource, contentHash string) files.PluginSummaryInput {
	status := files.SummaryStatusReady
	if source == files.SummarySourceDeterministicFallback {
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

func pageIndexRenameState(src, dst string, overwrite bool) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		prefix := strings.TrimSuffix(src, "/") + "/"
		type move struct {
			from string
			to   string
			row  IndexEntry
		}
		moves := make([]move, 0)
		for userPath, entry := range index {
			if userPath == src {
				moves = append(moves, move{from: userPath, to: dst, row: entry})
				continue
			}
			if strings.HasPrefix(userPath, prefix) {
				moves = append(moves, move{from: userPath, to: dst + strings.TrimPrefix(userPath, src), row: entry})
			}
		}
		if overwrite {
			for userPath, entry := range index {
				for _, candidate := range moves {
					if userPath == candidate.to || strings.HasPrefix(userPath, strings.TrimSuffix(candidate.to, "/")+"/") {
						delete(state, treePath(entry.DocID))
						delete(index, userPath)
						break
					}
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
