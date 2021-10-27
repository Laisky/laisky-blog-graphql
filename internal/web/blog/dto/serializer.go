package dto

import (
	"gopkg.in/mgo.v2/bson"
)

type PostInfo struct {
	Total int `json:"total"`
}

type PostCfg struct {
	ID                 bson.ObjectId
	Page, Size, Length int
	Name, Tag, Regexp  string
	CategoryURL        *string
}
