package cmd

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// runAPIWithLogging scopes the private log SDK to the API command, leaving
// maintenance commands and their existing alert behavior unchanged.
func runAPIWithLogging(ctx context.Context, run func(context.Context) error) (runErr error) {
	shutdown, err := log.ConfigureOTLP(ctx)
	if err != nil {
		return errors.Wrap(err, "initialize application logging")
	}
	defer func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdown(drainCtx); err != nil {
			runErr = errors.WithStack(errors.Join(runErr, err))
		}
	}()
	return run(ctx)
}
