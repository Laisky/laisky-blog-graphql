// Package model contains all the models used in the application.
package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Post blog posts
type Post struct {
	// ID unique identifier for the post
	ID primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	// CreatedAt time when the post was created
	CreatedAt time.Time `bson:"post_created_at" json:"created_at"`
	// ModifiedAt time when the post was last modified
	ModifiedAt time.Time `bson:"post_modified_gmt" json:"modified_at"`
	// Title title of the post
	Title string `bson:"post_title" json:"title"`
	// Type type of the post
	Type string `bson:"post_type" json:"type"`
	// Status status of the post
	Status string `bson:"post_status" json:"status"`
	// Name name of the post
	Name string `bson:"post_name" json:"name"`
	// Content content of the post
	Content string `bson:"post_content" json:"content"`
	// Markdown markdown content of the post
	Markdown string `bson:"post_markdown" json:"markdown"`
	// Author author of the post
	Author primitive.ObjectID `bson:"post_author,omitempty" json:"author"`
	// Menu menu of the post
	Menu string `bson:"post_menu" json:"menu"`
	// Password password of the post
	Password string `bson:"post_password" json:"password"`
	// Category category of the post
	Category primitive.ObjectID `bson:"category,omitempty" json:"category"`
	// Tags tags of the post
	Tags []string `bson:"post_tags" json:"tags"`
	// Hidden whether the post is hidden or not
	Hidden bool `bson:"hidden" json:"hidden"`
	// I18N internationalization of the post
	I18N PostI18N `bson:"i18n" json:"i18n"`
	// Language language of the post content or markdown
	Language string `bson:"-" json:"language"`
}

// PostI18N blog post internationalization
type PostI18N struct {
	// UpdateAt time when the post was last modified
	UpdateAt time.Time `bson:"update_at" json:"update_at"`
	// EnUs english version
	EnUs PostI18NLanguage `bson:"en_us" json:"en_us"`
}

// PostI18NLanguage blog post internationalization language
type PostI18NLanguage struct {
	PostMarkdown string `bson:"post_markdown" json:"post_markdown"`
	PostContent  string `bson:"post_content" json:"post_content"`
	PostTitle    string `bson:"post_title" json:"post_title"`
}

// Category blog post categories
type Category struct {
	// ID unique identifier for the category
	ID primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	// Name name of the category
	Name string `bson:"name" json:"name"`
	// URL url of the category
	URL string `bson:"url" json:"url"`
}

// PostSeries blog post series
type PostSeries struct {
	// ID unique identifier for the post series
	ID primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	// Key key of the post series
	Key string `bson:"key" json:"key"`
	// Remark remark of the post series
	Remark string `bson:"remark" json:"remark"`
	// Posts posts of the post series
	Posts []primitive.ObjectID `bson:"posts" json:"posts"`
	// Chidlren child series
	Chidlren []primitive.ObjectID `bson:"children" json:"children"`
}

// PostTags blog post tags
type PostTags struct {
	// ID unique identifier for the post tags
	ID primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	// Keywords keywords of the post tags
	Keywords []string `bson:"keywords" json:"keywords"`
	// ModifiedAtGMT last modified time of the post tags
	ModifiedAtGMT time.Time `bson:"modified_at_gmt" json:"modified_at_gmt"`
}
