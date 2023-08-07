package dto

import (
	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// PostInfo blog post info
type PostInfo struct {
	Total int `json:"total"`
}

// PostCfg blog post config
type PostCfg struct {
	ID                 primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	Page, Size, Length int
	Name, Tag, Regexp  string
	CategoryURL        *string
	Language           models.Language
}
