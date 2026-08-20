package main

import (
	"context"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/benchmark"
)

func TestCloseBenchmarkBackendPropagatesCloseFailure(t *testing.T) {
	t.Parallel()
	backend := &closeErrorBackend{closeErr: errors.New("close failed")}
	err := closeBenchmarkBackend(context.Background(), backend, nil)
	require.ErrorContains(t, err, "close benchmark backend")
	require.ErrorContains(t, err, "close failed")
	require.True(t, backend.closed)
}

func TestCloseBenchmarkBackendPreservesEarlierFailure(t *testing.T) {
	t.Parallel()
	backend := &closeErrorBackend{closeErr: errors.New("close failed")}
	runErr := errors.New("run failed")
	err := closeBenchmarkBackend(context.Background(), backend, runErr)
	require.ErrorIs(t, err, runErr)
	require.True(t, backend.closed)
}

type closeErrorBackend struct {
	closed   bool
	closeErr error
}

func (b *closeErrorBackend) Name() string { return "close-error-test" }

func (b *closeErrorBackend) Write(context.Context, string, benchmark.Document) error { return nil }

func (b *closeErrorBackend) Search(context.Context, string, benchmark.Query, int) ([]benchmark.SearchHit, error) {
	return nil, nil
}

func (b *closeErrorBackend) Delete(context.Context, string, string, bool) error { return nil }

func (b *closeErrorBackend) Close(context.Context) error {
	b.closed = true
	return b.closeErr
}
