package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Post blog posts
type Post struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt  time.Time          `bson:"post_created_at" json:"created_at"`
	ModifiedAt time.Time          `bson:"post_modified_gmt" json:"modified_at"`
	Title      string             `bson:"post_title" json:"title"`
	Type       string             `bson:"post_type" json:"type"`
	Status     string             `bson:"post_status" json:"status"`
	Name       string             `bson:"post_name" json:"name"`
	Content    string             `bson:"post_content" json:"content"`
	Markdown   string             `bson:"post_markdown" json:"markdown"`
	Author     primitive.ObjectID `bson:"post_author" json:"author"`
	Menu       string             `bson:"post_menu" json:"menu"`
	Password   string             `bson:"post_password" json:"password"`
	Category   primitive.ObjectID `bson:"category,omitempty" json:"category"`
	Tags       []string           `bson:"post_tags" json:"tags"`
	Hidden     bool               `bson:"hidden" json:"hidden"`
}

// User blog users
type User struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	Username string             `bson:"username" json:"username"`
	Account  string             `bson:"account" json:"account"`
	Password string             `bson:"password" json:"password"`
}

// Category blog post categories
type Category struct {
	ID   primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	Name string             `bson:"name" json:"name"`
	URL  string             `bson:"url" json:"url"`
}

// PostSeries blog post series
type PostSeries struct {
	ID     primitive.ObjectID   `bson:"_id,omitempty" json:"mongo_id"`
	Key    string               `bson:"key" json:"key"`
	Remark string               `bson:"remark" json:"remark"`
	Posts  []primitive.ObjectID `bson:"posts" json:"posts"`
	// Chidlren child series
	Chidlren []primitive.ObjectID `bson:"children" json:"children"`
}

func (u *User) GetID() string {
	return u.ID.Hex()
}

func (u *User) GetPayload() map[string]interface{} {
	return map[string]interface{}{
		"display_name": u.Username,
		"account":      u.Account,
	}
}
