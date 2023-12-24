// Package dao provides data access object for general service.
package dao

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/web/general/model"
	fsDB "github.com/Laisky/laisky-blog-graphql/library/db/firestore"

	"cloud.google.com/go/firestore"
)

const (
	locksColName = "Locks"
)

var Instance *Type

type Type struct {
	*fsDB.DB
}

func Initialize(ctx context.Context) {
	model.Initialize(ctx)

	Instance = &Type{
		DB: model.GeneralDB,
	}
}

func (d *Type) GetLocksCol() *firestore.CollectionRef {
	return d.Collection(locksColName)
}
