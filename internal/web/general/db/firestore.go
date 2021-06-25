package db

import (
	"laisky-blog-graphql/library/db"

	"cloud.google.com/go/firestore"
)

const (
	locksColName = "Locks"
)

type DB struct {
	*db.Firestore
}

func NewDB(db *db.Firestore) *DB {
	return &DB{
		db,
	}
}

func (db *DB) GetLocksCol() *firestore.CollectionRef {
	return db.Collection(locksColName)
}
