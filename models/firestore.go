package models

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/pkg/errors"
)

type Firestore struct {
	*firestore.Client
	projectID string
}

// NewFirestore create firestore client
func NewFirestore(ctx context.Context, projectID string) (db *Firestore, err error) {
	db = &Firestore{
		projectID: projectID,
	}
	var cli *firestore.Client
	if cli, err = firestore.NewClient(ctx, projectID); err != nil {
		return nil, errors.Wrap(err, "create firestore client")
	}

	db.Client = cli
	return db, nil
}
