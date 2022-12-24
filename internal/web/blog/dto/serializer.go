package dto

import "go.mongodb.org/mongo-driver/bson/primitive"

type PostInfo struct {
	Total int `json:"total"`
}

type PostCfg struct {
	ID                 primitive.ObjectID
	Page, Size, Length int
	Name, Tag, Regexp  string
	CategoryURL        *string
}
