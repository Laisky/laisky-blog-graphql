package dao

import (
	"context"

	"laisky-blog-graphql/internal/web/general/model"
	"laisky-blog-graphql/library/db"

	"cloud.google.com/go/firestore"
)

const (
	locksColName = "Locks"
)

var Instance *Type

type Type struct {
	*db.Firestore
}

func Initialize(ctx context.Context) {
	model.Initialize(ctx)

	Instance = &Type{
		Firestore: model.GeneralDB,
	}
}

func (d *Type) GetLocksCol() *firestore.CollectionRef {
	return d.Collection(locksColName)
}
