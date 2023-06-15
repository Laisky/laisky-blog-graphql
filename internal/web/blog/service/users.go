package service

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

func (s *Blog) ValidateLogin(ctx context.Context, account, password string) (u *model.User, err error) {
	return s.dao.ValidateLogin(ctx, account, password)
}
