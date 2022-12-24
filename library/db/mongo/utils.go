package mongo

import mongoLib "go.mongodb.org/mongo-driver/mongo"

func NotFound(err error) bool {
	return err == mongoLib.ErrNoDocuments
}
