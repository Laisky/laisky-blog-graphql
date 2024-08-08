package model

import "go.mongodb.org/mongo-driver/bson/primitive"

type Fulltext struct {
	ID     primitive.ObjectID   `bson:"_id,omitempty" json:"mongo_id"`
	Word   string               `bson:"word" json:"word"`
	Movies []primitive.ObjectID `bson:"movies" json:"movies"`
}
