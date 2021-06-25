package telegram

import (
	"time"

	"github.com/pkg/errors"
	"gopkg.in/mgo.v2/bson"
)

var (
	ErrAlreadyExists = errors.New("already exists")
)

type QueryCfg struct {
	Name       string
	Page, Size int
}

// AlertTypes type of alert
type AlertTypes struct {
	ID         bson.ObjectId `bson:"_id,omitempty" json:"mongo_id"`
	Name       string        `bson:"name" json:"name"`
	PushToken  string        `bson:"push_token" json:"push_token"`
	JoinKey    string        `bson:"join_key" json:"join_key"`
	CreatedAt  time.Time     `bson:"created_at" json:"created_at"`
	ModifiedAt time.Time     `bson:"modified_at" json:"modified_at"`
}

type Users struct {
	ID         bson.ObjectId `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt  time.Time     `bson:"created_at" json:"created_at"`
	ModifiedAt time.Time     `bson:"modified_at" json:"modified_at"`
	Name       string        `bson:"name" json:"name"`
	UID        int           `bson:"uid" json:"uid"`
}

type UserAlertRelations struct {
	ID           bson.ObjectId `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt    time.Time     `bson:"created_at" json:"created_at"`
	ModifiedAt   time.Time     `bson:"modified_at" json:"modified_at"`
	UserMongoID  bson.ObjectId `bson:"user_id" json:"user_id"`
	AlertMongoID bson.ObjectId `bson:"alert_id" json:"alert_id"`
}
