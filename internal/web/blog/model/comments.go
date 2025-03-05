package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Comment represents a comment in the blog
type Comment struct {
	// ID is the unique identifier for the comment
	ID primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	// CreatedAt records when the comment was first submitted
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
	// Author contains information about the comment author
	Author CommentUser `bson:"author" json:"author"`
	// PostID is the identifier of the blog post this comment belongs to
	PostID primitive.ObjectID `bson:"post_id" json:"post_id"`
	// ParentID references the parent comment's ID if this is a reply, null for top-level comments
	ParentID *primitive.ObjectID `bson:"parent_id,omitempty" json:"parent_id,omitempty"`
	// UpdatedAt tracks when the comment was last modified
	// Content contains the actual text/body of the comment
	Content string `bson:"content" json:"content"`
	// IsApproved indicates whether the comment has been approved by a moderator
	IsApproved bool `bson:"is_approved" json:"is_approved"`
	// Not stored in database, populated at runtime when retrieving comments
	Replies []*Comment `bson:"-" json:"replies,omitempty"`
	Likes   int        `json:"likes"`
}

// Collection returns the name of the MongoDB collection for comments
func (Comment) Collection() string {
	return "comments"
}

// CommentUser contains information about a comment author
type CommentUser struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	// Name is the display name of the comment author
	Name string `bson:"name" json:"name"`
	// Email is the email address of the commenter (only visible to admins)
	Email string `bson:"email" json:"-"` // Not exposed in API
	// Website is an optional URL/website provided by the commenter
	Website *string `bson:"website,omitempty" json:"website,omitempty"`
}

// Collection returns the name of the MongoDB collection for comment authors
func (CommentUser) Collection() string {
	return "comment_users"
}

type CommentLike struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	CommentID primitive.ObjectID `bson:"comment_id" json:"comment_id"`
	Author    CommentUser        `bson:"author" json:"author"`
}

// Collection returns the name of the MongoDB collection for comment likes
func (CommentLike) Collection() string {
	return "comment_likes"
}
