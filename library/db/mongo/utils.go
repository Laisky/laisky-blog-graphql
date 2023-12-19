package mongo

import (
	"github.com/Laisky/errors/v2"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
)

func NotFound(err error) bool {
	return errors.Is(err, mongoLib.ErrNoDocuments)
}
