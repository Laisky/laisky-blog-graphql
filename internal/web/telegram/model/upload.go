package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type UploadFile struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt   time.Time          `bson:"created_at" json:"created_at"`
	FileID      string             `bson:"file_id" json:"file_id"`
	FileSize    int64              `bson:"file_size" json:"file_size"`
	TelegramUID int64              `bson:"telegram_uid" json:"telegram_uid"`
}

type UploadUser struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt   time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt   time.Time          `bson:"updated_at" json:"updated_at"`
	TelegramUID int64              `bson:"telegram_uid" json:"telegram_uid"`
	OneapiKey   string             `bson:"oneapi_key" json:"oneapi_key"`
}
