package eval

import (
	"context"
	"fmt"
	"sort"
	"sync"

	errors "github.com/Laisky/errors/v2"
	"golang.org/x/sync/errgroup"
)

// LabelledJudge wraps an LLMJudge with a stable identifier for ensemble logs.
type LabelledJudge struct {
	ID    string
	Judge LLMJudge
}

// EnsembleJudge dispatches each prompt to N judges in parallel and aggregates
// outputs. Booleans go via majority-true; floats go via mean.
type EnsembleJudge struct {
	judges []LabelledJudge
}

// NewEnsembleJudge constructs the ensemble. Order of `judges` is preserved.
func NewEnsembleJudge(judges []LabelledJudge) (*EnsembleJudge, error) {
	if len(judges) == 0 {
		return nil, errors.New("ensemble requires at least one judge")
	}
	for i, j := range judges {
		if j.Judge == nil {
			return nil, errors.Errorf("judge %d is nil", i)
		}
		if j.ID == "" {
			return nil, errors.Errorf("judge %d has empty ID", i)
		}
	}
	return &EnsembleJudge{judges: append([]LabelledJudge(nil), judges...)}, nil
}

// Judge dispatches the request to every member judge concurrently and
// aggregates their outputs by key:
//   - bool keys are majority-true (ties resolve to false)
//   - numeric keys are averaged
//   - other keys take the value of the first judge in declared order
//
// Token counts are summed across all member calls.
func (e *EnsembleJudge) Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error) {
	if e == nil || len(e.judges) == 0 {
		return JudgeResponse{}, errors.New("ensemble is empty")
	}

	responses := make([]JudgeResponse, len(e.judges))
	g, gctx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	for i, j := range e.judges {
		i, j := i, j
		g.Go(func() error {
			resp, err := j.Judge.Judge(gctx, req)
			if err != nil {
				return errors.Wrapf(err, "judge %s", j.ID)
			}
			resp.JudgeID = j.ID
			mu.Lock()
			responses[i] = resp
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return JudgeResponse{}, errors.WithStack(err)
	}

	merged := mergeOutputs(responses)
	total := JudgeResponse{Output: merged, JudgeID: ensembleID(e.judges)}
	for _, r := range responses {
		total.InputTokens += r.InputTokens
		total.OutputTokens += r.OutputTokens
		total.TotalTokens += r.TotalTokens
	}
	return total, nil
}

// MajorityVote returns the majority-true outcome over a slice of booleans.
// Ties resolve to false (defensive default — a tie does not warrant a "pass").
func MajorityVote(votes []bool) bool {
	trueCount := 0
	for _, v := range votes {
		if v {
			trueCount++
		}
	}
	return trueCount > len(votes)/2
}

func mergeOutputs(responses []JudgeResponse) map[string]any {
	keys := map[string]struct{}{}
	for _, r := range responses {
		for k := range r.Output {
			keys[k] = struct{}{}
		}
	}
	merged := make(map[string]any, len(keys))

	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	for _, k := range sortedKeys {
		// gather typed votes
		bools := make([]bool, 0, len(responses))
		floats := make([]float64, 0, len(responses))
		var firstNonNil any
		for _, r := range responses {
			v, ok := r.Output[k]
			if !ok {
				continue
			}
			if firstNonNil == nil {
				firstNonNil = v
			}
			switch x := v.(type) {
			case bool:
				bools = append(bools, x)
			case float64:
				floats = append(floats, x)
			case float32:
				floats = append(floats, float64(x))
			case int:
				floats = append(floats, float64(x))
			case int64:
				floats = append(floats, float64(x))
			}
		}
		switch {
		case len(bools) == len(responses) && len(bools) > 0:
			merged[k] = MajorityVote(bools)
		case len(floats) == len(responses) && len(floats) > 0:
			sum := 0.0
			for _, f := range floats {
				sum += f
			}
			merged[k] = sum / float64(len(floats))
		default:
			merged[k] = firstNonNil
		}
	}
	return merged
}

func ensembleID(judges []LabelledJudge) string {
	ids := make([]string, 0, len(judges))
	for _, j := range judges {
		ids = append(ids, j.ID)
	}
	sort.Strings(ids)
	return fmt.Sprintf("ensemble[%v]", ids)
}
