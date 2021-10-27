package general

import (
	"laisky-blog-graphql/library/db"

	"cloud.google.com/go/firestore"
)

const (
	locksColName = "Locks"
)

type Dao struct {
	db *db.Firestore
}

func NewDao(db *db.Firestore) *Dao {
	return &Dao{
		db: db,
	}
}

func (d *Dao) GetLocksCol() *firestore.CollectionRef {
	return d.db.Collection(locksColName)
}
