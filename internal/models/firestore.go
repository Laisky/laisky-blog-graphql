package models

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/pkg/errors"
	"google.golang.org/api/option"
)

type Firestore struct {
	*firestore.Client
	projectID string
}

// NewFirestore create firestore client
func NewFirestore(ctx context.Context, projectID string, opts ...option.ClientOption) (db *Firestore, err error) {
	db = &Firestore{
		projectID: projectID,
	}
	var cli *firestore.Client
	if cli, err = firestore.NewClient(ctx, projectID, opts...); err != nil {
		return nil, errors.Wrap(err, "create firestore client")
	}

	db.Client = cli
	return db, nil
}
