package dao

import (
	"context"

	"github.com/Laisky/errors"
	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/model"
	"gorm.io/gorm"
)

type Search interface {
	SearchByText(ctx context.Context, text string) (tweetIDs []string, err error)
}

type sqlSearch struct {
	db *gorm.DB
}

func NewSQLSearch(db *gorm.DB) Search {
	return &sqlSearch{
		db: db,
	}
}

func (s *sqlSearch) SearchByText(ctx context.Context, text string) (tweetIDs []string, err error) {
	var tweets []model.SearchTweet
	err = s.db.Model(model.SearchTweet{}).
		// Where("text LIKE ?", "%"+text+"%").
		Where("match(text, ?)", text).
		Order("created_at DESC").
		Find(&tweets).Error
	if err != nil {
		return nil, errors.Wrapf(err, "search text `%s", text)
	}

	for i := range tweets {
		tweetIDs = append(tweetIDs, tweets[i].TweetID)
	}

	return tweetIDs, nil
}
