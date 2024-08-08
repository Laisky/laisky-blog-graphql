package model

import "go.mongodb.org/mongo-driver/bson/primitive"

type Actress struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	Name        string             `bson:"name" json:"name"`
	NameEnglish string             `bson:"name_english" json:"name_english"`
	URL         string             `bson:"url" json:"url"`
}
