package dao

import (
	"laisky-blog-graphql/internal/web/twitter/model"

	"github.com/pkg/errors"
	"gorm.io/gorm"
)

type Search struct {
	db *gorm.DB
}

func NewSearch(db *gorm.DB) *Search {
	return &Search{
		db: db,
	}
}

func (s *Search) SearchByText(text string) (tweetIDs []string, err error) {
	var tweets []model.SearchTweet
	err = s.db.Model(model.SearchTweet{}).
		// Where("text LIKE ?", "%"+text+"%").
		Where("match(text, ?)", text).
		Find(&tweets).Error
	if err != nil {
		return nil, errors.Wrapf(err, "search text `%s", text)
	}

	for i := range tweets {
		tweetIDs = append(tweetIDs, tweets[i].ID)
	}

	return tweetIDs, nil
}
