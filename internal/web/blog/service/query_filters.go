package service

import "go.mongodb.org/mongo-driver/bson/primitive"

// Fixed BSON shapes keep caller data in scalar values, never query keys or
// operators. Do not add omitempty: an empty identity must not become {}.
type postNameFilter struct {
	Name string `bson:"post_name"`
}

type documentIDFilter struct {
	ID primitive.ObjectID `bson:"_id"`
}

type userAccountFilter struct {
	Account string `bson:"account"`
}

type userUIDFilter struct {
	UID string `bson:"uid"`
}

type verificationIdentityFilter struct {
	Account string `bson:"account"`
	Purpose string `bson:"purpose"`
}
