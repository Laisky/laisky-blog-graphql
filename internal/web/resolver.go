package web

import (
	"context"
)

type Resolver struct{}

func (r *Resolver) Query() QueryResolver {
	return &queryResolver{r}
}

func (r *Resolver) Mutation() MutationResolver {
	return &mutationResolver{r}
}

// ===========================
// query
// ===========================

type queryResolver struct{ *Resolver }

// =================
// query resolver
// =================

func (r *queryResolver) Hello(ctx context.Context) (string, error) {
	return "hello, world", nil
}

// ============================
// mutations
// ============================
type mutationResolver struct{ *Resolver }
