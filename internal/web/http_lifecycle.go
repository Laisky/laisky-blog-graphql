package web

import (
	"context"
	stderrors "errors"
	"net"
	"net/http"
	"time"

	"github.com/Laisky/errors/v2"
)

// serveHTTP stops admission before draining in-flight requests. Exporter shutdown
// belongs to the caller after this returns, so completion logs can still export.
func serveHTTP(ctx context.Context, server *http.Server, listener net.Listener) error {
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		if stderrors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.Wrap(err, "serve HTTP")
	case <-ctx.Done():
		drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(drainCtx)
		if shutdownErr != nil {
			if closeErr := server.Close(); closeErr != nil {
				return errors.Wrap(closeErr, "close HTTP after drain deadline")
			}
		}
		serveErr := <-served
		if shutdownErr != nil {
			return errors.Wrap(shutdownErr, "drain HTTP")
		}
		if !stderrors.Is(serveErr, http.ErrServerClosed) {
			return errors.Wrap(serveErr, "serve HTTP")
		}
		return nil
	}
}
