package dto

import "go.mongodb.org/mongo-driver/bson/primitive"

// PostInfo blog post info
type PostInfo struct {
	Total int `json:"total"`
}

// PostCfg blog post config
type PostCfg struct {
	ID                 primitive.ObjectID
	Page, Size, Length int
	Name, Tag, Regexp  string
	CategoryURL        *string
}
