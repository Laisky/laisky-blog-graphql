package dao

import (
	"context"
	"database/sql"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/model"
)

type Search interface {
	SearchByText(ctx context.Context, text string) (tweetIDs []string, err error)
}

type sqlSearch struct {
	db *sql.DB
}

// NewSQLSearch creates a SQL-backed tweet search DAO.
func NewSQLSearch(db *sql.DB) Search {
	return &sqlSearch{
		db: db,
	}
}

func (s *sqlSearch) SearchByText(ctx context.Context, text string) (tweetIDs []string, err error) {
	const query = `
SELECT tweet_id, text, user_id, created_at
FROM tweets
WHERE match(text, $1)
ORDER BY created_at DESC
`

	rows, err := s.db.QueryContext(ctx, query, text)
	if err != nil {
		return nil, errors.Wrapf(err, "search text `%s", text)
	}
	defer rows.Close()

	var tweets []model.SearchTweet
	for rows.Next() {
		var tweet model.SearchTweet
		if scanErr := rows.Scan(&tweet.TweetID, &tweet.Text, &tweet.UserID, &tweet.CreatedAt); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan tweet")
		}
		tweets = append(tweets, tweet)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, errors.Wrap(rowsErr, "iterate tweets")
	}

	for i := range tweets {
		tweetIDs = append(tweetIDs, tweets[i].TweetID)
	}

	return tweetIDs, nil
}
