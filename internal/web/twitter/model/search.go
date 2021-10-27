package model

type SearchTweet struct {
	ID     string `gorm:"column:id" json:"id"`
	Text   string `gorm:"column:text" json:"text"`
	UserID string `gorm:"column:user_id" json:"user_id"`
}

func (SearchTweet) TableName() string {
	return "tweets"
}
