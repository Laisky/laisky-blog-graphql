package model

import "time"

type SearchTweet struct {
	TweetID   string     `gorm:"column:tweet_id" json:"tweet_id"`
	Text      string     `gorm:"column:text" json:"text"`
	UserID    string     `gorm:"column:user_id" json:"user_id"`
	CreatedAt *time.Time `gorm:"column:created_at" json:"created_at"`
}

func (SearchTweet) TableName() string {
	return "tweets"
}
