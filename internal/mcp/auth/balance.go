package auth

import (
	"context"

	errors "github.com/Laisky/errors/v2"
)

// Balance returns the one-api quota of apiKey and validates the key in the
// same call. Answers are reused for a short time per key hash, and every
// upstream call is charged to the caller's per-source and the global budget.
// It fails like Authenticate: ErrInvalidAuthorization for a malformed or
// oversized key, ErrCredentialRejected or ErrValidationUnavailable.
func (a *Authenticator) Balance(ctx context.Context, apiKey string) (Balance, error) {
	candidate, err := a.parseCandidate("Bearer " + apiKey)
	if err != nil {
		return Balance{}, errors.WithStack(err)
	}
	fetcher, ok := a.validator.(BalanceValidator)
	if !ok {
		return Balance{}, errors.Wrap(ErrValidationUnavailable, "the configured validator reports no balance")
	}

	key := candidate.APIKeyHash
	now := a.opts.Now()
	if a.verdicts.trust(key, now).level == trustRejected {
		return Balance{}, errors.WithStack(ErrCredentialRejected)
	}
	if balance, cached := a.verdicts.balance(key, now); cached {
		return balance, nil
	}

	value, err := a.coalesce(ctx, "balance:"+key, "wait for one-api balance", func() (any, error) {
		var balance Balance
		err := a.upstream(ctx, key, callNewKey, callLabelBalance, func(callCtx context.Context) error {
			var fetchErr error
			balance, fetchErr = fetcher.FetchBalance(callCtx, candidate.APIKey)
			return errors.WithStack(fetchErr)
		})
		if err != nil {
			return Balance{}, errors.WithStack(err)
		}
		a.verdicts.putBalance(key, balance)
		return balance, nil
	})
	if err != nil {
		return Balance{}, errors.WithStack(err)
	}
	balance, _ := value.(Balance)
	return balance, nil
}
