package model

import "go.mongodb.org/mongo-driver/bson/primitive"

type Movice struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	ActressID   primitive.ObjectID `bson:"actress_id" json:"actress_id"`
	Description string             `bson:"description" json:"description"`
	ImgUrl      string             `bson:"img_url" json:"img_url"`
	Name        string             `bson:"name" json:"name"`
	Tags        []string           `bson:"tags" json:"tags"`
}
